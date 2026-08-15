// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !darwin && !linux && !windows

package session

import "time"

// procStartTime is unavailable on platforms without a cheap way to read it.
// Returning (zero, false) makes pidReused conservative: with no start time to
// compare, a session is never treated as reused, so behaviour falls back to the
// plain PID-liveness check.
func procStartTime(pid int) (time.Time, bool) { return time.Time{}, false }
