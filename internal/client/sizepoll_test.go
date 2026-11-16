// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// A window resized resizes its session: the watcher reads the size off a
// reader that can read one (on Windows, a console's input side never can) and
// ticks when it changes, once per change, never on a failed read.
func TestAResizedWindowTicksItsWatcher(t *testing.T) {
	inputSide := func() (int, int, error) { return 0, 0, errors.New("the handle is invalid") }
	var mu sync.Mutex
	w, h := 120, 30
	outputSide := func() (int, int, error) { mu.Lock(); defer mu.Unlock(); return w, h, nil }
	resize := func(nw, nh int) { mu.Lock(); w, h = nw, nh; mu.Unlock() }

	if firstWorking(inputSide) != nil {
		t.Fatal("a reader that cannot read a size was taken")
	}
	read := firstWorking(inputSide, outputSide)
	if read == nil {
		t.Fatal("the output side, which can read a size, was not taken")
	}

	ch, stop := pollSize(read, 10*time.Millisecond)
	defer stop()
	tick := func(within time.Duration) bool {
		select {
		case <-ch:
			return true
		case <-time.After(within):
			return false
		}
	}
	if tick(60 * time.Millisecond) {
		t.Fatal("a tick with no resize")
	}
	resize(100, 40)
	if !tick(500 * time.Millisecond) {
		t.Fatal("the window was resized and the watcher never ticked: the session keeps its old size")
	}
	if tick(60 * time.Millisecond) {
		t.Fatal("one resize ticked twice")
	}

	// The input side alone, as the watcher read before: never a tick.
	ch2, stop2 := pollSize(firstWorking(inputSide), 10*time.Millisecond)
	defer stop2()
	resize(80, 24)
	select {
	case <-ch2:
		t.Fatal("a watcher with nothing to read ticked")
	case <-time.After(60 * time.Millisecond):
	}
}
