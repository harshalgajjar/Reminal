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
// if it was made before the child: by the order Windows made them in where
// it says (procOrder), which no change of the clock touches, and by their
// start times where it does not. One that cannot be told is not taken.
func parentPID(pid int) int {
	p := snapshotParent(pid)
	if p <= 0 {
		return 0
	}
	pn, pnok := procOrder(p)
	cn, cnok := procOrder(pid)
	ps, pok := procStartTime(p)
	cs, cok := procStartTime(pid)
	if !parentCameFirst(pn, pnok, cn, cnok, ps, pok, cs, cok) {
		return 0
	}
	return p
}

// processSequenceNumber is the information class of a process's number in
// the order Windows made its processes since it started (Windows 10 1703 and
// later).
const processSequenceNumber = 92

// procOrder is that number for pid: larger for a process made later, whatever
// the clock said at the time. False where it cannot be read (an older
// Windows, a process this user may not ask about, one that is gone).
func procOrder(pid int) (uint64, bool) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return 0, false
	}
	defer windows.CloseHandle(h)
	var n uint64
	var got uint32
	if err := windows.NtQueryInformationProcess(h, processSequenceNumber, unsafe.Pointer(&n), uint32(unsafe.Sizeof(n)), &got); err != nil || got != uint32(unsafe.Sizeof(n)) || n == 0 {
		return 0, false
	}
	return n, true
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
