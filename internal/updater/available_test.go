// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package updater

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The ordering the Host panel depends on: newest first, so the first entry is
// the one an upgrade lands on ("next") and the rest are the skipped middle.
func TestReleasesSinceOrdering(t *testing.T) {
	in := []Release{{Version: "3.5.5"}, {Version: "3.6.0"}, {Version: "3.5.7"}}
	// Same comparator the function uses.
	sortReleases(in)
	got := []string{in[0].Version, in[1].Version, in[2].Version}
	want := []string{"3.6.0", "3.5.7", "3.5.5"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func TestAvailableIsQuietWithoutACache(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no version-check.json here
	if v := Available("3.5.4"); v != "" {
		t.Errorf("Available with no cache = %q, want empty — the panel must not claim an update it has not seen", v)
	}
	// A dev build never claims one either.
	if v := Available("dev"); v != "" {
		t.Errorf("Available(dev) = %q, want empty", v)
	}
}

// An unversioned build compares as 0.0.0, so every release looks newer and the
// panel would announce "20 releases behind" on a developer's own machine.
func TestReleasesSinceRefusesDevBuilds(t *testing.T) {
	for _, v := range []string{"", "dev", "0.0.0"} {
		rels, err := ReleasesSince(context.Background(), v, 20)
		if err == nil {
			t.Errorf("version %q was compared against the release list and got %d releases", v, len(rels))
		}
		if len(rels) != 0 {
			t.Errorf("version %q returned %d releases", v, len(rels))
		}
	}
}

// The notes panel opens on the version you are running, so the filter behind
// it has to keep that release. The upgrade offer must go on refusing it — a
// host on the latest being told to upgrade to what it already has is the
// regression this pins down.
func TestReleaseFilterKeepsTheRunningVersion(t *testing.T) {
	if !atOrNewer("3.6.0", "v3.6.0") {
		t.Error("the running version was filtered out of its own release notes")
	}
	if !atOrNewer("3.5.7", "v3.6.0") {
		t.Error("a newer release was filtered out")
	}
	if atOrNewer("3.6.0", "v3.5.7") {
		t.Error("an older release was kept")
	}
	if newer("3.6.0", "v3.6.0") {
		t.Error("the upgrade offer treated the running version as an update")
	}
}

// stubFeed points the release feed at a local server returning these tags (as
// GitHub's list endpoint would), counting how often it is hit, and clears the
// memo so the test starts from nothing.
func stubFeed(t *testing.T, tags ...string) *int {
	t.Helper()
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		out := "["
		for i, tag := range tags {
			if i > 0 {
				out += ","
			}
			out += `{"tag_name":"` + tag + `","body":"notes for ` + tag + `","published_at":"2026-09-08T00:00:00Z"}`
		}
		_, _ = w.Write([]byte(out + "]"))
	}))
	t.Cleanup(srv.Close)
	old := releasesURL
	releasesURL = srv.URL
	t.Cleanup(func() { releasesURL = old })
	t.Setenv("REMINAL_WEB", "") // no criticality beacon to reach in a test
	resetFeed()
	t.Cleanup(resetFeed)
	return &hits
}

func resetFeed() {
	feedMu.Lock()
	feedVal, feedRead, feedErr, feedErrAt = nil, time.Time{}, nil, time.Time{}
	feedMu.Unlock()
	availMu.Lock()
	availRead = time.Time{}
	availMu.Unlock()
}

// The bug this design exists to prevent: the panel said "up to date" beside a
// "What's new · 2" that listed a newer version, because the offer and the
// notes were read from two different places with two different ages. They now
// come from one feed, so the offer IS the top of the notes.
func TestOfferAndNotesAreOneAnswer(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stubFeed(t, "v3.6.4", "v3.6.3", "v3.6.2")

	rels, err := ReleasesSince(context.Background(), "3.6.3", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 2 || rels[0].Version != "3.6.4" || !rels[1].Current {
		t.Fatalf("notes for a 3.6.3 host: %+v", rels)
	}
	if got := Available("3.6.3"); got != "3.6.4" {
		t.Fatalf("the notes list 3.6.4 but the offer says %q — the two disagree again", got)
	}
	rels, _ = ReleasesSince(context.Background(), "3.6.4", 20)
	if len(rels) != 1 || !rels[0].Current {
		t.Fatalf("notes for a current host: %+v", rels)
	}
	if got := Available("3.6.4"); got != "" {
		t.Fatalf("a current host was offered %q", got)
	}
}

// One fetch serves every consumer for feedTTL. Opening the Host panel asks; a
// viewer opening it in a loop — or a PIN guest sending the message by hand —
// must not become a stream of requests at GitHub.
func TestFeedIsMemoized(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	hits := stubFeed(t, "v3.6.4")
	for i := 0; i < 5; i++ {
		if _, err := ReleasesSince(context.Background(), "3.6.0", 20); err != nil {
			t.Fatal(err)
		}
		RefreshAvailable("3.6.0")
		_ = Available("3.6.0")
	}
	if *hits != 1 {
		t.Fatalf("five panel opens plus the daily check reached the network %d times; want 1", *hits)
	}
	got, _ := ReleasesSince(context.Background(), "3.6.0", 20)
	got[0].Version = "mutated"
	again, _ := ReleasesSince(context.Background(), "3.6.0", 20)
	if again[0].Version == "mutated" {
		t.Error("the memo handed out its own backing array — one caller can corrupt every later read")
	}
}

// A failure is remembered briefly so a broken network cannot become a request
// loop, and the previous good answer stays in place — an open panel must not
// lose an update it already knew about because of a momentary problem.
func TestFeedFailureKeepsThePreviousAnswer(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stubFeed(t, "v3.6.4")
	if _, err := ReleasesSince(context.Background(), "3.6.0", 20); err != nil {
		t.Fatal(err)
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad.Close()
	releasesURL = bad.URL
	feedMu.Lock()
	feedRead = time.Time{} // force a refetch
	feedMu.Unlock()

	first, err := ReleasesSince(context.Background(), "3.6.0", 20)
	if err == nil {
		t.Fatalf("a failed fetch reported success: %+v", first)
	}
	if got := Available("3.6.0"); got != "3.6.4" {
		t.Fatalf("a network blip erased the offer: %q", got)
	}
	// Within feedErrTTL the failure is served from memory: a cancelled context
	// proves no request is made, because the remembered error is returned
	// verbatim rather than a context error.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, again := ReleasesSince(ctx, "3.6.0", 20); again == nil || errors.Is(again, context.Canceled) {
		t.Fatalf("expected the remembered failure, got %v", again)
	}
}

// A machine whose sessions all run in the background has nobody to prompt, so
// the interactive check never runs there — but the Host panel reads the cache
// that a check writes. Without this the upgrade could not be offered at all on
// exactly the always-on hosts it is meant for.
func TestRefreshAvailablePopulatesAnEmptyCache(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, ok := readCache(); ok {
		t.Fatal("expected no cache to start from")
	}
	stubFeed(t, "v3.6.2")

	RefreshAvailable("3.6.0")

	entry, ok := readCache()
	if !ok || entry.LatestTag != "v3.6.2" {
		t.Fatalf("background refresh did not record the answer: %+v (ok=%v)", entry, ok)
	}
	if got := Available("3.6.0"); got != "3.6.2" {
		t.Fatalf("Available reported %q, so the panel would show nothing", got)
	}
}
