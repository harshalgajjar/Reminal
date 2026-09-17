// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build darwin

package client

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestShellCwdSurvivesAHangingLsof pins the incident this timeout exists for: a
// spinning lsof used to block the lookup forever, and because it runs during
// agent startup that stalled the readiness handshake — `reminal new` failed
// with "didn't report ready within 15s" and left a dead session behind.
//
// Aimed at lsofCwd rather than shellCwd: the syscall now answers first, so
// shellCwd would never reach the fallback and the timeout would go untested.
// A fake lsof that never exits stands in for the real one misbehaving.
func TestShellCwdSurvivesAHangingLsof(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "lsof")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nwhile :; do sleep 1; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	done := make(chan string, 1)
	start := time.Now()
	go func() { done <- lsofCwd(os.Getpid()) }()

	select {
	case got := <-done:
		if elapsed := time.Since(start); elapsed > shellCwdTimeout+3*time.Second {
			t.Fatalf("returned after %v, want ~%v — the timeout is not bounding it", elapsed, shellCwdTimeout)
		}
		if got != "" {
			t.Fatalf("a hung lsof should yield no cwd, got %q", got)
		}
	case <-time.After(shellCwdTimeout + 5*time.Second):
		t.Fatal("lsofCwd never returned — a hung lsof still blocks the agent's startup")
	}
}
