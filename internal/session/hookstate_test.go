// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// A hook state is written whole or not at all. Two hooks can fire close enough
// together to overlap — an agent reporting a state change as it exits while
// another starts a turn — and an in-place write leaves a reader the tail of the
// longer record stuck onto the shorter one. That does not parse, so the session
// silently loses its precise state and falls back to reading the screen until
// the record expires.
func TestWriteHookStateIsNeverSeenHalfWritten(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir) // Windows
	const id = "TEARTEST"

	path, err := hookStatePath(id)
	if err != nil {
		t.Fatal(err)
	}

	// Two states of different lengths, so a torn record is detectable rather
	// than coincidentally the right size.
	states := []string{"working", "done"}

	// The record exists before anyone reads, so every read below is of a
	// record. Reading first and writing second, the test once checked nothing
	// at all: on a busy machine the reader could finish before any writer had
	// run, and then the writers saw it was over and never wrote.
	if err := WriteHookState(id, states[0]); err != nil {
		t.Fatal(err)
	}

	// Each writer writes a fixed number of records, whatever the reader does.
	// Write errors are not the point here (Windows refuses to replace a file a
	// reader has open; that write is lost, not torn).
	var writers sync.WaitGroup
	for i := 0; i < 4; i++ {
		writers.Add(1)
		go func(i int) {
			defer writers.Done()
			for n := 0; n < 300; n++ {
				_ = WriteHookState(id, states[(i+n)%len(states)])
			}
		}(i)
	}
	finished := make(chan struct{})
	go func() { writers.Wait(); close(finished) }()

	// Read for as long as anyone is writing, however the scheduler orders
	// them, and once more after: every record read has to parse.
	reads := 0
	for done := false; !done; {
		select {
		case <-finished:
			done = true
		default:
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			continue // Windows: a read that met the replace; nothing torn
		}
		reads++
		var hs HookState
		if err := json.Unmarshal(raw, &hs); err != nil {
			t.Fatalf("read a half-written record: %q", raw)
		}
		if hs.State != "working" && hs.State != "done" {
			t.Fatalf("read a record holding a state nobody wrote: %q", raw)
		}
	}
	if reads == 0 {
		t.Fatal("never read the record")
	}

	// And nothing is left lying about in the state directory.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("left a temp file behind: %s", e.Name())
		}
	}
}

// A turn's state written while a subagent's count is being read and written
// back waits for it, so it is not overwritten by the older state; a count
// that cannot get the record in time is left unsaid.
func TestAHookStateIsNotUndoneByACountWrittenBackAroundIt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := WriteHookStateWith("LKLK2345", "done", &Background{Agents: 2}); err != nil {
		t.Fatal(err)
	}
	unlock, err := lockHookState("LKLK2345") // a count being written back
	if err != nil {
		t.Fatal(err)
	}
	wrote := make(chan error, 1)
	go func() { wrote <- WriteHookStateWith("LKLK2345", "working", nil) }()
	select {
	case <-wrote:
		t.Fatal("a turn's state was written in the middle of another hook's read and write back")
	case <-time.After(200 * time.Millisecond):
	}
	unlock()
	if err := <-wrote; err != nil {
		t.Fatal(err)
	}
	if hs := ReadHookStateAnyAge("LKLK2345"); hs == nil || hs.State != "working" || hs.Background != nil {
		t.Fatalf("after: %+v", hs)
	}

	unlock, err = lockHookState("LKLK2345")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if err := UpdateHookBackground("LKLK2345", &Background{Shells: 9}); err != nil {
		t.Fatal(err)
	}
	if hs := ReadHookStateAnyAge("LKLK2345"); hs == nil || hs.Background != nil {
		t.Fatalf("a count written without the record's lock: %+v", hs)
	}
}

// When a turn began is kept through the states after it: a short turn's
// "done" overwrites its "working" before anything reads it.
func TestHookStateKeepsWhenATurnBegan(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir) // Windows
	const id = "TURNTEST"
	if err := WriteHookStateWith(id, "done", nil); err != nil {
		t.Fatal(err)
	}
	if hs := ReadHookStateAnyAge(id); hs == nil || !hs.TurnAt.IsZero() {
		t.Fatalf("no turn yet, turn_at %+v", hs)
	}
	before := time.Now()
	_ = WriteHookStateWith(id, "working", nil)
	_ = WriteHookStateWith(id, "done", nil)
	_ = WriteHookStateWith(id, "input", nil)
	hs := ReadHookStateAnyAge(id)
	if hs == nil || hs.State != "input" || hs.TurnAt.Before(before) || hs.TurnAt.After(hs.TS) {
		t.Fatalf("turn_at not kept: %+v (turn began after %v)", hs, before)
	}
}

// A kind is the harness's own word and `reminal list` prints it: kept to
// printable text of a sane length, and an empty one is a background task.
func TestBackgroundAddOtherKeepsKindPrintable(t *testing.T) {
	var b Background
	b.AddOther("moni\x1b[2Jtor\a", 1)
	b.AddOther("  ", 2)
	b.AddOther(strings.Repeat("w", 60), 1)
	b.AddOther("monitor", 0)
	want := map[string]int{"moni[2Jtor": 1, BackgroundTask: 2, strings.Repeat("w", maxKindLen): 1}
	if len(b.Other) != len(want) {
		t.Fatalf("Other = %v, want %v", b.Other, want)
	}
	for k, n := range want {
		if b.Other[k] != n {
			t.Errorf("Other[%q] = %d, want %d (all: %v)", k, b.Other[k], n, b.Other)
		}
	}
	if b.OtherCount() != 4 {
		t.Errorf("OtherCount = %d, want 4", b.OtherCount())
	}
}
