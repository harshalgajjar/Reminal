// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build darwin

package session

import (
	"time"

	"golang.org/x/sys/unix"
)

// bootTime is when this machine last started, per the kernel (kern.boottime).
func bootTime() (time.Time, bool) {
	tv, err := unix.SysctlTimeval("kern.boottime")
	if err != nil || tv.Sec <= 0 {
		return time.Time{}, false
	}
	return time.Unix(tv.Sec, int64(tv.Usec)*1000), true
}
