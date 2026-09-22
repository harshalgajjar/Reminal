// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build windows

package session

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// parentPID returns the parent of pid by scanning the process snapshot, or 0.
func parentPID(pid int) int {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0
	}
	defer windows.CloseHandle(snap)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	if windows.Process32First(snap, &e) != nil {
		return 0
	}
	for {
		if int(e.ProcessID) == pid {
			return int(e.ParentProcessID)
		}
		if windows.Process32Next(snap, &e) != nil {
			return 0
		}
	}
}
