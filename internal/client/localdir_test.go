// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"os"
	"testing"
	"time"

	"reminal/internal/session"
)

// TestLocalDirectoryReadsRegistry verifies that LocalDirectory surfaces the
// machine's own live sessions straight from the local registry — the path
// `reminal machines` uses so the machine you're on always shows up without a
// relay round-trip or being enrolled as an owner of itself.
func TestLocalDirectoryReadsRegistry(t *testing.T) {
	isolateHome(t)

	if err := session.WriteActive(session.Active{
		ID:        "TESTSESS",
		PID:       os.Getpid(), // our own PID is alive, so it isn't pruned
		StartedAt: time.Now(),
		Kind:      session.KindShell,
		Name:      "demo",
	}); err != nil {
		t.Fatal(err)
	}

	d := LocalDirectory()
	if len(d.Sessions) != 1 {
		t.Fatalf("expected 1 local session, got %d", len(d.Sessions))
	}
	if d.Sessions[0].ID != "TESTSESS" || d.Sessions[0].Name != "demo" {
		t.Fatalf("wrong session surfaced: %+v", d.Sessions[0])
	}
}
