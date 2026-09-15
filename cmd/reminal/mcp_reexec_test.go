// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"strings"
	"testing"
)

func TestCacheReuseWarning(t *testing.T) {
	reset := func() {
		toolsListServed.Store(false)
		cacheReuseWarned.Store(false)
	}
	t.Cleanup(reset)

	t.Run("client that never listed is using someone else's list", func(t *testing.T) {
		reset()
		w := cacheReuseWarning()
		if w == "" {
			t.Fatal("a tool call with no preceding tools/list must warn")
		}
		if !strings.Contains(w, "pass it anyway") {
			t.Errorf("warning should offer the in-conversation remedy, got %q", w)
		}
	})

	t.Run("said once, not on every call", func(t *testing.T) {
		reset()
		if cacheReuseWarning() == "" {
			t.Fatal("first call should warn")
		}
		// It is a heuristic, not a proven mismatch: repeating it on a client that
		// structurally never calls tools/list would be pure noise.
		if w := cacheReuseWarning(); w != "" {
			t.Fatalf("want silence on repeat, got %q", w)
		}
	})

	t.Run("silent once this process served the list", func(t *testing.T) {
		reset()
		noteToolsListServed()
		if w := cacheReuseWarning(); w != "" {
			t.Fatalf("a list we produced is not cache reuse, got %q", w)
		}
	})
}
