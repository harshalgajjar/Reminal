// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import "testing"

// Input comes twice, on the relay and on the direct channel, each path in
// order. Each number is typed once, in order, whichever copy is first; the
// channel never gets ahead of what the relay will bring, and the relay
// alone carries on when the channel has gone.
func TestTerminalInputIsTypedOnceWhicheverPathIsFirst(t *testing.T) {
	const direct, relay = true, false
	type step struct {
		n      uint64
		direct bool
		typed  bool
	}
	for _, c := range []struct {
		name  string
		steps []step
	}{
		{"the channel first, then the relay's copies", []step{{1, direct, true}, {2, direct, true}, {1, relay, false}, {2, relay, false}, {3, relay, true}, {3, direct, false}}},
		{"the relay first", []step{{1, relay, true}, {1, direct, false}, {2, relay, true}, {2, direct, false}}},
		{"interleaved", []step{{1, direct, true}, {1, relay, false}, {2, relay, true}, {3, direct, true}, {2, direct, false}, {3, relay, false}}},
		{"the channel opened late: it does not jump ahead", []step{{1, relay, true}, {3, direct, false}, {2, relay, true}, {3, relay, true}, {4, direct, true}}},
		{"the channel gone: the relay carries on", []step{{1, direct, true}, {2, relay, true}, {3, relay, true}, {4, relay, true}}},
		{"a viewer met in the middle of its count", []step{{7, direct, false}, {7, relay, true}, {8, direct, true}, {8, relay, false}}},
	} {
		var o termOrder
		for i, s := range c.steps {
			if got := o.take(42, s.n, s.direct); got != s.typed {
				t.Errorf("%s: step %d (input %d, direct %v): typed %v, want %v", c.name, i+1, s.n, s.direct, got, s.typed)
			}
		}
	}
	// Each viewer has its own count.
	var o termOrder
	if !o.take(1, 1, direct) || !o.take(2, 1, direct) || o.take(1, 1, relay) || !o.take(2, 2, relay) {
		t.Error("two viewers' counts were mixed")
	}
	// Past what is kept, a viewer still typing goes on from the relay's copy.
	var many termOrder
	for s := uint32(1); s <= termStreamsKept+1; s++ {
		many.take(s, 5, relay)
	}
	if !many.take(1, 6, relay) || many.take(1, 6, direct) {
		t.Error("a viewer forgotten for room did not go on from its relay copy, once")
	}
}
