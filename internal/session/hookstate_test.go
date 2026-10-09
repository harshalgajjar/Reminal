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
