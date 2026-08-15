// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !windows

package main

import (
	"os"
	"syscall"
)

// pauseAgent asks the agent with the given pid to stop broadcasting but keep
// its shell alive (`reminal stop`). On Unix that's SIGUSR1; Windows sends
// "pause" over the agent's control socket instead.
func pauseAgent(pid int) error {
	return syscall.Kill(pid, syscall.SIGUSR1)
}

// execReplace swaps this process image for bin (same argv/env) — used after a
// self-heal reinstall so the user's command continues under the new binary.
// Never returns on success.
func execReplace(bin string) error {
	return syscall.Exec(bin, os.Args, os.Environ())
}
