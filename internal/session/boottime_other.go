// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !darwin && !linux && !windows

package session

import "time"

// bootTime is not known here: every record is judged by what it says.
func bootTime() (time.Time, bool) { return time.Time{}, false }
