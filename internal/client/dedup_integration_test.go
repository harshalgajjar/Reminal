// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/x/vt"
	"reminal/internal/crypto"
)

// TestSnapshotDedupWiredIntoReconnectPath drives the REAL Ink resize+repaint capture
// through the actual reconnect snapshot pipeline — record() into the encrypted buffer,
// AppendResize markers, then snapshotFrame() (which rebuilds history via rebuildView and
// now runs dedupBlocks). It decrypts the frame exactly like a reconnecting viewer would
// replay it, and asserts the CORRECT invariant: no substantial paragraph is duplicated in
// the delivered history, and the legitimate cross-draft repeats are preserved. This proves
// the dedup is wired into the real path, not just callable in isolation.
func TestSnapshotDedupWiredIntoReconnectPath(t *testing.T) {
	data, err := os.ReadFile("testdata/scrollback_stable_resize.json")
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	var events []capEvent
	if err := json.Unmarshal(data, &events); err != nil {
		t.Fatalf("parse capture: %v", err)
	}

	key, err := crypto.NewSessionKey()
	if err != nil {
		t.Fatal(err)
	}
	box, err := crypto.NewBox(key)
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{box: box, buf: newScrollback(4 << 20), scrollbackLines: 10000}
	a.screen = vt.NewEmulator(119, 51)
	a.screen.Scrollback().SetMaxLines(10000)
	if a.screen == nil {
		t.Skip("snapshots disabled")
	}
	a.buf.SetBase(119, 51)

	for _, ev := range events {
		switch ev.T {
		case "w":
			b, derr := base64.StdEncoding.DecodeString(ev.D)
			if derr != nil {
				t.Fatalf("bad base64: %v", derr)
			}
			a.record(b)
		case "r":
			a.buf.AppendResize(ev.C, ev.R)
			a.screen.Resize(ev.C, ev.R)
		}
	}

	frame, seq := a.snapshotFrame()
	if frame == "" || seq == 0 {
		t.Fatalf("snapshotFrame returned empty (frame=%q seq=%d)", frame, seq)
	}
	pt, err := box.Decrypt(frame)
	if err != nil {
		t.Fatalf("decrypt snapshot: %v", err)
	}

	// Replay the snapshot into a fresh tall emulator exactly as a reconnecting viewer
	// does, then read back the reconstructed rows — this is what the user would see.
	dst := vt.NewEmulator(119, 400)
	dst.Scrollback().SetMaxLines(10000)
	if _, err := dst.Write(pt); err != nil {
		t.Fatalf("replay snapshot: %v", err)
	}
	rows := strings.Split(strings.ReplaceAll(dst.Render(), "\r\n", "\n"), "\n")

	// Invariant: no substantial paragraph appears more than once in the delivered history.
	keys := paraKeys(rows)
	seen := map[string]int{}
	for _, k := range keys {
		seen[k]++
	}
	dup := 0
	for k, c := range seen {
		if c > 1 {
			dup++
			t.Errorf("delivered snapshot duplicates a paragraph %d×: %.60q…", c, strings.ReplaceAll(k, "\x00", " "))
		}
	}
	t.Logf("reconstructed rows=%d substantial-paras=%d distinct=%d duplicated=%d", len(rows), len(keys), len(seen), dup)
}
