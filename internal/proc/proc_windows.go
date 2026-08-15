// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build windows

package proc

import (
	"errors"

	"golang.org/x/sys/windows"
)

// ErrGone reports that the target process doesn't exist. Callers treat it as
// "already dead — success" when tearing a process down.
var ErrGone = errors.New("process does not exist")

// stillActive is the GetExitCodeProcess sentinel for a running process.
const stillActive = 259

// Alive reports whether pid currently exists. Opens the process with the
// narrowest right that answers the question; an open failure (invalid
// parameter = never existed / already reaped) means dead, and access-denied
// (someone else's process) counts as alive, matching the Unix EPERM contract.
func Alive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return errors.Is(err, windows.ERROR_ACCESS_DENIED)
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return true // opened it but can't read the code — assume alive
	}
	return code == stillActive
}

// Terminate ends pid. Windows has no cross-process SIGTERM for console
// programs, so this is a hard TerminateProcess — the target's defers do NOT
// run. Session/tunnel records are liveness-checked on read, so a stale record
// left behind is reaped on the next `reminal list`/`prune`.
func Terminate(pid int) error { return Kill(pid) }

// Kill force-terminates pid. Returns ErrGone when the process was already dead.
func Kill(pid int) error {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return ErrGone
		}
		return err
	}
	defer windows.CloseHandle(h)
	var code uint32
	if gerr := windows.GetExitCodeProcess(h, &code); gerr == nil && code != stillActive {
		return ErrGone // object lingers only because a handle is held; process itself is gone
	}
	return windows.TerminateProcess(h, 1)
}
