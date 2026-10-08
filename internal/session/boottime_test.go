// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package session

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestBootTimeIsInThePast(t *testing.T) {
	boot, ok := bootTime()
	if !ok {
		if runtime.GOOS == "linux" || runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
			t.Fatal("no boot time on " + runtime.GOOS)
		}
		t.Skip("no boot time here")
	}
	if !boot.Before(time.Now()) || time.Since(boot) > 10*365*24*time.Hour {
		t.Fatalf("boot time %v", boot)
	}
}

// A record cut short by a power cut is from before this boot: its session
// is gone and may come back. One that cannot be read and was written since
// may be a running agent's, mid-write: not knowing is not gone.
func TestAnUnreadableRecordIsGoneOnlyFromBeforeThisBoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".reminal")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "active-CUT00001.json")
	if err := os.WriteFile(p, []byte(`{"id":"CUT0`), 0o600); err != nil {
		t.Fatal(err)
	}
	boot := time.Now().Add(-time.Hour)
	defer func(f func() (time.Time, bool)) { bootTimeFn = f }(bootTimeFn)
	bootTimeFn = func() (time.Time, bool) { return boot, true }
	if got := ActiveState("CUT00001"); got != Unknown {
		t.Fatalf("an unreadable record written since boot: %v, want Unknown", got)
	}
	before := boot.Add(-time.Minute)
	if err := os.Chtimes(p, before, before); err != nil {
		t.Fatal(err)
	}
	if got := ActiveState("CUT00001"); got != Gone {
		t.Fatalf("an unreadable record from before this boot: %v, want Gone", got)
	}
	bootTimeFn = func() (time.Time, bool) { return time.Time{}, false }
	if got := ActiveState("CUT00001"); got != Unknown {
		t.Fatalf("no boot time to go by: %v, want Unknown", got)
	}
}
