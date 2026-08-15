// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build windows

package client

import (
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// x/sys/windows wraps neither of these kernel32 calls, so bind them lazily.
var (
	winKernel32              = windows.NewLazySystemDLL("kernel32.dll")
	procGlobalMemoryStatusEx = winKernel32.NewProc("GlobalMemoryStatusEx")
	procGetTickCount64       = winKernel32.NewProc("GetTickCount64")
)

// memoryStatusEx mirrors MEMORYSTATUSEX (sysinfoapi.h). Length must be set to
// the struct size before the call or it fails with ERROR_INVALID_PARAMETER.
type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

// fillHostInfo reads Windows stats via kernel32 + the registry. Each read is
// independent and best-effort — a failing one just leaves its field zero. Load
// averages don't exist on Windows, so Load1/5/15 stay zero and the viewer omits
// them (the CPU meter comes from cpuPercent instead).
func fillHostInfo(h *HostInfo) {
	var ms memoryStatusEx
	ms.Length = uint32(unsafe.Sizeof(ms))
	if r, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&ms))); r != 0 {
		h.MemTotal = ms.TotalPhys
		if ms.AvailPhys <= ms.TotalPhys {
			h.MemUsed = ms.TotalPhys - ms.AvailPhys
		}
	}

	// Milliseconds since boot, 64-bit so it doesn't wrap at 49 days like the
	// classic GetTickCount. (Windows on Go is 64-bit only, so the uintptr return
	// carries the full value.)
	if ms64, _, _ := procGetTickCount64.Call(); ms64 > 0 {
		h.Uptime = int64(ms64 / 1000)
	}

	h.CPUModel = windowsCPUModel()
}

// windowsCPUModel reads the marketing name of CPU 0 from the registry — the
// same string Task Manager shows. There's no GetSystemInfo equivalent for the
// model name; the HARDWARE hive (populated by the kernel at boot) is the
// canonical no-cgo source.
func windowsCPUModel() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`HARDWARE\DESCRIPTION\System\CentralProcessor\0`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	s, _, err := k.GetStringValue("ProcessorNameString")
	if err != nil {
		return ""
	}
	return s
}
