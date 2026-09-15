// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package relay

import (
	"os"
	"regexp"
	"testing"

	"github.com/reminal/reminal/internal/protocol"
)

// Message types that deliberately do NOT cross the session forward switch, and
// why. Everything else must be forwardable.
//
// The hosted relay forwards opaquely — it never inspects a type, so it cannot
// leave one behind. This one decides per type, which makes forgetting an entry
// a silent, one-sided failure: the feature works for everyone on the hosted
// relay and does nothing here, with no error on either side to suggest why.
// `reminal notify`, `reminal send` and viewer uploads were all in that state.
var exemptFromSessionForwarding = map[protocol.MessageType]string{
	protocol.TypeAuth:         "relay-handled: authenticates the socket",
	protocol.TypeAuthOK:       "relay-generated: reply to auth",
	protocol.TypeRegister:     "relay-handled: agent claims a session",
	protocol.TypeJoin:         "relay-handled: viewer joins a session",
	protocol.TypeConnected:    "relay-generated: viewer count changed",
	protocol.TypeClosed:       "relay-generated: session ended",
	protocol.TypeError:        "relay-generated: refusal or fault",
	protocol.TypePing:         "relay-handled: keepalive, answered in place",
	protocol.TypePong:         "relay-generated: reply to ping",
	protocol.TypeAgentOnline:  "relay-generated: agent presence",
	protocol.TypeAgentOffline: "relay-generated: agent presence",

	// The copy/paste rendezvous is a different endpoint with its own room, so
	// its handshake never reaches this switch.
	protocol.TypeKexConfirm: "rendezvous endpoint, not the session socket",
	protocol.TypeCopyAck:    "rendezvous endpoint, not the session socket",

	// Port forwarding is not implemented on this relay at all — the hosted one
	// carries a tunnel role that has no counterpart here.
	protocol.TypeTunnelRegister: "tunnels unsupported on this relay",
	protocol.TypeTunnelReq:      "tunnels unsupported on this relay",
	protocol.TypeTunnelResp:     "tunnels unsupported on this relay",
	protocol.TypeTunnelWSOpen:   "tunnels unsupported on this relay",
	protocol.TypeTunnelWSData:   "tunnels unsupported on this relay",
	protocol.TypeTunnelWSClose:  "tunnels unsupported on this relay",
}

// Every declared message type must be either forwardable or explicitly exempt.
// Read from the source rather than a hand-kept list, so a type added tomorrow
// fails this until somebody decides which it is — which is the whole point.
func TestEverySessionMessageTypeIsClassified(t *testing.T) {
	src, err := os.ReadFile("../protocol/messages.go")
	if err != nil {
		t.Fatalf("read protocol declarations: %v", err)
	}
	decls := regexp.MustCompile(`(?m)^\s*(Type[A-Za-z]+)\s+MessageType\s*=\s*"([^"]+)"`).FindAllStringSubmatch(string(src), -1)
	if len(decls) < 20 {
		t.Fatalf("only found %d message types; the declaration pattern has changed", len(decls))
	}

	for _, d := range decls {
		name, wire := d[1], protocol.MessageType(d[2])
		_, forwarded := forwardableTypes[wire]
		reason, exempt := exemptFromSessionForwarding[wire]
		switch {
		case forwarded && exempt:
			t.Errorf("%s (%q) is both forwardable and exempt (%q) — pick one", name, wire, reason)
		case !forwarded && !exempt:
			t.Errorf("%s (%q) is neither forwarded nor exempt.\n"+
				"  If viewers and agents exchange it over the session socket, add it to forwardableTypes —\n"+
				"  otherwise it silently does nothing on this relay while working on the hosted one.\n"+
				"  If it is relay-handled, rendezvous-only or unsupported here, say so in exemptFromSessionForwarding.",
				name, wire)
		}
	}
}
