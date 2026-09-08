// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build darwin

package client

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
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

// The census lease is what makes this change safe across versions, so its
// edges matter more than most. Each of these corresponds to a way the
// closed-lid display could silently stop existing.
func TestCensusLease(t *testing.T) {
	write := func(t *testing.T, body string, age time.Duration) {
		t.Helper()
		p := vdisplayCensusLeasePath()
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if age > 0 {
			old := time.Now().Add(-age)
			if err := os.Chtimes(p, old, old); err != nil {
				t.Fatal(err)
			}
		}
	}

	t.Run("no lease at all", func(t *testing.T) {
		isolateHome(t)
		// An OLD daemon writes no lease. A new session must NOT defer to it,
		// or nobody censuses and closed-lid mode dies silently.
		if censusHeldByOther() {
			t.Error("deferred with no claim present — an old daemon would strand the machine")
		}
	})

	t.Run("fresh lease from a live process", func(t *testing.T) {
		isolateHome(t)
		write(t, "1\n", 0) // launchd: alive, not us
		if !censusHeldByOther() {
			t.Error("a fresh claim was ignored, so every session censuses again")
		}
	})

	t.Run("abandoned lease is not honoured", func(t *testing.T) {
		isolateHome(t)
		write(t, "1\n", censusLeaseTTL+time.Second)
		if censusHeldByOther() {
			t.Error("a stale claim still held; a crashed owner would block takeover forever")
		}
	})

	t.Run("dead owner", func(t *testing.T) {
		isolateHome(t)
		write(t, "999999\n", 0)
		if censusHeldByOther() {
			t.Error("a dead pid held the census")
		}
	})

	t.Run("our own claim is not someone else's", func(t *testing.T) {
		isolateHome(t)
		write(t, strconv.Itoa(os.Getpid())+"\n", 0)
		if censusHeldByOther() {
			t.Error("we deferred to ourselves and would never census again")
		}
	})

	t.Run("claim then read back", func(t *testing.T) {
		isolateHome(t)
		claimCensusLease()
		b, err := os.ReadFile(vdisplayCensusLeasePath())
		if err != nil {
			t.Fatalf("claim wrote nothing: %v", err)
		}
		if strings.TrimSpace(string(b)) != strconv.Itoa(os.Getpid()) {
			t.Errorf("claim recorded %q, want our pid", strings.TrimSpace(string(b)))
		}
		// Our own claim must not make us defer to ourselves.
		if censusHeldByOther() {
			t.Error("after claiming, we read our own lease as another process")
		}
	})

	t.Run("garbage", func(t *testing.T) {
		isolateHome(t)
		for _, junk := range []string{"", "\n", "nope\n", "-1\n", "0\n"} {
			write(t, junk, 0)
			if censusHeldByOther() {
				t.Errorf("%q read as a live claim", junk)
			}
		}
	})
}

func TestReleaseCensusLeaseOnlyDropsOurOwn(t *testing.T) {
	isolateHome(t)
	p := vdisplayCensusLeasePath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}

	// Ours: released, so a survivor takes over on its next poll.
	claimCensusLease()
	releaseCensusLease()
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Error("our own claim survived release")
	}

	// A successor's: must NOT be removed, or we would strip the claim of the
	// process that took over from us and both would census.
	if err := os.WriteFile(p, []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	releaseCensusLease()
	if _, err := os.Stat(p); err != nil {
		t.Error("released someone else's claim on the way out")
	}
}
