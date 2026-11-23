// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// browserDetachedProcess is CreateProcess's DETACHED_PROCESS: the browser
// gets no console of this terminal's.
const browserDetachedProcess = 0x00000008

// browserDetach starts the browser apart from this console: it is the
// person's browser from then on, and must not end with this terminal or
// take a Ctrl-C meant for reminal.
func browserDetach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | browserDetachedProcess}
}
