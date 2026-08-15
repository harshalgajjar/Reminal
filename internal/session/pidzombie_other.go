// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !darwin && !linux && !windows

package session

// pidIsZombie is a no-op on platforms without a cheap zombie check (e.g.
// Windows): pidAlive falls back to the signal-0 liveness test alone.
func pidIsZombie(pid int) bool { return false }
