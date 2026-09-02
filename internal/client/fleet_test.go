// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"os"
	"testing"
	"time"

	"github.com/reminal/reminal/internal/session"
)

func TestCollectFleetIncludesLocalSessionMetadata(t *testing.T) {
	isolateHome(t)
	if err := session.WriteActive(session.Active{
		ID:        "TESTSESS",
		PID:       os.Getpid(),
		StartedAt: time.Now(),
		Kind:      session.KindShell,
		Name:      "demo",
		Cwd:       "/tmp/project",
		Title:     "npm run dev",
		Headless:  true,
		Viewers:   2,
	}); err != nil {
		t.Fatal(err)
	}

	fleet, err := CollectFleet("")
	if err != nil {
		t.Fatal(err)
	}
	if len(fleet) != 1 {
		t.Fatalf("expected this machine only, got %d", len(fleet))
	}
	m := fleet[0]
	if !m.Local || !m.Online {
		t.Fatalf("local machine should be online: %+v", m)
	}
	if m.ID == "" || m.Name == "" {
		t.Fatalf("missing machine identity: %+v", m)
	}
	if len(m.Sessions) != 1 {
		t.Fatalf("sessions = %+v", m.Sessions)
	}
	s := m.Sessions[0]
	if s.ID != "TESTSESS" || s.Name != "demo" || s.Cwd != "/tmp/project" || s.Title != "npm run dev" {
		t.Fatalf("session metadata: %+v", s)
	}
}
