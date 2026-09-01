// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"testing"
	"time"
)

// Two viewers: the PTY is min(widths)×min(heights), so a laptop cannot
// overwrite a phone's keyboard-open height and hide the input off-screen.
func TestTwoViewersTakeMinWidthAndHeight(t *testing.T) {
	a := sizedAgent(80, 40)
	a.viewerSizes = map[string][2]uint16{"laptop": {80, 40}}

	a.coalesceViewerResize("phone", 80, 15)
	time.Sleep(resizeSettle + 150*time.Millisecond)

	a.viewerSizeMu.Lock()
	gotC, gotR := a.viewerCols, a.viewerRows
	a.viewerSizeMu.Unlock()
	if gotC != 80 || gotR != 15 {
		t.Errorf("min of 80x40 and 80x15 = %dx%d, want 80x15", gotC, gotR)
	}

	// The laptop keeping its own size must not steal the PTY back.
	a.coalesceViewerResize("laptop", 80, 40)
	time.Sleep(resizeSettle + 150*time.Millisecond)

	a.viewerSizeMu.Lock()
	gotC, gotR = a.viewerCols, a.viewerRows
	a.viewerSizeMu.Unlock()
	if gotC != 80 || gotR != 15 {
		t.Errorf("laptop re-report grew the PTY to %dx%d, want 80x15", gotC, gotR)
	}
}

func TestTwoViewersGrowWhenSmallerViewerGrows(t *testing.T) {
	a := sizedAgent(80, 15)
	a.viewerSizes = map[string][2]uint16{
		"laptop": {80, 40},
		"phone":  {80, 15},
	}

	a.coalesceViewerResize("phone", 80, 40)
	time.Sleep(resizeSettle + 150*time.Millisecond)

	a.viewerSizeMu.Lock()
	gotR := a.viewerRows
	a.viewerSizeMu.Unlock()
	if gotR != 40 {
		t.Errorf("after the phone grew, min rows = %d, want 40", gotR)
	}
}

func TestTwoViewersMinIsIndependentPerAxis(t *testing.T) {
	a := sizedAgent(100, 40)
	a.coalesceViewerResize("wide-short", 100, 20)
	a.coalesceViewerResize("narrow-tall", 80, 40)
	time.Sleep(resizeSettle + 150*time.Millisecond)

	a.viewerSizeMu.Lock()
	gotC, gotR := a.viewerCols, a.viewerRows
	a.viewerSizeMu.Unlock()
	if gotC != 80 || gotR != 20 {
		t.Errorf("min of 100x20 and 80x40 = %dx%d, want 80x20", gotC, gotR)
	}
}

func TestMinViewerSizeEmpty(t *testing.T) {
	c, r := minViewerSize(nil)
	if c != 0 || r != 0 {
		t.Errorf("empty map = %dx%d, want 0x0", c, r)
	}
}

func TestCoalesceRejectsTinyViewport(t *testing.T) {
	a := sizedAgent(80, 40)
	a.viewerSizes = map[string][2]uint16{"phone": {80, 40}}
	a.coalesceViewerResize("phone", 80, 5)
	time.Sleep(resizeSettle + 150*time.Millisecond)
	a.viewerSizeMu.Lock()
	gotR := a.viewerRows
	a.viewerSizeMu.Unlock()
	if gotR != 40 {
		t.Errorf("5-row garbage report settled at %d, want 40", gotR)
	}
}
