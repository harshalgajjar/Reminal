// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !windows

package client

import (
	"os"
	"path/filepath"
	"testing"

	"reminal/internal/session"
)

// A session's first save over a record that will not open keeps its .conv
// and scrollback where they are: a live session's saves only look at the
// record before them (PeekRestore: what it held, whether a turn was seen,
// since when its conversation), never quarantine it, which moved both away
// with it, and the lost-agent note needs the .conv. The save then writes
// the session's own record over the bad one.
func TestAFirstSaveOverAnUnreadableRecordKeepsItsConversation(t *testing.T) {
	home := isolateHome(t)
	const id = "PEEK0001"
	const conv = "a1b2c3d4-0001-4e5f-8a9b-0123456789ab"
	dir := filepath.Join(home, ".reminal", "restore")
	if err := session.WriteRestore(session.Restore{ID: id, PIN: "123456", Fg: "claude", Conv: conv}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".sealed"), []byte("not a sealed record"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := session.WriteConv(id, conv); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".scrollback.sealed"), []byte("scrollback"), 0o600); err != nil {
		t.Fatal(err)
	}

	a := quietShell(t, `printf '$ '`)
	a.sessionID, a.pin = id, "123456"
	a.saveRestore()

	if got := session.ReadConv(id); got != conv {
		t.Errorf(".conv = %q after the first save, want %q: the conversation was taken away", got, conv)
	}
	if _, err := os.Stat(filepath.Join(dir, id+".scrollback.sealed")); err != nil {
		t.Errorf("the scrollback is gone after the first save: %v", err)
	}
	if ents, _ := os.ReadDir(filepath.Join(dir, "quarantine")); len(ents) > 0 {
		t.Errorf("the first save quarantined %d file(s)", len(ents))
	}
}
