// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"testing"
	"time"

	"reminal/internal/session"
)

// A session killed from the viewer is forgotten even when its agent saves its
// restore record while it dies: once the agent is gone, the record is gone,
// whether it went on SIGTERM or had to be killed.
func TestASessionKilledFromTheViewerStaysForgotten(t *testing.T) {
	isolateHome(t)
	const id = "KILLED01"
	save := func() {
		t.Helper()
		if err := session.WriteRestore(session.Restore{ID: id, PIN: "123456", Cwd: t.TempDir(), SavedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	gone := func() bool { _, err := session.ReadRestore(id); return err != nil }

	for _, tc := range []struct {
		name     string
		termWins bool // it leaves on SIGTERM, before the grace runs out
	}{{"leaves on SIGTERM", true}, {"has to be killed", false}} {
		t.Run(tc.name, func(t *testing.T) {
			save()
			_ = session.ClearRestore(id) // killLocalSession, right after SIGTERM
			calls, killed := 0, false
			alive := func(int) bool {
				calls++
				if calls == 2 {
					save() // its save tick lands while it dies
				}
				if killed {
					return false
				}
				return !tc.termWins || calls < 4
			}
			kill := func(int) error { killed = true; return nil }
			forgetWhenGone(id, 4242, alive, kill, 300*time.Millisecond)
			if !gone() {
				t.Fatalf("the record its agent saved while dying is still there: the session comes back at the next login")
			}
			if killed == tc.termWins {
				t.Errorf("killed = %v, want %v", killed, !tc.termWins)
			}
		})
	}
}
