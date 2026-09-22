// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/reminal/reminal/internal/protocol"
)

// A machine says what it can be asked for, so a caller need not ask and wait
// for a silence to find out.
func TestMachineCapsDescribeThisBuild(t *testing.T) {
	caps := machineCaps()
	if len(caps) == 0 {
		t.Fatal("a machine advertised no capabilities at all")
	}
	have := map[string]bool{}
	for _, c := range caps {
		have[c] = true
	}
	// Everything the agent accepts is advertised, and nothing else is.
	for tp, ok := range machineAccepts {
		if ok && !have[string(tp)] {
			t.Errorf("%s is accepted but not advertised", tp)
		}
	}
	for _, c := range caps {
		if !machineAccepts[protocol.MessageType(c)] {
			t.Errorf("%s is advertised but not accepted", c)
		}
	}
	// Sorted, so a caller can compare two machines' lists without sorting.
	for i := 1; i < len(caps); i++ {
		if caps[i-1] > caps[i] {
			t.Fatalf("capabilities are not in order: %v", caps)
		}
	}
}

// The list rides on the ordinary directory reply, and an older caller that
// does not know about it reads the reply exactly as before.
func TestCapsRideOnTheDirectoryReply(t *testing.T) {
	raw, err := json.Marshal(protocol.DirResponse{Hostname: "box", Caps: machineCaps()})
	if err != nil {
		t.Fatal(err)
	}
	var old struct {
		Hostname string                `json:"hostname"`
		Sessions []protocol.DirSession `json:"sessions"`
	}
	if err := json.Unmarshal(raw, &old); err != nil {
		t.Fatalf("a reply with capabilities did not read as one without: %v", err)
	}
	if old.Hostname != "box" {
		t.Fatalf("hostname = %q", old.Hostname)
	}
	if !strings.Contains(string(raw), `"caps"`) {
		t.Fatal("the capabilities were not sent")
	}
}

// A request this build has no handler for is answered as such, rather than
// dropped — the answer says which request, and that it was not a failure.
func TestUnsupportedAckSaysWhatItIs(t *testing.T) {
	ack := dirAck{ReqID: "r1", Unsupported: true, Error: "this reminal does not answer dir_something"}
	raw, err := json.Marshal(ack)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["unsupported"] != true || got["req_id"] != "r1" {
		t.Fatalf("ack = %s", raw)
	}
	if ok, _ := got["ok"].(bool); ok {
		t.Fatal("an unsupported request must not read as done")
	}
}
