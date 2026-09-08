// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"sync"
	"time"
)

// Battery is one machine's power state at a moment in time.
//
// A machine with no battery — a desktop, a VM, a rack server — reports nothing
// at all rather than zeroes, and every renderer treats absence as "show no
// battery UI for this machine". That makes "has a battery" a property the host
// asserts by answering, instead of something a viewer has to infer from a
// suspicious 0%.
type Battery struct {
	// Pct is 0..100. A pointer because 0% is a real (and alarming) reading
	// that must not be confused with "not reported" — the same distinction
	// HostInfo.CPUPercent draws.
	Pct *int
	// State is "charging", "discharging" or "charged" (plugged in and full).
	// Three states, not a bool: "on AC at 100%" and "on AC climbing through
	// 80%" look different to a human and a bool cannot say which is which.
	State string
	// Mins is the OS's own estimate of minutes remaining — to empty while
	// discharging, to full while charging. 0 means the OS declined to guess,
	// which it does for a minute or two after any power-source change.
	Mins int
}

// batteryTTL caches the reading between reads. Every directory query from an
// owner calls this, and a machine answering several viewers should not shell
// out per query. Well under any interval a human perceives.
const batteryTTL = 10 * time.Second

var (
	batMu   sync.Mutex
	batVal  *Battery
	batRead time.Time
)

// CurrentBattery returns this machine's power state, or nil if it has no
// battery (or the platform has no reader). Cached for batteryTTL.
func CurrentBattery() *Battery {
	batMu.Lock()
	defer batMu.Unlock()
	if !batRead.IsZero() && time.Since(batRead) < batteryTTL {
		return batCopy()
	}
	batVal = readBattery()
	batRead = time.Now()
	return batCopy()
}

// batCopy hands out a copy rather than the cached value itself: the cache is
// shared by every caller for batteryTTL, and one of them mutating the struct
// (or the Pct it points at) would poison the reading for all the others.
func batCopy() *Battery {
	if batVal == nil {
		return nil
	}
	out := *batVal
	if batVal.Pct != nil {
		pct := *batVal.Pct
		out.Pct = &pct
	}
	return &out
}
