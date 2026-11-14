// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !windows

package client

import (
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/sys/unix"

	"reminal/internal/pty"
)

// termHost is the program that holds a foreground session's terminal: a
// terminal app, an editor's terminal process, a tmux server, sshd. It is the
// parent of the terminal's session leader, named by pid and start time so a
// reused pid is not taken for it. Zero when it cannot be told.
type termHost struct {
	pid  int
	mark string
}

func findTermHost() termHost {
	sid, err := unix.Getsid(0)
	if err != nil || sid <= 0 {
		return termHost{}
	}
	_, ppid, ok := pty.ProcMark(sid)
	if !ok || ppid <= 1 {
		return termHost{} // the leader's parent gone already: no host to watch
	}
	mark, _, ok := pty.ProcMark(ppid)
	if !ok {
		return termHost{}
	}
	return termHost{pid: ppid, mark: mark}
}

// alive is whether the host is still running. False when it was never known.
func (h termHost) alive() bool {
	if h.pid == 0 {
		return false
	}
	mark, _, ok := pty.ProcMark(h.pid)
	return ok && mark == h.mark
}

// notifyHangup adds SIGHUP, the terminal closing, to a foreground session's
// signals. Left alone when it was started with SIGHUP ignored (nohup): a
// handler would undo that.
func notifyHangup(ch chan os.Signal) {
	if !signal.Ignored(syscall.SIGHUP) {
		signal.Notify(ch, syscall.SIGHUP)
	}
}

func isHangupSignal(sig os.Signal) bool { return sig == syscall.SIGHUP }
