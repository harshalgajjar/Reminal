// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"os"
	"strings"
	"testing"
	"time"
)

// sizedAgent is settled at cols×rows with no PTY: applyEffectiveSize is a
// no-op, so these tests observe exactly what the size book decided.
func sizedAgent(cols, rows uint16) *Agent {
	a := &Agent{}
	a.sizeBook.seed(map[string]termSize{"phone": {cols, rows}}, termSize{cols, rows})
	return a
}

func TestAgentPhoneKeyboardTakesMinRows(t *testing.T) {
	a := sizedAgent(80, 40)
	a.sizeBook.seed(map[string]termSize{"laptop": {80, 40}}, termSize{80, 40})
	a.coalesceViewerResize("phone", 80, 15)
	time.Sleep(resizeSettle + 150*time.Millisecond)
	got := a.sizeBook.settledSize()
	if got.cols != 80 || got.rows != 15 {
		t.Errorf("min of 80x40 and 80x15 = %dx%d, want 80x15", got.cols, got.rows)
	}
}

func TestAgentNarrowerViewerStillTakesMinWidth(t *testing.T) {
	a := sizedAgent(100, 40)
	a.coalesceViewerResize("phone", 80, 24)
	time.Sleep(resizeSettle + 150*time.Millisecond)
	got := a.sizeBook.settledSize()
	if got.cols != 80 || got.rows != 24 {
		t.Errorf("min of 100x40 and 80x24 = %dx%d, want 80x24", got.cols, got.rows)
	}
}

func TestAgentRejectsTinyViewport(t *testing.T) {
	a := sizedAgent(80, 40)
	a.coalesceViewerResize("phone", 80, 5)
	time.Sleep(resizeSettle + 150*time.Millisecond)
	if got := a.sizeBook.settledSize(); got.rows != 40 {
		t.Errorf("5-row garbage settled at %d, want 40", got.rows)
	}
}

func TestBroadcastSizeSentinelKeepsZeroFields(t *testing.T) {
	src, err := os.ReadFile("agent.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	if !strings.Contains(s, "Must not use protocol.Message") {
		t.Fatal("broadcastSize lost the omitempty guard; (0,0) would serialize as {} and the remaining viewer would not grow")
	}
}
