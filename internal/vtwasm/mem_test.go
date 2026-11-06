// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package vtwasm

import (
	"fmt"
	"strings"
	"testing"
)

// The memory is reserved once and committed as it grows: a grow gives the
// same bytes at the same address, never a copy; the maximum is clamped to
// memoryMax, and a grow past it is refused rather than reserved.
func TestMemoryGrowsInPlaceAndStopsAtItsMaximum(t *testing.T) {
	const page = 64 << 10
	m := allocate(page, 4<<30) // what wazero asks for a module with no maximum
	defer m.Free()
	first := m.Reallocate(page)
	if uint64(len(first)) != page {
		t.Fatalf("the first memory is %d bytes, want %d", len(first), page)
	}
	first[0], first[page-1] = 1, 2
	grown := m.Reallocate(64 * page)
	if grown == nil || &grown[0] != &first[0] {
		t.Fatalf("a grow moved the memory (or was refused): %p then %p", first, grown)
	}
	if grown[0] != 1 || grown[page-1] != 2 || grown[64*page-1] != 0 {
		t.Error("a grow lost what was written, or gave pages that are not zero")
	}
	if m.Reallocate(memoryMax) == nil {
		t.Error("a grow to the maximum was refused")
	}
	if m.Reallocate(memoryMax+page) != nil {
		t.Error("a grow past the maximum was not refused")
	}
}

// A terminal that would grow past memoryMax is refused the memory, and
// libghostty takes that as it takes its byte limit: the oldest pages of
// scrollback go, the newest rows stay, and the terminal goes on. Nothing
// traps, nothing in the process panics, and a terminal made after it has
// its own memory.
func TestATerminalPastItsMemoryKeepsItsNewestRowsAndGoesOn(t *testing.T) {
	saved := memoryMax
	memoryMax = 8 << 20
	defer func() { memoryMax = saved }()

	v, err := New(200, 50, 1_000_000)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer v.Close()
	const n = 100_000
	for i := 0; i < n; i++ {
		if _, err := v.Write([]byte(fmt.Sprintf("row-%06d %s\r\n", i, strings.Repeat("x", 180)))); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if err := v.Err(); err != nil {
		t.Fatalf("the terminal is done: %v", err)
	}
	kept := v.ScrollbackRows()
	if kept == 0 || kept >= n-50 {
		t.Fatalf("%d rows kept of %d: the memory bound did not hold, or nothing was", kept, n)
	}
	hist, err := v.History()
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if last := fmt.Sprintf("row-%06d", n-51); !strings.Contains(hist, last) {
		t.Errorf("the newest rows went: %s not in the history (%d rows kept)", last, kept)
	}
	if strings.Contains(hist, "row-000000") {
		t.Error("the oldest row is still there: nothing was pruned")
	}

	w, err := New(80, 24, 100)
	if err != nil {
		t.Fatalf("a terminal after it: %v", err)
	}
	defer w.Close()
	if _, err := w.Write([]byte("still here")); err != nil {
		t.Errorf("a terminal after it: write: %v", err)
	}
	if out, _ := w.Text(); !strings.Contains(out, "still here") {
		t.Errorf("a terminal after it shows %q", out)
	}
}
