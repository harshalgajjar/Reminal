// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/reminal/reminal/internal/protocol"
)

// Battery rides on the EXISTING TypeDirResp payload as new optional fields —
// no new message type, so no relay forwarding change is needed. That only
// stays true if the wire format degrades cleanly in both directions, which is
// what these tests pin.

// Old host → new viewer. A pre-battery agent sends a DirResponse with no
// battery keys at all; it must parse and show nothing rather than 0%.
func TestOldHostReplyParsesWithNoBattery(t *testing.T) {
	isolateHome(t)
	const oldWire = `{"hostname":"harshals-pc","sessions":[{"id":"NFWW9EDD","name":"driver","idle_secs":4}]}`
	var resp protocol.DirResponse
	if err := json.Unmarshal([]byte(oldWire), &resp); err != nil {
		t.Fatalf("a pre-battery reply no longer parses: %v", err)
	}
	if resp.Hostname != "harshals-pc" || len(resp.Sessions) != 1 {
		t.Fatalf("the rest of the reply was damaged: %+v", resp)
	}
	if resp.BatteryPct != nil {
		t.Errorf("BatteryPct = %v, want nil for a host that never sent one", resp.BatteryPct)
	}
	if got := ObserveBattery("mach_old", resp); got != nil {
		t.Errorf("an old host produced a battery reading %+v — it would render a charge it never reported", got)
	}
}

// New host → old viewer. A machine with no battery must serialise to exactly
// the bytes it always did, so an old viewer sees a payload it has always been
// able to read and no battery UI appears anywhere.
func TestNoBatteryOmitsEveryFieldFromTheWire(t *testing.T) {
	resp := protocol.DirResponse{Hostname: "aws-eu-1", Sessions: []protocol.DirSession{{ID: "X"}}}
	fillBattery(&resp) // a machine with no battery leaves it untouched
	if resp.BatteryPct != nil {
		t.Skip("this host has a battery; the omission path is covered by the explicit case below")
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"battery_pct", "battery_state", "battery_mins"} {
		if strings.Contains(string(raw), key) {
			t.Errorf("a batteryless machine still put %q on the wire: %s", key, raw)
		}
	}
}

// The same guarantee, stated without depending on what the test host is.
func TestBatteryFieldsAreOmitEmpty(t *testing.T) {
	raw, err := json.Marshal(protocol.DirResponse{Hostname: "tower"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "battery") {
		t.Errorf("zero-value DirResponse serialises battery keys, so every desktop would "+
			"start sending them to old viewers: %s", raw)
	}
	// And a real reading must actually appear, or the feature is invisible.
	pct := 0
	raw2, err := json.Marshal(protocol.DirResponse{BatteryPct: &pct, BatteryState: "discharging"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw2), `"battery_pct":0`) {
		t.Errorf("0%% did not survive serialisation — the machine about to die would report nothing: %s", raw2)
	}
}

// A new host's reply must remain readable by a decoder that predates the
// fields, which is what an old viewer effectively is.
func TestNewReplyDecodesIntoOldShape(t *testing.T) {
	pct := 62
	raw, err := json.Marshal(protocol.DirResponse{
		Hostname: "studio-mac", Sessions: []protocol.DirSession{{ID: "Q49ULHPV", Name: "work"}},
		BatteryPct: &pct, BatteryState: "discharging", BatteryMins: 141,
	})
	if err != nil {
		t.Fatal(err)
	}
	// The shape an old build compiled against: no battery fields at all.
	var old struct {
		Hostname string `json:"hostname"`
		Sessions []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(raw, &old); err != nil {
		t.Fatalf("an old viewer could not decode a new reply: %v", err)
	}
	if old.Hostname != "studio-mac" || len(old.Sessions) != 1 || old.Sessions[0].Name != "work" {
		t.Errorf("an old viewer would mis-read the reply: %+v", old)
	}
}
