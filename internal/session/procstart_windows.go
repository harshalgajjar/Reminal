// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build windows

package session

import (
	"time"

	"golang.org/x/sys/windows"
)

// procStartTime returns when the process now at pid was started, per the kernel.
// Used to detect PID reuse: if this is meaningfully later than the session's
// recorded StartedAt, the OS handed the PID to a different, newer process and
// our session is actually dead. Windows reads it off the process handle:
// OpenProcess with the narrowest right that GetProcessTimes accepts, then take
// the creation FILETIME. Best-effort — (zero, false) on any error, which the
// caller treats as "can't tell, assume not reused" so a live session is never
// pruned on missing evidence.
func procStartTime(pid int) (time.Time, bool) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return time.Time{}, false
	}
	defer windows.CloseHandle(h)
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &creation, &exit, &kernel, &user); err != nil {
		return time.Time{}, false
	}
	// Filetime.Nanoseconds already rebases from the Windows epoch (1601) to Unix.
	return time.Unix(0, creation.Nanoseconds()), true
}
