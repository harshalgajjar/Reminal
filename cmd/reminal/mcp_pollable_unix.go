// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !windows

package main

import (
	"os"
	"syscall"
	"time"
)

// mcpPollable returns the file so that reads on it take a deadline. The stdin
// a client hands us is a blocking pipe, which Go does not poll; put in
// non-blocking mode and re-wrapped, it is. A file Go already polls (a pipe it
// made itself, say) is returned as it is: registering the same descriptor a
// second time fails on Linux, and the copy would then block forever. Returns
// the file itself when nothing can be done — reads then simply block, as they
// always did.
func mcpPollable(f *os.File) *os.File {
	if f.SetReadDeadline(time.Time{}) == nil {
		return f // already pollable
	}
	fd := int(f.Fd())
	if err := syscall.SetNonblock(fd, true); err != nil {
		return f
	}
	nf := os.NewFile(uintptr(fd), f.Name())
	if nf == nil || nf.SetReadDeadline(time.Time{}) != nil {
		_ = syscall.SetNonblock(fd, false)
		return f
	}
	return nf
}
