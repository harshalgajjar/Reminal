// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !windows

package client

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Forty readers of one machine's CPU (as forty session processes are: each
// its own cpuShare, all on one folder) run ONE sampler between them, however
// often they ask, and all of them get its reading. When they stop asking the
// sampler stops, and it starts again when one asks again.
//
// Each session used to take its own reading: forty sessions asked at once
// ran forty samplers at once.
func TestManyReadersRunOneCPUSampler(t *testing.T) {
	dir := t.TempDir()
	var running, most, starts atomic.Int32
	sample := func(emit func(float64) bool, wanted func() bool) {
		starts.Add(1)
		n := running.Add(1)
		for m := most.Load(); n > m && !most.CompareAndSwap(m, n); m = most.Load() {
		}
		defer running.Add(-1)
		for emit(42) {
			time.Sleep(20 * time.Millisecond)
		}
	}
	const readers = 40
	shares := make([]*cpuShare, readers)
	for i := range shares {
		shares[i] = &cpuShare{dir: dir, sample: sample, fresh: 300 * time.Millisecond, wantEvery: 50 * time.Millisecond, wantFor: 400 * time.Millisecond, tryEvery: 30 * time.Millisecond}
	}
	ask := func(d time.Duration) (got int32) {
		var wg sync.WaitGroup
		var n atomic.Int32
		for _, s := range shares {
			wg.Add(1)
			go func(s *cpuShare) {
				defer wg.Done()
				ok := false
				for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(15 * time.Millisecond) {
					if v, has := s.read(); has && v == 42 {
						ok = true
					}
				}
				if ok {
					n.Add(1)
				}
			}(s)
		}
		wg.Wait()
		return n.Load()
	}
	if got := ask(1500 * time.Millisecond); got != readers {
		t.Fatalf("%d of %d readers got the reading", got, readers)
	}
	if m, s := most.Load(), starts.Load(); m != 1 || s != 1 {
		t.Fatalf("%d readers ran %d samplers at once, started %d times: want one sampler, started once", readers, m, s)
	}
	// Nobody asks: the sampler stops.
	for end := time.Now().Add(3 * time.Second); running.Load() != 0 && time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
	}
	if n := running.Load(); n != 0 {
		t.Fatalf("with no reader asking, %d sampler(s) still run", n)
	}
	time.Sleep(400 * time.Millisecond) // the last reading goes stale
	if v, ok := shares[0].read(); ok {
		t.Fatalf("a stale reading was given as the value (%v)", v)
	}
	// Asked again: one sampler again.
	if got := ask(1000 * time.Millisecond); got != readers {
		t.Fatalf("asked again, %d of %d readers got the reading", got, readers)
	}
	if m, s := most.Load(), starts.Load(); m != 1 || s != 2 {
		t.Fatalf("asked again: %d samplers at once, started %d times in all: want one at a time, twice", m, s)
	}
}

// The process that samples dies (its lock goes with it): the next reader
// takes over, and the reading that was left is not given as the value once
// it is old.
func TestAnotherReaderTakesOverTheCPUSampler(t *testing.T) {
	dir := t.TempDir()
	mk := func(sample func(func(float64) bool, func() bool)) *cpuShare {
		return &cpuShare{dir: dir, sample: sample, fresh: 200 * time.Millisecond, wantEvery: 20 * time.Millisecond, wantFor: 5 * time.Second, tryEvery: 20 * time.Millisecond}
	}
	// One that gives a reading and ends at once, as a holder that died does.
	gone := mk(func(emit func(float64) bool, _ func() bool) { emit(10) })
	next := mk(func(emit func(float64) bool, _ func() bool) {
		for emit(77) {
			time.Sleep(20 * time.Millisecond)
		}
	})
	gone.read()
	var v float64
	var ok bool
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(15 * time.Millisecond) {
		if v, ok = next.read(); ok && v == 77 {
			break
		}
	}
	if !ok || v != 77 {
		t.Fatalf("no reader took over: %v, %v", v, ok)
	}
}
