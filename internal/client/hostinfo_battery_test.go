// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"encoding/json"
	"testing"
	"time"
)

// setBattery puts a reading in the battery cache, as the machine's reader
// would have left it, and puts the cache back after the test.
func setBattery(t *testing.T, b *Battery) {
	t.Helper()
	batMu.Lock()
	oldVal, oldRead := batVal, batRead
	batVal, batRead = b, time.Now()
	batMu.Unlock()
	t.Cleanup(func() {
		batMu.Lock()
		batVal, batRead = oldVal, oldRead
		batMu.Unlock()
	})
}

// The Host panel's report carries the machine's battery under the names the
// machines list reads, 0% included; a machine with no battery sends none.
func TestHostInfoCarriesTheBattery(t *testing.T) {
	fields := func() map[string]any {
		t.Helper()
		raw, err := json.Marshal(gatherHostInfo())
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	pct := 82
	setBattery(t, &Battery{Pct: &pct, State: "discharging", Mins: 250})
	m := fields()
	if m["battery_pct"] != float64(82) || m["battery_state"] != "discharging" || m["battery_mins"] != float64(250) {
		t.Errorf("host info battery = %v %v %v, want 82 discharging 250", m["battery_pct"], m["battery_state"], m["battery_mins"])
	}

	zero := 0
	setBattery(t, &Battery{Pct: &zero, State: "discharging"})
	if m := fields(); m["battery_pct"] != float64(0) {
		t.Errorf("a flat battery: battery_pct = %v, want 0 (not left out)", m["battery_pct"])
	}

	setBattery(t, nil)
	m = fields()
	for _, k := range []string{"battery_pct", "battery_state", "battery_mins"} {
		if _, ok := m[k]; ok {
			t.Errorf("a machine with no battery sent %s = %v", k, m[k])
		}
	}
}
