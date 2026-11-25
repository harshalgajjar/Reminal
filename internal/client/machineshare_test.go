// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !windows

package client

import (
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Forty readers of one machine's CPU (as forty session processes are: each
// its own machineShare, all on one folder) run ONE sampler between them, however
// often they ask, and all of them get its reading. When they stop asking the
// sampler stops, and it starts again when one asks again.
//
// Each session used to take its own reading: forty sessions asked at once
// ran forty samplers at once.
func TestManyReadersRunOneCPUSampler(t *testing.T) {
	dir := t.TempDir()
	var running, most, starts atomic.Int32
	sample := func(emit func(string) bool, wanted func() bool) {
		starts.Add(1)
		n := running.Add(1)
		for m := most.Load(); n > m && !most.CompareAndSwap(m, n); m = most.Load() {
		}
		defer running.Add(-1)
		for emit("42") {
			time.Sleep(20 * time.Millisecond)
		}
	}
	const readers = 40
	shares := make([]*machineShare, readers)
	for i := range shares {
		shares[i] = settleAtEnd(t, &machineShare{dir: dir, name: "cpu", sample: sample, fresh: 300 * time.Millisecond, wantEvery: 50 * time.Millisecond, wantFor: 400 * time.Millisecond, tryEvery: 30 * time.Millisecond})
	}
	ask := func(d time.Duration) (got int32) {
		var wg sync.WaitGroup
		var n atomic.Int32
		for _, s := range shares {
			wg.Add(1)
			go func(s *machineShare) {
				defer wg.Done()
				ok := false
				for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(15 * time.Millisecond) {
					if v, has := s.read(); has && v == "42" {
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
	mk := func(sample func(func(string) bool, func() bool)) *machineShare {
		return settleAtEnd(t, &machineShare{dir: dir, name: "cpu", sample: sample, fresh: 200 * time.Millisecond, wantEvery: 20 * time.Millisecond, wantFor: 5 * time.Second, tryEvery: 20 * time.Millisecond})
	}
	// One that gives a reading and ends at once, as a holder that died does.
	gone := mk(func(emit func(string) bool, _ func() bool) { emit("10") })
	next := mk(func(emit func(string) bool, _ func() bool) {
		for emit("77") {
			time.Sleep(20 * time.Millisecond)
		}
	})
	gone.read()
	var v string
	var ok bool
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(15 * time.Millisecond) {
		if v, ok = next.read(); ok && v == "77" {
			break
		}
	}
	if !ok || v != "77" {
		t.Fatalf("no reader took over: %v, %v", v, ok)
	}
}

// Two readings on one folder (the CPU and the battery) each have their own
// files and their own sampler: neither holds the other's lock or reads the
// other's value.
func TestMachineReadingsDoNotShareFiles(t *testing.T) {
	dir := t.TempDir()
	mk := func(name, val string) *machineShare {
		return settleAtEnd(t, &machineShare{dir: dir, name: name, fresh: time.Second, wantEvery: 20 * time.Millisecond, wantFor: 400 * time.Millisecond, tryEvery: 20 * time.Millisecond,
			sample: func(emit func(string) bool, _ func() bool) {
				for emit(val) {
					time.Sleep(20 * time.Millisecond)
				}
			}})
	}
	cpu, bat := mk("cpu", "42.0"), mk("battery", "85,charging,123")
	var c, b string
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end) && (c == "" || b == ""); time.Sleep(15 * time.Millisecond) {
		c, _ = cpu.read()
		b, _ = bat.read()
	}
	if c != "42.0" || b != "85,charging,123" {
		t.Fatalf("cpu read %q, battery read %q", c, b)
	}
	// A value with a space in it would break the file's form: not written.
	bad := mk("bad", "two words")
	if v, ok := bad.readWithin(300 * time.Millisecond); ok {
		t.Fatalf("a value that is not one word was shared: %q", v)
	}
}

// A battery reading survives being shared as a word, "no battery" too, and
// a word that is not a reading is refused (the reader then reads the battery
// itself) and never taken for "no battery".
func TestBatteryWord(t *testing.T) {
	pct := 85
	for _, b := range []*Battery{{Pct: &pct, State: "charging", Mins: 123}, {Pct: &pct, State: "charged"}, nil} {
		got, ok := batteryFromWord(batteryWord(b))
		if !ok || (b == nil) != (got == nil) || (b != nil && (*got.Pct != *b.Pct || got.State != b.State || got.Mins != b.Mins)) {
			t.Fatalf("%+v came back as %+v, %v (word %q)", b, got, ok, batteryWord(b))
		}
	}
	zero := 0
	if got, ok := batteryFromWord(batteryWord(&Battery{Pct: &zero, State: "discharging"})); !ok || got == nil || *got.Pct != 0 {
		t.Fatalf("0%% did not come back as a reading: %+v, %v", got, ok)
	}
	for _, w := range []string{"", "85", "85,charging", "x,charging,1", "101,charging,1", "85,asleep,1", "85,charging,-1", "42.0"} {
		if b, ok := batteryFromWord(w); ok {
			t.Fatalf("%q was read as a battery reading (%+v)", w, b)
		}
	}
}

// settleAtEnd has s out of its directory before the test's folder is removed:
// as the test ends nobody wants readings any more (its .want file goes), and
// the offers to sample that s started, the one sampling among them, are
// waited for. One still running wrote into the folder while it was being
// removed, and the test failed on the removal ("directory not empty").
// Registered after t.TempDir, so it runs before the folder goes.
func settleAtEnd(t *testing.T, s *machineShare) *machineShare {
	t.Helper()
	t.Cleanup(func() {
		for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
			_ = os.Remove(s.path(".want"))
			if s.leading.Load() == 0 {
				return
			}
		}
		t.Errorf("%s: an offer to sample was still running 10 s after the test", s.name)
	})
	return s
}
