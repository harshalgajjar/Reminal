// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build windows

package client

import "errors"

// Hot restart replaces the process image in place via syscall.Exec, which has
// no Windows equivalent (no exec(), no fd inheritance across it, and ConPTY
// handles can't be re-adopted by a fresh image). `reminal restart` therefore
// reports unsupported here; the upgrade path still works — new sessions pick up
// the new binary, and the daemon respawns itself onto it (see watchBinaryAndExit).

// LoadResumeState never resumes on Windows: no process here can have been
// hot-restarted, so the fresh-startup path is always taken.
func LoadResumeState() (*ResumeState, error) { return nil, nil }

func (a *Agent) executeRestart() error {
	return errors.New("hot restart isn't supported on Windows — exit the session and start a new one to pick up the upgraded binary")
}
