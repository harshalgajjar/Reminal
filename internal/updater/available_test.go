// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package updater

import (
	"context"
	"errors"
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

// The fetch is triggered by anyone opening "What's new", and GitHub allows 60
// unauthenticated calls an hour. Without a cache a viewer reopening the sheet
// could exhaust the host's quota for everyone.
func TestReleasesSinceServesFromCache(t *testing.T) {
	relMu.Lock()
	relFor, relVal, relRead, relErr = "9.9.9", []Release{{Version: "9.9.10"}, {Version: "9.9.11"}}, time.Now(), nil
	relMu.Unlock()
	t.Cleanup(func() {
		relMu.Lock()
		relFor, relVal, relRead, relErr = "", nil, time.Time{}, nil
		relMu.Unlock()
	})

	// A cancelled context proves no network call happens: a cache miss would
	// fail immediately, a hit ignores the context entirely.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := ReleasesSince(ctx, "9.9.9", 20)
	if err != nil {
		t.Fatalf("a cached answer still hit the network: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("cache returned %d releases, want 2", len(got))
	}
	// The limit must apply to the cached copy too.
	if one, _ := ReleasesSince(ctx, "9.9.9", 1); len(one) != 1 {
		t.Errorf("limit ignored on a cache hit: got %d", len(one))
	}
	// And the caller must not be handed the cache's own slice to mutate.
	got[0].Version = "mutated"
	again, _ := ReleasesSince(ctx, "9.9.9", 20)
	if again[0].Version == "mutated" {
		t.Error("the cache handed out its own backing array — one caller can corrupt every later read")
	}

	// A different version must not be served the previous one's answer.
	if _, err := ReleasesSince(ctx, "9.9.8", 20); err == nil {
		t.Error("a different version was served from another version's cache")
	}
}

// A failure is remembered briefly so a broken network cannot become a request
// loop, but not so long that a transient outage sticks.
func TestReleasesSinceCachesFailuresBriefly(t *testing.T) {
	relMu.Lock()
	relFor, relErr, relErrAt, relRead = "9.9.9", errors.New("boom"), time.Now(), time.Time{}
	relMu.Unlock()
	t.Cleanup(func() {
		relMu.Lock()
		relFor, relErr, relErrAt = "", nil, time.Time{}
		relMu.Unlock()
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ReleasesSince(ctx, "9.9.9", 20); err == nil || err.Error() != "boom" {
		t.Errorf("a cached failure was not reused: %v", err)
	}
	// Past the window it must try again (and fail on the dead context, proving
	// it reached the network rather than the memo).
	relMu.Lock()
	relErrAt = time.Now().Add(-releaseErrTTL - time.Second)
	relMu.Unlock()
	if _, err := ReleasesSince(ctx, "9.9.9", 20); err == nil || err.Error() == "boom" {
		t.Errorf("a stale failure was still being served: %v", err)
	}
}
