// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"testing"
	"time"
)

// A stream that never produced a picture stops sending heartbeats (which keep
// a pane on "Connecting…") after noPictureAfter and tells the pane why, every
// noPictureEvery; once a picture arrives it is a normal stream again.
func TestNoPictureDue(t *testing.T) {
	t0 := time.Unix(1_000_000, 0)
	cases := []struct {
		name       string
		got        bool
		since      time.Duration
		lastTold   time.Time
		hold, tell bool
	}{
		{"just started", false, 3 * time.Second, time.Time{}, false, false},
		{"no picture past the wait: tell", false, 11 * time.Second, time.Time{}, true, true},
		{"told 2 s ago: hold, don't repeat", false, 13 * time.Second, t0.Add(11 * time.Second), true, false},
		{"told 6 s ago: tell again", false, 17 * time.Second, t0.Add(11 * time.Second), true, true},
		{"had a picture: normal heartbeats", true, time.Minute, time.Time{}, false, false},
	}
	for _, c := range cases {
		hold, tell := noPictureDue(c.got, t0, c.lastTold, t0.Add(c.since))
		if hold != c.hold || tell != c.tell {
			t.Errorf("%s: hold=%v tell=%v, want %v %v", c.name, hold, tell, c.hold, c.tell)
		}
	}
}
