// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package relay

import "time"

const (
	MaxAuthAttempts = 5
	AuthLockout     = 5 * time.Minute
)

type authState struct {
	pinHash        string
	agentAuthed    bool
	viewerAuthed   bool
	failedAttempts int
	lockedUntil    time.Time
}

func (a *authState) isLocked() bool {
	return !a.lockedUntil.IsZero() && time.Now().Before(a.lockedUntil)
}

func (a *authState) recordFailure() {
	a.failedAttempts++
	if a.failedAttempts >= MaxAuthAttempts {
		a.lockedUntil = time.Now().Add(AuthLockout)
		a.failedAttempts = 0
	}
}

func (a *authState) resetFailures() {
	a.failedAttempts = 0
	a.lockedUntil = time.Time{}
}
