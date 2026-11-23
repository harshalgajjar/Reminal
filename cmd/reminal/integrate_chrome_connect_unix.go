// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// browserDetach starts the browser in a session of its own: it is the
// person's browser from then on, and must not end with this terminal.
func browserDetach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
