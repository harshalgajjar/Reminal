// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// shellCwdTimeout bounds the macOS lsof call. Seen in the wild: lsof pinning a
// core and never returning for a freshly spawned shell, while the same command
// by hand answered in 0.35s. That hung the AGENT, because this lookup runs
// during startup — before the relay registration that releases the parent's
// handshake — so `reminal new` failed with "detached reminal didn't report
// ready within 15s" and left a dead entry in the machine list. Worse, the
// periodic refresh left one spinning lsof per attempt, orphaned to pid 1,
// burning a core each.
//
// A session's Dir column is a nicety; it must never be able to stop a session
// starting. CommandContext also KILLS the child when the deadline passes, which
// is what stops the orphans accumulating.
const shellCwdTimeout = 2 * time.Second

// shellCwd returns the live working directory of the shell process with the
// given pid, or "" if it can't be determined. This lets `reminal list`'s Dir
// column follow the shell as it cd's, instead of being frozen at the directory
// the session was launched from.
//
//   - Linux: read /proc/<pid>/cwd — cheap, no subprocess.
//   - macOS: the proc_info syscall (see shellcwd_darwin.go) — there's no
//     /proc, and proc_pidinfo() itself needs cgo/libproc, but the syscall it
//     wraps does not. lsof remains a bounded fallback.
//   - Windows: read the target's PEB (see shellcwd_windows.go) — preferring
//     the most recently started DESCENDANT of the shell, which loosely mirrors
//     the Unix "foreground process group" behavior so the Dir column tracks
//     an editor or tool launched from the shell, not just the shell itself.
//
// Best-effort: any error yields "" and the caller keeps the previous value.
func shellCwd(pid int) string {
	if pid <= 0 {
		return ""
	}
	switch runtime.GOOS {
	case "linux":
		if p, err := os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid)); err == nil {
			return p
		}
	case "windows":
		return shellCwdWindows(pid)
	case "darwin":
		// The proc_info syscall answers directly — no subprocess, ~50µs against
		// lsof's ~350ms, and nothing that can hang or outlive us. lsof stays as
		// the fallback for the case where the syscall says nothing.
		if p := procCwdDarwin(pid); p != "" {
			return p
		}
		// `lsof -a -d cwd -p PID -Fn` prints field lines; the cwd path is the
		// one prefixed with "n", e.g.:  p<pid>\nfcwd\nn/Users/me/project
		return lsofCwd(pid)
	}
	return ""
}

// lsofCwd is the fallback for when the syscall says nothing. Kept separate so
// its timeout stays under test: the syscall now answers first in practice, so a
// test driving shellCwd would never reach this.
func lsofCwd(pid int) string {
	ctx, cancel := context.WithTimeout(context.Background(), shellCwdTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "lsof", "-a", "-d", "cwd", "-p", strconv.Itoa(pid), "-Fn").Output()
	if err != nil {
		return "" // includes the timeout: caller keeps the previous value
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "n") {
			return strings.TrimPrefix(line, "n")
		}
	}
	return ""
}
