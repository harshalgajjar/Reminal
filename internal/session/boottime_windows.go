// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build windows

package session

import (
	"time"

	"golang.org/x/sys/windows"
)

// bootTime is when this machine last started: now, less how long it has
// been up (GetTickCount64). With Fast Startup a shutdown hibernates the
// kernel and the count goes on, so this can be earlier than the last power
// on — a record from before it then stays Unknown, as without a boot time.
func bootTime() (time.Time, bool) {
	r, _, _ := procGetTickCount64.Call() // 64-bit targets only: the whole count fits
	up := uint64(r)
	if up == 0 {
		return time.Time{}, false
	}
	return time.Now().Add(-time.Duration(up) * time.Millisecond), true
}

var procGetTickCount64 = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetTickCount64")
