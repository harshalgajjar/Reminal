// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import "testing"

// Every change of size in a burst is kept, up to resizeBurstMax; a resize
// to the size already recorded is not; and a viewer reporting a new size a
// thousand times with no output between cannot grow the ring past the bound
// or evict the output before it.
func TestResizeMarkersAreKeptDistinctAndBounded(t *testing.T) {
	sb := newScrollback(1 << 20)
	for i := 0; i < 10; i++ {
		sb.Append("output before the burst")
	}
	sb.AppendResize(55, 58)
	sb.AppendResize(55, 58) // the same size again: nothing
	sb.AppendResize(102, 45)
	sb.AppendResize(55, 58)
	sb.AppendResize(102, 45)
	markers := func() (n int, first, last scrollEntry) {
		for _, e := range sb.From(0) {
			if e.Cols > 0 {
				if n == 0 {
					first = e
				}
				last = e
				n++
			}
		}
		return
	}
	if n, _, _ := markers(); n != 4 {
		t.Fatalf("a fold flipped four ways: %d markers, want 4 (the repeat dropped)", n)
	}
	for i := 0; i < 1000; i++ {
		sb.AppendResize(60+i%50, 20+i%30)
	}
	n, first, last := markers()
	if n > resizeBurstMax {
		t.Errorf("after a 1000-resize burst the ring holds %d markers, want at most %d", n, resizeBurstMax)
	}
	if first.Cols != 55 || first.Rows != 58 {
		t.Errorf("the burst's first size is %dx%d, want 55x58 kept", first.Cols, first.Rows)
	}
	if last.Cols != 60+999%50 || last.Rows != 20+999%30 {
		t.Errorf("the burst's last size is %dx%d, want the newest %dx%d", last.Cols, last.Rows, 60+999%50, 20+999%30)
	}
	outputs := 0
	for _, e := range sb.From(0) {
		if e.Data != "" {
			outputs++
		}
	}
	if outputs != 10 {
		t.Errorf("%d output entries in the ring after the burst, want all 10", outputs)
	}
	// Output after the burst starts a new one.
	sb.Append("output after")
	sb.AppendResize(80, 24)
	if n2, _, last2 := markers(); n2 != n+1 || last2.Cols != 80 {
		t.Errorf("a resize after output: %d markers (was %d), last %dx%d", n2, n, last2.Cols, last2.Rows)
	}
}
