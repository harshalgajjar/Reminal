// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !linux && !darwin && !windows

package session

// parentPID is unsupported here; callers fall back to the environment.
func parentPID(pid int) int { return 0 }
