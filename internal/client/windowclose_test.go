// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"testing"

	"github.com/reminal/reminal/internal/protocol"
)

// TestWindowCloseChannelGate pins that a window_close request is actually served
// on both channels. It's easy to add a message type and its handler but forget
// the servesOnThisChannel gate — machineAccepts for the owner/machine channel,
// and NOT dirChannelOnly (which would wrongly reserve it to the machine channel
// and make a session's pane ✕→close a silent no-op).
func TestWindowCloseChannelGate(t *testing.T) {
	if !machineAccepts[protocol.TypeWindowClose] {
		t.Error("machine channel must accept window_close (owner closing a mirrored window)")
	}
	if dirChannelOnly[protocol.TypeWindowClose] {
		t.Error("window_close must not be machine-channel-only — a session pane must be able to close its window")
	}
	if a := (&Agent{}); !a.servesOnThisChannel(protocol.TypeWindowClose) {
		t.Error("a session agent must serve window_close")
	}
	if a := (&Agent{machine: true}); !a.servesOnThisChannel(protocol.TypeWindowClose) {
		t.Error("the machine agent must serve window_close")
	}
}
