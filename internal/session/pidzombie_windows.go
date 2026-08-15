// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build windows

package session

// pidIsZombie is always false on Windows: there is no zombie state — an exited
// process just leaves a handle-refcounted object, which the liveness check
// (GetExitCodeProcess != STILL_ACTIVE) already sees as dead.
func pidIsZombie(pid int) bool { return false }
