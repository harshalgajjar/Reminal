// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"testing"
	"time"

	"github.com/reminal/reminal/internal/protocol"
)

func intp(v int) *int { return &v }

func TestObserveBatteryRemembersAcrossOffline(t *testing.T) {
	isolateHome(t) // temp HOME: never touch the real ~/.reminal
	const id = "mach_test"

	// Online and reporting: fresh, and written down.
	got := ObserveBattery(id, protocol.DirResponse{
		BatteryPct: intp(20), BatteryState: "discharging", BatteryMins: 62,
	})
	if got == nil {
		t.Fatal("a reported battery produced no snapshot")
	}
	if got.Stale {
		t.Error("a reading straight off the machine is marked stale")
	}
	if got.Pct != 20 || got.State != "discharging" || got.Mins != 62 {
		t.Errorf("reading mangled: %+v", got)
	}

	// Now the machine goes dark: an empty reply, the shape an offline machine
	// produces. The last reading must survive, marked as history.
	after := ObserveBattery(id, protocol.DirResponse{})
	if after == nil {
		t.Fatal("history was lost the moment the machine went offline — the whole point of the feature")
	}
	if !after.Stale {
		t.Error("a remembered reading is not marked stale, so it would render as current")
	}
	if after.Pct != 20 || after.Mins != 62 {
		t.Errorf("remembered reading changed: %+v", after)
	}
	if after.At.IsZero() {
		t.Error("remembered reading has no timestamp, so it cannot say WHEN it was 20%")
	}
	if time.Since(after.At) > time.Minute {
		t.Errorf("timestamp is not the moment we saw it: %v", after.At)
	}
}

func TestObserveBatteryNeverSeenStaysSilent(t *testing.T) {
	isolateHome(t) // temp HOME: never touch the real ~/.reminal
	// A desktop: online, answering, reporting no battery. It must render
	// nothing at all — not 0%, not "unknown".
	if got := ObserveBattery("mach_desktop", protocol.DirResponse{Hostname: "tower"}); got != nil {
		t.Errorf("a machine with no battery produced %+v, want nil", got)
	}
	// And an offline machine we have never heard a reading from is the same.
	if got := ObserveBattery("mach_never", protocol.DirResponse{}); got != nil {
		t.Errorf("an unseen machine produced %+v, want nil", got)
	}
}

func TestObserveBatteryZeroPercentIsNotAbsence(t *testing.T) {
	isolateHome(t) // temp HOME: never touch the real ~/.reminal
	// 0% is a real and alarming reading. If the wire format collapsed it into
	// "not reported" the machine about to die would be the one showing nothing.
	got := ObserveBattery("mach_flat", protocol.DirResponse{
		BatteryPct: intp(0), BatteryState: "discharging",
	})
	if got == nil {
		t.Fatal("0% was treated as no battery")
	}
	if got.Pct != 0 {
		t.Errorf("Pct = %d, want 0", got.Pct)
	}
}

func TestObserveBatteryUpdatesToNewerReading(t *testing.T) {
	isolateHome(t) // temp HOME: never touch the real ~/.reminal
	const id = "mach_moving"
	ObserveBattery(id, protocol.DirResponse{BatteryPct: intp(80), BatteryState: "discharging"})
	ObserveBattery(id, protocol.DirResponse{BatteryPct: intp(55), BatteryState: "discharging"})
	got := ObserveBattery(id, protocol.DirResponse{}) // offline
	if got == nil || got.Pct != 55 {
		t.Fatalf("history kept the older reading: %+v", got)
	}
}

func TestObserveBatteryForgetsAncientReadings(t *testing.T) {
	isolateHome(t) // temp HOME: never touch the real ~/.reminal
	const id = "mach_drawer"
	ObserveBattery(id, protocol.DirResponse{BatteryPct: intp(20), BatteryState: "discharging"})

	// Age the stored reading past the TTL by rewriting the file directly —
	// the same thing a month in a drawer does.
	h := loadBatteryHistory()
	rec := h.Machines[id]
	rec.At = time.Now().Add(-batteryHistoryTTL - time.Hour)
	h.Machines[id] = rec
	saveBatteryHistory(h)

	if got := ObserveBattery(id, protocol.DirResponse{}); got != nil {
		t.Errorf("a reading older than the TTL was still asserted: %+v", got)
	}
	// Just inside the window is still worth showing.
	rec.At = time.Now().Add(-batteryHistoryTTL + time.Hour)
	h.Machines[id] = rec
	saveBatteryHistory(h)
	if got := ObserveBattery(id, protocol.DirResponse{}); got == nil || !got.Stale {
		t.Errorf("a reading inside the TTL was dropped: %+v", got)
	}
}

func TestCurrentBatteryDoesNotHandOutItsCache(t *testing.T) {
	// The cache is shared by every caller for batteryTTL. If it returned the
	// cached pointer, one caller mutating the struct — or the int Pct points
	// at — would corrupt the reading for all the others until it expired.
	a := CurrentBattery()
	if a == nil {
		t.Skip("this host has no battery")
	}
	if a.Pct != nil {
		*a.Pct = 3
	}
	a.State = "mangled"
	b := CurrentBattery()
	if b == nil {
		t.Fatal("second read returned nothing")
	}
	if b.State == "mangled" || (b.Pct != nil && *b.Pct == 3) {
		t.Errorf("mutating one caller's copy changed the cache: %+v", b)
	}
}
