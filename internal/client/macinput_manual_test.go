// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build darwin

package client

// Manual test for how long macOS input injection takes, from the call to the
// event posted. Gated on REMINAL_MAC_INPUT_TEST=1 (it moves the real pointer
// and clicks), with REMINAL_CAPTURE_HELPER naming the reminal-capture to use
// and REMINAL_MAC_INPUT_AT the screen point "x,y" to click at.

import (
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestMacInputCost(t *testing.T) {
	if os.Getenv("REMINAL_MAC_INPUT_TEST") != "1" {
		t.Skip("set REMINAL_MAC_INPUT_TEST=1 on a Mac to enable")
	}
	var x, y int
	if at := strings.Split(os.Getenv("REMINAL_MAC_INPUT_AT"), ","); len(at) == 2 {
		x, y = atoi(at[0]), atoi(at[1])
	}
	w := winInfo{X: x - 100, Y: y - 100, W: 200, H: 200}
	b := darwinWindows{}
	n := 20
	cost := func(name string, f func() error) {
		var ds []float64
		for i := 0; i < n; i++ {
			t0 := time.Now()
			if err := f(); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			ds = append(ds, float64(time.Since(t0).Microseconds())/1000)
			time.Sleep(60 * time.Millisecond)
		}
		sort.Float64s(ds)
		t.Logf("%-10s p50 %7.2f ms  p95 %7.2f ms", name, ds[n/2], ds[n*95/100])
	}
	cost("click", func() error { return b.clickN(w, 0.5, 0.5, 1, false) })
	cost("scroll", func() error { return b.scroll(w, 0.5, 0.5, 0, 40, "") })
	cost("drag-step", func() error { return b.dragPhase(w, "move", 0.5, 0.5) })
	cost("key", func() error { return b.key(w, "a") })
	_ = b.dragPhase(w, "up", 0.5, 0.5)
}

// TestKeysHelperTakesPointerEvents: the running helper answers ok to a click,
// a scroll and a drag step, so clickN, scroll and dragPhase use it.
func TestKeysHelperTakesPointerEvents(t *testing.T) {
	if os.Getenv("REMINAL_MAC_INPUT_TEST") != "1" {
		t.Skip("set REMINAL_MAC_INPUT_TEST=1 on a Mac to enable")
	}
	if !keyHelperClick(10, 10, false, 1) {
		t.Error("the helper did not take a click")
	}
	if !keyHelperScroll(10, 10, 0, 0) {
		t.Error("the helper did not take a scroll")
	}
	if !keyHelperDrag("up", 10, 10) {
		t.Error("the helper did not take a drag step")
	}
	if !keyHelperType("") || !keyHelperKey(53, nil) {
		t.Error("the helper stopped taking keys")
	}
}
