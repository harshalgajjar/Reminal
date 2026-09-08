// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build darwin

package client

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// vdisplayHeldByOther is the cheap question the census loop now asks BEFORE
// paying for a ~100ms osascript. Getting it wrong is expensive in both
// directions: a false positive means a lid-shut Mac never grows its virtual
// display, a false negative brings back the per-session spawn storm.
func TestVdisplayHeldByOther(t *testing.T) {
	writeLock := func(t *testing.T, body string) {
		t.Helper()
		p := vdisplayLockPath()
		if p == "" {
			t.Fatal("no lock path")
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("no lock file", func(t *testing.T) {
		isolateHome(t)
		if vdisplayHeldByOther() {
			t.Error("an absent lock read as held — no session would ever create a display")
		}
	})

	t.Run("our own pid is not another process", func(t *testing.T) {
		isolateHome(t)
		writeLock(t, strconv.Itoa(os.Getpid())+"\n")
		if vdisplayHeldByOther() {
			t.Error("we read our own lock as someone else's, so we would skip the census that tells us to stand down")
		}
	})

	t.Run("a dead pid does not hold anything", func(t *testing.T) {
		isolateHome(t)
		// A pid that cannot be running: the file survived a crash.
		writeLock(t, "999999\n")
		if vdisplayHeldByOther() {
			t.Error("a stale lock from a crashed process would strand the machine with no display")
		}
	})

	t.Run("a live other pid holds it", func(t *testing.T) {
		isolateHome(t)
		writeLock(t, "1\n") // launchd: always alive, never us
		if !vdisplayHeldByOther() {
			t.Error("a live holder was not detected, so every session would run the census anyway")
		}
	})

	t.Run("garbage is not a holder", func(t *testing.T) {
		isolateHome(t)
		for _, junk := range []string{"", "\n", "not-a-pid\n", "-3\n", "0\n"} {
			writeLock(t, junk)
			if vdisplayHeldByOther() {
				t.Errorf("%q read as a live holder", junk)
			}
		}
	})
}
