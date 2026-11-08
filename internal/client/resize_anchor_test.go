// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"fmt"
	"strings"
	"testing"
)

// screenRows renders the live emulator's screen as right-trimmed rows.
func screenRows(a *Agent) []string {
	rows := strings.Split(a.screen.Render(), "\n")
	for i, r := range rows {
		rows[i] = strings.TrimRight(r, " ")
	}
	return rows
}

// lastNonBlankRow returns the index of the bottom-most row with content, or -1.
func lastNonBlankRow(rows []string) int {
	for i := len(rows) - 1; i >= 0; i-- {
		if strings.TrimSpace(rows[i]) != "" {
			return i
		}
	}
	return -1
}

// feedLines flows n numbered lines through the live screen, exactly as PTY
// output would arrive.
func feedLines(a *Agent, n int) {
	for i := 1; i <= n; i++ {
		_, _ = a.screen.Write([]byte(fmt.Sprintf("line-%04d\r\n", i)))
	}
}

// A terminal is BOTTOM-anchored on resize. Growing the window reveals older
// lines above and leaves the last line where it was, relative to the bottom —
// drag the corner of Terminal.app and the prompt stays put while history
// appears above it.
//
// vt.Emulator.Resize is top-anchored: it appends blank rows at the BOTTOM and
// leaves content where it is. That is the void under the prompt after a phone
// keyboard collapses — the snapshot built from this screen paints the frame at
// the top with dead space below, so the viewer's own (correctly bottom-
// anchored) grow gets overwritten by it.
func TestResizeScreenGrowAnchorsBottom(t *testing.T) {
	a := clearingAgent(t, 80, 10)
	feedLines(a, 30)

	before := screenRows(a)
	sbBefore := a.screen.HistoryLen()
	if got := lastNonBlankRow(before); got != len(before)-2 {
		t.Fatalf("setup: last content row %d, want %d (cursor sits on the blank row below)", got, len(before)-2)
	}
	if sbBefore == 0 {
		t.Fatal("setup: expected scrollback to hold the lines that scrolled off")
	}

	const grown = 20
	a.resizeScreen(80, grown)

	rows := screenRows(a)
	if len(rows) != grown {
		t.Fatalf("screen is %d rows after grow, want %d", len(rows), grown)
	}
	// The last line must still sit one row above the bottom (the cursor's row),
	// not stay where it was with blank rows appended beneath it.
	if got := lastNonBlankRow(rows); got != grown-2 {
		t.Errorf("last content row %d after growing to %d, want %d — content did not stay anchored to the bottom", got, grown, grown-2)
	}
	if rows[grown-2] != "line-0030" {
		t.Errorf("row above the cursor is %q, want %q", rows[grown-2], "line-0030")
	}
	// The 10 new rows must be filled from scrollback, not left blank.
	if sbAfter := a.screen.HistoryLen(); sbAfter != sbBefore-(grown-10) {
		t.Errorf("scrollback is %d lines after grow, want %d — older lines were not pulled back onto the screen",
			sbAfter, sbBefore-(grown-10))
	}
	if strings.TrimSpace(rows[0]) == "" {
		t.Errorf("top row is blank after grow; want an older line revealed from scrollback")
	}
}

// Shrinking must scroll the TOP away into scrollback and keep the bottom —
// never truncate the bottom rows, which is what silently ate the prompt and
// the last lines of output when a phone keyboard opened.
func TestResizeScreenShrinkKeepsBottom(t *testing.T) {
	a := clearingAgent(t, 80, 20)
	feedLines(a, 30)

	sbBefore := a.screen.HistoryLen()
	const shrunk = 10
	a.resizeScreen(80, shrunk)

	rows := screenRows(a)
	if len(rows) != shrunk {
		t.Fatalf("screen is %d rows after shrink, want %d", len(rows), shrunk)
	}
	if got := lastNonBlankRow(rows); got != shrunk-2 {
		t.Errorf("last content row %d after shrinking to %d, want %d", got, shrunk, shrunk-2)
	}
	if rows[shrunk-2] != "line-0030" {
		t.Errorf("row above the cursor is %q, want %q — the bottom of the screen was truncated away", rows[shrunk-2], "line-0030")
	}
	if sbAfter := a.screen.HistoryLen(); sbAfter != sbBefore+(20-shrunk) {
		t.Errorf("scrollback is %d lines after shrink, want %d — the rows scrolled off the top were dropped instead of kept",
			sbAfter, sbBefore+(20-shrunk))
	}
}

// A resize that only changes width must not move content vertically.
func TestResizeScreenWidthOnlyKeepsRows(t *testing.T) {
	a := clearingAgent(t, 80, 10)
	feedLines(a, 30)

	want := lastNonBlankRow(screenRows(a))
	a.resizeScreen(100, 10)
	if got := lastNonBlankRow(screenRows(a)); got != want {
		t.Errorf("last content row moved from %d to %d on a width-only resize", want, got)
	}
}
