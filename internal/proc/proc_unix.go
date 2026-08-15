// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !windows

// Package proc abstracts the handful of cross-process operations reminal needs
// (liveness, terminate, hard-kill) over the Unix signal model and the Windows
// process-handle model, so callers stay build-tag-free.
package proc

import (
	"errors"
	"syscall"
)

// ErrGone reports that the target process doesn't exist (Unix ESRCH). Callers
// treat it as "already dead — success" when tearing a process down.
var ErrGone = errors.New("process does not exist")

// Alive reports whether pid currently exists. Mirrors the classic
// kill(pid, 0) probe; EPERM (alive but not ours) counts as alive.
func Alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// Terminate asks pid to shut down gracefully (SIGTERM — defers run, records
// are cleaned). Returns ErrGone when the process was already dead.
func Terminate(pid int) error {
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return ErrGone
		}
		return err
	}
	return nil
}

// Kill force-terminates pid (SIGKILL) — the escalation after Terminate is
// ignored. Returns ErrGone when the process was already dead.
func Kill(pid int) error {
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return ErrGone
		}
		return err
	}
	return nil
}
