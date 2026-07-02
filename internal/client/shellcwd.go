// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// shellCwd returns the live working directory of the shell process with the
// given pid, or "" if it can't be determined. This lets `reminal list`'s Dir
// column follow the shell as it cd's, instead of being frozen at the directory
// the session was launched from.
//
//   - Linux: read /proc/<pid>/cwd — cheap, no subprocess.
//   - macOS: shell out to lsof — there's no /proc, and proc_pidinfo needs
//     cgo/libproc which the static (CGO_ENABLED=0) build doesn't have.
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
	case "darwin":
		// `lsof -a -d cwd -p PID -Fn` prints field lines; the cwd path is the
		// one prefixed with "n", e.g.:  p<pid>\nfcwd\nn/Users/me/project
		out, err := exec.Command("lsof", "-a", "-d", "cwd", "-p", strconv.Itoa(pid), "-Fn").Output()
		if err != nil {
			return ""
		}
		for _, line := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(line, "n") {
				return strings.TrimPrefix(line, "n")
			}
		}
	}
	return ""
}
