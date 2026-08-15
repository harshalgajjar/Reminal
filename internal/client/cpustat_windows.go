// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build windows

package client

import (
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// GetSystemTimes hands back three aggregate FILETIMEs (100ns ticks since boot,
// summed across all cores): idle, kernel, user. x/sys/windows doesn't wrap it,
// so bind the proc lazily.
var procGetSystemTimes = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetSystemTimes")

// The previous sample, so cpuPercent can report utilization as the busy
// fraction of ticks elapsed since the last call — same shape as the Linux
// /proc/stat differ.
var (
	winCPUMu     sync.Mutex
	winPrevIdle  uint64
	winPrevTotal uint64
	winHavePrev  bool
)

// cpuPercent returns real CPU utilization (0..100) on Windows by diffing
// GetSystemTimes between successive calls. No cgo, no subprocess. Gotcha baked
// into the API: the KERNEL time already INCLUDES idle time (idle runs in the
// kernel's idle threads), so total = kernel + user and busy = total − idle —
// don't add idle in again. The first call has no prior sample to diff against,
// so it seeds the baseline and reports "unknown"; the next poll (~1.5s later)
// yields a real number over that interval.
func cpuPercent() (float64, bool) {
	var idleFT, kernelFT, userFT windows.Filetime
	r, _, _ := procGetSystemTimes.Call(
		uintptr(unsafe.Pointer(&idleFT)),
		uintptr(unsafe.Pointer(&kernelFT)),
		uintptr(unsafe.Pointer(&userFT)),
	)
	if r == 0 {
		return 0, false
	}
	idle := filetimeTicks(idleFT)
	total := filetimeTicks(kernelFT) + filetimeTicks(userFT) // kernel includes idle

	winCPUMu.Lock()
	defer winCPUMu.Unlock()
	if !winHavePrev {
		winPrevIdle, winPrevTotal, winHavePrev = idle, total, true
		return 0, false // no delta yet
	}
	dIdle := idle - winPrevIdle
	dTotal := total - winPrevTotal
	winPrevIdle, winPrevTotal = idle, total
	if dTotal == 0 {
		return 0, false
	}
	busy := float64(dTotal-dIdle) / float64(dTotal) * 100
	if busy < 0 {
		busy = 0
	}
	if busy > 100 {
		busy = 100
	}
	return busy, true
}

// filetimeTicks flattens a FILETIME's split 32-bit halves into one 64-bit
// 100ns-tick counter.
func filetimeTicks(ft windows.Filetime) uint64 {
	return uint64(ft.HighDateTime)<<32 | uint64(ft.LowDateTime)
}
