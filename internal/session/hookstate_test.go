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

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for n := 0; n < 300; n++ {
				select {
				case <-stop:
					return
				default:
				}
				_ = WriteHookState(id, states[(i+n)%len(states)])
			}
		}(i)
	}

	// Read as fast as they write; every record that exists has to parse.
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(stop)
		for n := 0; n < 1500; n++ {
			raw, err := os.ReadFile(path)
			if err != nil {
				continue // not written yet
			}
			var hs HookState
			if err := json.Unmarshal(raw, &hs); err != nil {
				t.Errorf("read a half-written record: %q", raw)
				return
			}
			if hs.State != "working" && hs.State != "done" {
				t.Errorf("read a record holding a state nobody wrote: %q", raw)
				return
			}
		}
	}()
	wg.Wait()

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
