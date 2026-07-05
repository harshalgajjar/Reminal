// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import "testing"

// TestNeedSnapshotOnResume pins the reconnect strategy: a viewer that still
// holds its terminal (an incremental live reconnect) gets only the raw delta it
// missed, NOT a full scrollback snapshot — the bug where unlocking a phone that
// missed a little output flooded it with up to 10k lines and stalled input.
func TestNeedSnapshotOnResume(t *testing.T) {
	cases := []struct {
		name         string
		cursor       uint64 // from_seq+1, or 0 if the viewer outran us
		oldest       uint64 // oldest buffered seq, 0 if empty
		wantSnapshot bool
	}{
		// Fresh join / brand-new page: from_seq 0 → cursor 1. Needs the whole
		// picture as one snapshot, not a mutation-by-mutation buffer replay.
		{"fresh join", 1, 1, true},
		// Viewer outran us (hot-restart reset our seq counter) → cursor 0.
		{"viewer outran us", 0, 42, true},
		// The exact flood case: live reconnect that missed a handful of lines.
		// The delta [cursor, latest] is still buffered → send it, NOT a snapshot.
		{"incremental reconnect, delta buffered", 500, 100, false},
		// Caught-up reconnect: nothing missed. No snapshot; the raw replay sends
		// nothing, which is correct (the viewer already has everything).
		{"caught-up reconnect", 1000, 100, false},
		// Fell so far behind the delta was evicted (cursor below the oldest seq
		// we still hold) → a raw replay would be incomplete, so snapshot.
		{"delta evicted, rolled off buffer", 50, 100, true},
		// Boundary: cursor exactly at oldest → the delta starts at a seq we still
		// have, so it's complete → delta, not snapshot.
		{"cursor at oldest boundary", 100, 100, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := needSnapshotOnResume(tc.cursor, tc.oldest); got != tc.wantSnapshot {
				t.Fatalf("needSnapshotOnResume(cursor=%d, oldest=%d) = %v, want %v",
					tc.cursor, tc.oldest, got, tc.wantSnapshot)
			}
		})
	}
}
