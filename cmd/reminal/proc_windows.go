// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build windows

package main

import "errors"

// pauseAgent asks the agent with the given pid to stop broadcasting but keep
// its shell alive (`reminal stop`). No SIGUSR1 on Windows — the request rides
// the agent's control socket ("pause" verb).
func pauseAgent(pid int) error {
	_, err := sendControl(pid, "pause")
	return err
}

// execReplace has no Windows equivalent (no exec()). Only reachable from the
// darwin-only bundle self-heal, so it never actually runs here.
func execReplace(bin string) error {
	return errors.New("in-place exec isn't supported on Windows")
}
