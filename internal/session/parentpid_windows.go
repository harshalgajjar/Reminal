// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build windows

package session

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// parentPID returns the parent of pid, or 0. Windows keeps a parent's id in
// its child after the parent has gone, and hands ids out again quickly, so
// the id alone may name an unrelated, later process: it is the parent only
// if it was created no later than the child. One that cannot be told is not
// taken.
func parentPID(pid int) int {
	p := snapshotParent(pid)
	if p <= 0 {
		return 0
	}
	ps, pok := procStartTime(p)
	cs, cok := procStartTime(pid)
	if !parentIsOlder(ps, pok, cs, cok) {
		return 0
	}
	return p
}

// snapshotParent is the parent id Windows has for pid in the process
// snapshot, or 0.
func snapshotParent(pid int) int {
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
