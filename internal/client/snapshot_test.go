// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/reminal/reminal/internal/crypto"
)

// TestRecordDoesNotBlockOnTerminalQueries guards the v0.11.0 regression where
// apps like `claude` froze inside a reminal session: the emulator replies to
// terminal queries (DA / cursor-position / DSR) by writing into an internal
// pipe, and with nothing draining it the next query made Write — and thus
// record() and the whole PTY pump — block forever. initScreen must start the
// drain so feeding queries never blocks.
func TestRecordDoesNotBlockOnTerminalQueries(t *testing.T) {
	t.Setenv("REMINAL_SNAPSHOT", "")
	key, _ := crypto.NewSessionKey()
	box, _ := crypto.NewBox(key)
	a := &Agent{box: box, buf: newScrollback(1 << 20)}
	a.initScreen()
	if a.screen == nil {
		t.Skip("snapshots disabled")
	}

	done := make(chan struct{})
	go func() {
		// Queries that make the emulator generate pipe replies, plus normal
		// output. Several, to be sure it's not just a one-deep pipe buffer.
		for i := 0; i < 50; i++ {
			a.record([]byte("\x1b[c"))  // primary device attributes
			a.record([]byte("\x1b[6n")) // cursor position report
			a.record([]byte("\x1b[5n")) // device status report
			a.record([]byte("line\r\n"))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("record() blocked on terminal-query replies — emulator pipe not drained")
	}
}

// TestAgentSnapshotFramePath exercises the whole agent-side path the way a
// fresh viewer triggers it: output is committed via record() (which feeds both
// the buffer and the emulator), then snapshotFrame() serializes screen +
// scrollback and encrypts it. It decrypts the frame and asserts the history is
// present — the regression guard for "the seq output is just not there".
func TestAgentSnapshotFramePath(t *testing.T) {
	key, err := crypto.NewSessionKey()
	if err != nil {
		t.Fatal(err)
	}
	box, err := crypto.NewBox(key)
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{box: box, buf: newScrollback(2 << 20), scrollbackLines: 10000}
	a.screen = vt.NewEmulator(80, 24)
	a.screen.Scrollback().SetMaxLines(10000)

	a.record([]byte("prompt$ seq 1 300\r\nSTART-OF-HISTORY\r\n"))
	for i := 1; i <= 300; i++ {
		a.record([]byte(fmt.Sprintf("%d\r\n", i)))
	}
	a.record([]byte("END-AT-BOTTOM\r\nprompt$ "))

	frame, seq := a.snapshotFrame()
	if frame == "" || seq == 0 {
		t.Fatalf("snapshotFrame returned empty (frame=%q seq=%d)", frame, seq)
	}
	pt, err := box.Decrypt(frame)
	if err != nil {
		t.Fatalf("decrypt snapshot: %v", err)
	}
	got := string(pt)
	// START-OF-HISTORY scrolled off the 24-row screen long ago, so its
	// presence proves scrollback is in the snapshot, not just the visible screen.
	for _, want := range []string{"START-OF-HISTORY", "END-AT-BOTTOM", "prompt$"} {
		if !strings.Contains(got, want) {
			t.Errorf("snapshot missing %q (len=%d)", want, len(got))
		}
	}

	// Feeding the snapshot into a fresh emulator must reproduce real scrollback.
	dst := vt.NewEmulator(80, 24)
	dst.Scrollback().SetMaxLines(10000)
	if _, err := dst.Write(pt); err != nil {
		t.Fatalf("replay snapshot: %v", err)
	}
	if dst.Scrollback().Len() == 0 {
		t.Fatal("reconstructed emulator has no scrollback")
	}
}

// TestAgentSnapshotDisabledFallsBack verifies that with no emulator (snapshots
// off) snapshotFrame is a no-op so the sender falls back to raw replay.
func TestAgentSnapshotDisabledFallsBack(t *testing.T) {
	key, _ := crypto.NewSessionKey()
	box, _ := crypto.NewBox(key)
	a := &Agent{box: box, buf: newScrollback(1 << 20)} // a.screen == nil
	if frame, seq := a.snapshotFrame(); frame != "" || seq != 0 {
		t.Fatalf("expected no snapshot with nil emulator, got frame=%q seq=%d", frame, seq)
	}
}

// roundTrip feeds output into a source emulator, snapshots it, replays the
// snapshot into a fresh emulator of the same size, and returns both so tests
// can assert the reconstruction matches.
func roundTrip(t *testing.T, w, h, sb int, output string) (src, dst *vt.Emulator) {
	t.Helper()
	src = vt.NewEmulator(w, h)
	src.Scrollback().SetMaxLines(sb)
	if _, err := src.Write([]byte(output)); err != nil {
		t.Fatalf("src write: %v", err)
	}
	snap := buildSnapshot(src, renderScrollback(src, sb), nil, 0, false)
	dst = vt.NewEmulator(w, h)
	dst.Scrollback().SetMaxLines(sb)
	if _, err := dst.Write([]byte(snap)); err != nil {
		t.Fatalf("dst write: %v", err)
	}
	return src, dst
}

func TestSnapshotScreenRoundTrip(t *testing.T) {
	src, dst := roundTrip(t, 40, 6, 1000,
		"line one\r\nline two\r\n\x1b[31mred\x1b[0m and \x1b[1;44mbold-bg\x1b[0m\r\nprompt$ ")
	if src.Render() != dst.Render() {
		t.Fatalf("screen mismatch:\n src=%q\n dst=%q", src.Render(), dst.Render())
	}
	if src.CursorPosition() != dst.CursorPosition() {
		t.Errorf("cursor mismatch: src=%v dst=%v", src.CursorPosition(), dst.CursorPosition())
	}
}

func TestSnapshotScrollbackRoundTrip(t *testing.T) {
	// Emit far more lines than the screen height so scrollback accumulates.
	var sb strings.Builder
	for i := 0; i < 50; i++ {
		fmt.Fprintf(&sb, "history line %d\r\n", i)
	}
	sb.WriteString("final$ ")
	src, dst := roundTrip(t, 30, 5, 1000, sb.String())

	if src.Render() != dst.Render() {
		t.Fatalf("screen mismatch:\n src=%q\n dst=%q", src.Render(), dst.Render())
	}
	// The reconstructed emulator should have real scrollback to scroll through.
	if dst.Scrollback().Len() == 0 {
		t.Fatalf("expected reconstructed scrollback, got none")
	}
}

func TestSnapshotAltScreenRoundTrip(t *testing.T) {
	// Some main-screen output, then a full-screen app on the alt buffer.
	src, dst := roundTrip(t, 24, 4, 1000,
		"shell history here\r\n\x1b[?1049h\x1b[2J\x1b[H\x1b[32mTUI\x1b[0m running\r\nstatus bar")
	if !src.IsAltScreen() {
		t.Fatal("precondition: source should be on alt screen")
	}
	if !dst.IsAltScreen() {
		t.Error("reconstructed emulator should be on the alt screen")
	}
	if src.Render() != dst.Render() {
		t.Fatalf("alt screen mismatch:\n src=%q\n dst=%q", src.Render(), dst.Render())
	}
}

// TestSnapshotByteCapTrimsOldest verifies the byte budget drops the oldest
// scrollback while keeping the newest — so a huge history can't balloon the
// snapshot even under a high line cap.
func TestSnapshotByteCapTrimsOldest(t *testing.T) {
	e := vt.NewEmulator(40, 4)
	e.Scrollback().SetMaxLines(10000)
	for i := 1; i <= 500; i++ {
		e.Write([]byte(fmt.Sprintf("LINE-%04d\r\n", i)))
	}
	// Generous line cap, tiny byte cap → only the newest scrollback survives.
	snap := buildSnapshot(e, renderScrollback(e, 10000), nil, 200, false)

	if !strings.Contains(snap, "LINE-0495") {
		t.Errorf("expected newest history (LINE-0495) within the byte cap")
	}
	if strings.Contains(snap, "LINE-0050") {
		t.Errorf("expected old history (LINE-0050) to be trimmed by the byte cap")
	}
	// Snapshot stays small (history bounded by ~200 bytes + the 4-row screen).
	if len(snap) > 2000 {
		t.Errorf("snapshot exceeded expected size under byte cap: %d bytes", len(snap))
	}
}
