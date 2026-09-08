// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build windows

package client

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// kernel32's GetSystemPowerStatus. x/sys/windows doesn't wrap it, so bind the
// proc lazily — the same shape keepawake uses for SetThreadExecutionState.
var procGetSystemPowerStatus = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetSystemPowerStatus")

// SYSTEM_POWER_STATUS from winbase.h. Field order and widths are load-bearing:
// this is written into by the kernel.
type systemPowerStatus struct {
	ACLineStatus        byte
	BatteryFlag         byte
	BatteryLifePercent  byte
	SystemStatusFlag    byte
	BatteryLifeTime     uint32
	BatteryFullLifeTime uint32
}

func readBattery() *Battery {
	var s systemPowerStatus
	r, _, _ := procGetSystemPowerStatus.Call(uintptr(unsafe.Pointer(&s)))
	if r == 0 {
		return nil
	}
	return decodePowerStatus(s.ACLineStatus, s.BatteryFlag, s.BatteryLifePercent, s.BatteryLifeTime)
}
