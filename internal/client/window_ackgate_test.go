// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"testing"
	"time"
)

// Over the relay a window's pictures share the connection with the session's
// terminal, in order. They go one at a time there, and a viewer that acks is
// waited for: a frame sent on a timer while the one before was still on its
// way put every echo behind both. A direct channel carries no terminal and
// keeps its pipeline.
func TestRelayFramesWaitForTheirAck(t *testing.T) {
	for _, c := range []struct {
		name                       string
		h264, wsVideo, relay, acks bool
		limit                      uint64
		wait                       time.Duration
	}{
		{"pictures over the relay, the viewer acks", false, false, true, true, 1, ackWaitSlowLink},
		{"pictures over the relay, a viewer that never acked", false, false, true, false, 1, ackWaitTimeout},
		{"pictures on a direct channel", false, false, false, true, maxFramesInFlight, ackWaitTimeout},
		{"video over the relay, the viewer acks", true, true, true, true, 6, ackWaitSlowLink},
		{"video on a direct channel", true, false, false, true, maxFramesInFlightH264, ackWaitTimeout},
	} {
		limit, wait := ackGate(c.h264, c.wsVideo, c.relay, c.acks)
		if limit != c.limit || wait != c.wait {
			t.Errorf("%s: %d in flight, wait %s; want %d, %s", c.name, limit, wait, c.limit, c.wait)
		}
	}
	if ackWaitSlowLink <= ackWaitTimeout || ackWaitSlowLink >= streamAckIdleTimeout {
		t.Errorf("the slow-link wait (%s) must be longer than the short one (%s) and end before a silent viewer is given up on (%s)", ackWaitSlowLink, ackWaitTimeout, streamAckIdleTimeout)
	}
}
