// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

// Win32 SYSTEM_POWER_STATUS values, from winbase.h.
const (
	batteryFlagNoBattery = 128 // BATTERY_FLAG_NO_BATTERY
	batteryFlagCharging  = 8   // BATTERY_FLAG_CHARGING
	acLineOnline         = 1   // AC_LINE_ONLINE
	powerUnknown         = 255 // BATTERY_PERCENTAGE_UNKNOWN / BATTERY_FLAG_UNKNOWN
	// BATTERY_LIFE_UNKNOWN: the OS declines to estimate. It is also what
	// Windows reports for the whole time a machine is on mains.
	lifetimeUnknown = 0xFFFFFFFF
)

// decodePowerStatus turns a SYSTEM_POWER_STATUS into a Battery. Untagged so it
// is compiled and tested on every platform: the syscall is one line and cannot
// be exercised off-Windows, but the flag arithmetic below is where the bugs
// would actually live.
func decodePowerStatus(acLine, flag, pct byte, lifetime uint32) *Battery {
	// A desktop reports NO_BATTERY; some VMs report an unknown percentage
	// instead, and Hyper-V reports both. All mean "nothing worth showing".
	if flag&batteryFlagNoBattery != 0 || pct == powerUnknown || flag == powerUnknown {
		return nil
	}
	if pct > 100 {
		return nil
	}
	p := int(pct)
	b := &Battery{Pct: &p}
	switch {
	case flag&batteryFlagCharging != 0:
		b.State = "charging"
	case acLine == acLineOnline:
		b.State = "charged" // on mains and not taking charge
	default:
		b.State = "discharging"
	}
	// BatteryLifeTime is seconds to empty and is only populated while
	// discharging. Windows has no time-to-full counterpart, so a charging
	// machine reports no estimate rather than a fabricated one.
	if b.State == "discharging" && lifetime != lifetimeUnknown {
		b.Mins = int(lifetime / 60)
	}
	return b
}
