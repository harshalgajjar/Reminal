// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !windows

package main

import (
	"os"
	"syscall"
	"time"
)

// mcpPollable returns the file re-opened so that reads on it take a deadline.
// The stdin a client hands us is a blocking pipe, which Go does not poll;
// put in non-blocking mode and re-wrapped, it is. Returns the file itself when
// that cannot be done — reads then simply block, as they always did.
func mcpPollable(f *os.File) *os.File {
	fd := int(f.Fd())
	if err := syscall.SetNonblock(fd, true); err != nil {
		return f
	}
	nf := os.NewFile(uintptr(fd), f.Name())
	if nf == nil {
		_ = syscall.SetNonblock(fd, false)
		return f
	}
	if err := nf.SetReadDeadline(time.Time{}); err != nil {
		_ = syscall.SetNonblock(fd, false)
		return f
	}
	return nf
}
