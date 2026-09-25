// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !windows

package main

import (
	"os"
	"syscall"
	"time"

	"golang.org/x/term"
)

// mcpPollable returns a file whose reads take a deadline. The stdin a client
// hands us is a blocking pipe, which Go does not poll; put in non-blocking
// mode and wrapped afresh, it is. A file Go already polls (a pipe it made
// itself, say) is returned as it is: registering the same descriptor a second
// time fails on Linux, and the copy would then block forever. Returns the
// file itself when nothing can be done — reads then simply block, as they
// always did.
//
// The wrapper gets a descriptor of its own (dup'd, close-on-exec): a second
// *os.File over the same number would close it from its finaliser, and a
// terminal is left alone — the flag would land on the terminal itself,
// shared with the shell that ran us.
func mcpPollable(f *os.File) *os.File {
	if f.SetReadDeadline(time.Time{}) == nil {
		return f // already pollable
	}
	fd := int(f.Fd())
	if term.IsTerminal(fd) {
		return f
	}
	dup, err := syscall.Dup(fd)
	if err != nil {
		return f
	}
	syscall.CloseOnExec(dup)
	if err := syscall.SetNonblock(dup, true); err != nil {
		_ = syscall.Close(dup)
		return f
	}
	nf := os.NewFile(uintptr(dup), f.Name())
	if nf == nil || nf.SetReadDeadline(time.Time{}) != nil {
		if nf != nil {
			_ = nf.Close()
		} else {
			_ = syscall.Close(dup)
		}
		_ = syscall.SetNonblock(fd, false) // the flag is on the shared description
		return f
	}
	return nf
}
