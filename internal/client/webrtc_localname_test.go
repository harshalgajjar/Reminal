// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"context"
	"errors"
	"net"
	"testing"
)

// A browser gives its address on the local network only as a ".local" name.
// Where peer connections do no name lookup of their own (Windows: it brought
// up the firewall's question), the name is asked of the system and the
// address put in its place, so a viewer on the same network still connects
// directly. A name nobody answers for is left out; any other candidate is
// passed as it came.
func TestAViewersLocalNameIsResolvedThroughTheSystem(t *testing.T) {
	old := lookupLocalName
	t.Cleanup(func() { lookupLocalName = old })
	asked := ""
	lookupLocalName = func(_ context.Context, name string) ([]net.IPAddr, error) {
		asked = name
		if name == "gone.local" {
			return nil, errors.New("no such host")
		}
		return []net.IPAddr{{IP: net.ParseIP("fe80::1")}, {IP: net.ParseIP("192.168.1.23")}}, nil
	}
	const named = "candidate:842163049 1 udp 2113937151 0f3c9d3e-1a2b-4c5d-8e9f-001122334455.local 54321 typ host generation 0 ufrag abcd network-cost 999"
	got, ok := resolveLocalCandidate(context.Background(), named)
	want := "candidate:842163049 1 udp 2113937151 192.168.1.23 54321 typ host generation 0 ufrag abcd network-cost 999"
	if !ok || got != want || asked != "0f3c9d3e-1a2b-4c5d-8e9f-001122334455.local" {
		t.Errorf("a .local candidate: %q (resolved %v, asked for %q), want %q", got, ok, asked, want)
	}
	if got, ok := resolveLocalCandidate(context.Background(), "candidate:1 1 udp 2113937151 gone.local 5 typ host"); ok {
		t.Errorf("a name nobody answers for was resolved: %q", got)
	}
	for _, plain := range []string{
		"candidate:1 1 udp 2113937151 192.168.1.5 54321 typ host",
		"candidate:2 1 udp 1677729535 203.0.113.9 61000 typ srflx raddr 0.0.0.0 rport 0",
		"candidate:3 1 udp 2113937151 printer.localdomain 54321 typ host",
		"",
	} {
		if n := localNameOf(plain); n != "" {
			t.Errorf("candidate %q: taken for a .local name (%q)", plain, n)
		}
	}
	if localNameOf("a=candidate:1 1 udp 2113937151 ABC.LOCAL 5 typ host") != "ABC.LOCAL" {
		t.Error("a .local name in an answer's own line, in capitals, was not seen")
	}
}
