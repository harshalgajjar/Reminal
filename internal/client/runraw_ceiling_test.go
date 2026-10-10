// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// A hung `import` holds the X server grab, so every other X client (wmctrl)
// stalls behind it. runRaw must give up on it within captureCeiling instead of
// waiting forever, and the error is what the capture loop skips a frame on.
func TestRunRawGivesUpOnAHungCapture(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH shim is a shell script")
	}
	dir := t.TempDir()
	shim := filepath.Join(dir, "import")
	// The background sleep inherits stdout, so killing the shim alone would
	// still leave Output() waiting: this checks WaitDelay too.
	if err := os.WriteFile(shim, []byte("#!/bin/sh\nsleep 60 &\nsleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	old := captureCeiling
	captureCeiling = 1 * time.Second
	t.Cleanup(func() { captureCeiling = old })

	start := time.Now()
	done := make(chan error, 1)
	go func() {
		_, err := runRaw("import", "-window", "0x00400003", "png:-")
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "gave up after") {
			t.Fatalf("runRaw on a hung import = %v, want a 'gave up after' error", err)
		}
		// ceiling + WaitDelay (2s) + slack
		if el := time.Since(start); el > 5*time.Second {
			t.Fatalf("runRaw took %s, want about captureCeiling + WaitDelay", el)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("runRaw is still waiting on a hung import after 15s: no ceiling")
	}
}
