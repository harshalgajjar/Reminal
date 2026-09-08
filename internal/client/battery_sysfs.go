// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// sysfsPowerRoot is where Linux exposes power supplies. A parameter rather
// than a constant inside the reader so the parsing can be exercised against
// fixture trees on any host — the alternative is Linux logic that only ever
// runs, untested, on machines the author does not have.
const sysfsPowerRoot = "/sys/class/power_supply"

// readBatterySysfs reads a Linux power-supply tree. Untagged on purpose: the
// logic is pure file parsing, so it is compiled and tested everywhere even
// though only battery_linux.go calls it for real.
func readBatterySysfs(root string) *Battery {
	dirs, err := filepath.Glob(filepath.Join(root, "*"))
	if err != nil {
		return nil
	}
	for _, d := range dirs {
		// "Battery" excludes the AC adapter entries, which live in the same
		// directory and would otherwise parse as a 0% battery.
		if strings.TrimSpace(sysfsStr(d, "type")) != "Battery" {
			continue
		}
		capStr := strings.TrimSpace(sysfsStr(d, "capacity"))
		if capStr == "" {
			continue
		}
		pct, err := strconv.Atoi(capStr)
		if err != nil || pct < 0 || pct > 100 {
			continue
		}
		b := &Battery{Pct: &pct}
		switch strings.TrimSpace(sysfsStr(d, "status")) {
		case "Charging":
			b.State = "charging"
		case "Full":
			b.State = "charged"
		case "Not charging":
			// Plugged in and deliberately holding — a charge-limit policy, or
			// a dock that has stopped topping up. Not draining, so it reads as
			// charged rather than as a countdown to empty.
			b.State = "charged"
		default:
			b.State = "discharging"
		}
		b.Mins = sysfsMinutes(d, b.State)
		return b
	}
	return nil
}

// sysfsMinutes derives a time estimate the way upower does: charge (or energy)
// remaining divided by the current rate. Kernels expose one pair or the other
// depending on the driver, and either may be missing or zero — a zero rate is
// an idle battery, not an infinite runtime, so it yields "no estimate".
func sysfsMinutes(dir, state string) int {
	if state == "charged" {
		return 0
	}
	now, rate := sysfsNum(dir, "energy_now"), sysfsNum(dir, "power_now")
	full := sysfsNum(dir, "energy_full")
	if now == 0 || rate == 0 {
		now, rate = sysfsNum(dir, "charge_now"), sysfsNum(dir, "current_now")
		full = sysfsNum(dir, "charge_full")
	}
	if now <= 0 || rate <= 0 {
		return 0
	}
	remaining := now // discharging: what is left
	if state == "charging" {
		if full <= 0 || full <= now {
			return 0
		}
		remaining = full - now // charging: what is still to go in
	}
	mins := int(float64(remaining) / float64(rate) * 60)
	if mins < 0 || mins > 60*72 { // a >3-day estimate is a driver glitch, not a fact
		return 0
	}
	return mins
}

func sysfsStr(dir, name string) string {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return string(b)
}

func sysfsNum(dir, name string) int64 {
	v, err := strconv.ParseInt(strings.TrimSpace(sysfsStr(dir, name)), 10, 64)
	if err != nil {
		return 0
	}
	return v
}
