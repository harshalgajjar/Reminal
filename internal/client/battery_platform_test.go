// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"os"
	"path/filepath"
	"testing"
)

// writeSupply builds one /sys/class/power_supply/<name> entry.
func writeSupply(t *testing.T, root, name string, files map[string]string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for k, v := range files {
		if err := os.WriteFile(filepath.Join(dir, k), []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// The fixtures below are the shapes real kernels produce: a ThinkPad exposing
// energy_*/power_now, a phone-style driver exposing charge_*/current_now, an
// AC adapter entry sitting in the same directory, and a server with neither.
func TestReadBatterySysfs(t *testing.T) {
	t.Run("discharging with energy_* driver", func(t *testing.T) {
		root := t.TempDir()
		writeSupply(t, root, "AC", map[string]string{"type": "Mains\n", "online": "0\n"})
		writeSupply(t, root, "BAT0", map[string]string{
			"type": "Battery\n", "capacity": "62\n", "status": "Discharging\n",
			"energy_now": "33000000\n", "energy_full": "53000000\n", "power_now": "14000000\n",
		})
		b := readBatterySysfs(root)
		if b == nil || b.Pct == nil {
			t.Fatal("no battery found")
		}
		if *b.Pct != 62 || b.State != "discharging" {
			t.Fatalf("got %d%% %q", *b.Pct, b.State)
		}
		// 33.0/14.0 h = 2h21m. Discharging counts down to EMPTY, so the whole
		// remaining charge is the numerator.
		if b.Mins != 141 {
			t.Errorf("Mins = %d, want 141", b.Mins)
		}
	})

	t.Run("charging counts up to full, not down to empty", func(t *testing.T) {
		root := t.TempDir()
		writeSupply(t, root, "BAT0", map[string]string{
			"type": "Battery\n", "capacity": "40\n", "status": "Charging\n",
			"charge_now": "2000000\n", "charge_full": "5000000\n", "current_now": "1500000\n",
		})
		b := readBatterySysfs(root)
		if b == nil || b.State != "charging" {
			t.Fatalf("got %+v", b)
		}
		// (5.0-2.0)/1.5 h = 2h. If this ever returned the discharging figure
		// (2.0/1.5 = 80m) the UI would say "1h 20m to full" while charging.
		if b.Mins != 120 {
			t.Errorf("Mins = %d, want 120 (time to FULL)", b.Mins)
		}
	})

	t.Run("full and not-charging both read as charged with no countdown", func(t *testing.T) {
		for _, status := range []string{"Full", "Not charging"} {
			root := t.TempDir()
			writeSupply(t, root, "BAT0", map[string]string{
				"type": "Battery\n", "capacity": "100\n", "status": status + "\n",
				"energy_now": "53000000\n", "energy_full": "53000000\n", "power_now": "0\n",
			})
			b := readBatterySysfs(root)
			if b == nil || b.State != "charged" {
				t.Fatalf("%q -> %+v", status, b)
			}
			if b.Mins != 0 {
				t.Errorf("%q gave a %d-minute estimate; a battery that is not moving has no countdown", status, b.Mins)
			}
		}
	})

	t.Run("an AC adapter alone is not a battery", func(t *testing.T) {
		root := t.TempDir()
		// The trap: a Mains entry has no capacity, and treating the directory
		// as a battery would report a desktop at 0%.
		writeSupply(t, root, "AC", map[string]string{"type": "Mains\n", "online": "1\n"})
		if b := readBatterySysfs(root); b != nil {
			t.Errorf("mains-only host reported %+v, want nil", b)
		}
	})

	t.Run("server with no power supplies at all", func(t *testing.T) {
		if b := readBatterySysfs(t.TempDir()); b != nil {
			t.Errorf("empty tree reported %+v, want nil", b)
		}
		if b := readBatterySysfs("/definitely/not/here"); b != nil {
			t.Errorf("missing tree reported %+v, want nil", b)
		}
	})

	t.Run("zero rate yields no estimate rather than infinity", func(t *testing.T) {
		root := t.TempDir()
		writeSupply(t, root, "BAT0", map[string]string{
			"type": "Battery\n", "capacity": "55\n", "status": "Discharging\n",
			"energy_now": "30000000\n", "energy_full": "53000000\n", "power_now": "0\n",
		})
		b := readBatterySysfs(root)
		if b == nil || b.Mins != 0 {
			t.Errorf("got %+v, want a reading with no estimate", b)
		}
	})

	t.Run("garbage capacity is ignored, not rendered", func(t *testing.T) {
		root := t.TempDir()
		writeSupply(t, root, "BAT0", map[string]string{"type": "Battery\n", "capacity": "not-a-number\n", "status": "Discharging\n"})
		if b := readBatterySysfs(root); b != nil {
			t.Errorf("unparseable capacity produced %+v, want nil", b)
		}
	})
}

func TestDecodePowerStatus(t *testing.T) {
	const (
		acOffline = 0
		acOnline  = 1
		noFlags   = 0
	)
	cases := []struct {
		name          string
		ac, flag, pct byte
		life          uint32
		wantNil       bool
		wantPct       int
		wantState     string
		wantMins      int
	}{
		{name: "desktop reports NO_BATTERY", ac: acOnline, flag: batteryFlagNoBattery, pct: powerUnknown, life: lifetimeUnknown, wantNil: true},
		{name: "VM reports an unknown percentage", ac: acOnline, flag: noFlags, pct: powerUnknown, life: lifetimeUnknown, wantNil: true},
		{name: "Hyper-V reports unknown flags too", ac: acOnline, flag: powerUnknown, pct: powerUnknown, life: lifetimeUnknown, wantNil: true},
		{name: "on battery with an estimate", ac: acOffline, flag: noFlags, pct: 62, life: 8460, wantPct: 62, wantState: "discharging", wantMins: 141},
		{name: "charging", ac: acOnline, flag: batteryFlagCharging, pct: 41, life: lifetimeUnknown, wantPct: 41, wantState: "charging"},
		{name: "on mains and full", ac: acOnline, flag: noFlags, pct: 100, life: lifetimeUnknown, wantPct: 100, wantState: "charged"},
		{name: "flat battery is a reading, not an absence", ac: acOffline, flag: noFlags, pct: 0, life: 60, wantPct: 0, wantState: "discharging", wantMins: 1},
		{name: "nonsense percentage rejected", ac: acOffline, flag: noFlags, pct: 200, life: lifetimeUnknown, wantNil: true},
		// Windows has no time-to-full, so a charging machine must not inherit
		// a stale time-to-empty and claim it as "to full".
		{name: "charging never borrows a to-empty estimate", ac: acOnline, flag: batteryFlagCharging, pct: 50, life: 3600, wantPct: 50, wantState: "charging", wantMins: 0},
	}
	for _, c := range cases {
		got := decodePowerStatus(c.ac, c.flag, c.pct, c.life)
		if c.wantNil {
			if got != nil {
				t.Errorf("%s: got %+v, want nil", c.name, got)
			}
			continue
		}
		if got == nil || got.Pct == nil {
			t.Errorf("%s: got nil, want a reading", c.name)
			continue
		}
		if *got.Pct != c.wantPct || got.State != c.wantState || got.Mins != c.wantMins {
			t.Errorf("%s: got %d%% %q %dm, want %d%% %q %dm",
				c.name, *got.Pct, got.State, got.Mins, c.wantPct, c.wantState, c.wantMins)
		}
	}
}
