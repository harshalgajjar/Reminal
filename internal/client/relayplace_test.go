// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"encoding/json"
	"strings"
	"testing"

	"reminal/internal/protocol"
	"reminal/internal/session"
)

// What the relay says of where it serves a session is shown to a person:
// only a location's code is kept, and info says it in words.
func TestWhereTheRelayServesASession(t *testing.T) {
	var msg protocol.Message
	if err := json.Unmarshal([]byte(`{"type":"auth_ok","relay":{"at":"SJC","edge":"BOM"}}`), &msg); err != nil {
		t.Fatal(err)
	}
	if p := msg.Relay.Clean(); p == nil || p.At != "SJC" || p.Edge != "BOM" {
		t.Fatalf("read %+v", p)
	}
	for _, c := range []struct {
		in   *protocol.RelayPlace
		want string
	}{
		{nil, ""},
		{&protocol.RelayPlace{}, ""},
		{&protocol.RelayPlace{At: "sjc", Edge: "\x1b[31m"}, ""},
		{&protocol.RelayPlace{At: "SJC", Edge: "BOMX"}, "SJC/"},
		{&protocol.RelayPlace{At: "S C", Edge: "BOM"}, "/BOM"},
	} {
		got := ""
		if p := c.in.Clean(); p != nil {
			got = p.At + "/" + p.Edge
		}
		if got != c.want {
			t.Errorf("Clean(%+v) = %q, want %q", c.in, got, c.want)
		}
	}
	for _, c := range []struct{ at, edge, want string }{
		{"", "", ""},
		{"SJC", "SJC", "served at SJC, where this machine reaches it"},
		{"SJC", "BOM", "served at SJC; this machine reaches it at BOM (the session was made nearer SJC)"},
		{"SJC", "", "served at SJC"},
		{"", "BOM", "this machine reaches it at BOM"},
	} {
		a := &session.Active{ID: "ABCD1234", RelayAt: c.at, RelayEdge: c.edge}
		if got := relayPlaceLine(a); got != c.want {
			t.Errorf("at %q edge %q: %q, want %q", c.at, c.edge, got, c.want)
		}
		raw, _ := json.Marshal(InfoJSON(a))
		if has := strings.Contains(string(raw), `"relay_at"`); has != (c.at != "") {
			t.Errorf("at %q: info's JSON has relay_at: %v", c.at, has)
		}
	}
}
