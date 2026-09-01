// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/reminal/reminal/internal/crypto"
)

// clearingAgent builds a bare snapshot-capable agent, like the resize-segment tests.
func clearingAgent(t *testing.T, cols, rows int) *Agent {
	t.Helper()
	key, _ := crypto.NewSessionKey()
	box, _ := crypto.NewBox(key)
	a := &Agent{box: box, buf: newScrollback(8 << 20), scrollbackLines: 20000}
	a.screen = vt.NewEmulator(cols, rows)
	a.screen.Scrollback().SetMaxLines(20000)
	go io.Copy(io.Discard, a.screen)
	a.buf.SetBase(cols, rows)
	return a
}

// snapshotText decrypts a snapshot frame and replays it through a tall emulator,
// returning everything a joining viewer would end up with: scrollback + screen.
func snapshotText(t *testing.T, a *Agent, frm string) string {
	t.Helper()
	pt, err := a.box.Decrypt(frm)
	if err != nil {
		t.Fatalf("decrypt snapshot: %v", err)
	}
	dst := vt.NewEmulator(80, 24)
	dst.Scrollback().SetMaxLines(20000)
	go io.Copy(io.Discard, dst)
	dst.Write(pt)
	var b strings.Builder
	for _, ln := range dst.Scrollback().Lines() {
		b.WriteString(ln.Render())
		b.WriteByte('\n')
	}
	b.WriteString(dst.Render())
	return b.String()
}

func fillerLines(prefix string, n int) []byte {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		b.WriteString(fmt.Sprintf("%s-%04d some words on this line to give it a realistic count\r\n", prefix, i))
	}
	return []byte(b.String())
}

// TestSnapshotSurvivesScrollbackClear is the regression test for the Windows
// crash "panic: runtime error: slice bounds out of range [12:0]" in regionWords
// (via dropResizeRepaints → snapshotHistory → snapshotFrame). Resize segments
// hold ABSOLUTE scrollback indices; ESC[3J (erase scrollback) empties the
// scrollback under them, so the newest segment's start pointed past the end of
// the rendered history and the region slice inverted. On Windows this fires on
// every session: conhost's PowerShell shim emits ESC[H ESC[2J ESC[3J when the
// shell blanks its buffer on resize, so the first viewer to connect (a resize)
// killed the agent.
func TestSnapshotSurvivesScrollbackClear(t *testing.T) {
	a := clearingAgent(t, 80, 24)

	// Two resizes with output between them: the minimum for dropResizeRepaints
	// to do any work at all (it no-ops below two segments).
	a.record(fillerLines("BEFORE", 40))
	a.resizeScreen(76, 24)
	a.record(fillerLines("MIDDLE", 20))
	a.resizeScreen(80, 24)
	a.record(fillerLines("AFTER", 20))
	if len(a.resizeSegs) < 2 {
		t.Fatalf("setup: want at least 2 resize segments, got %d", len(a.resizeSegs))
	}

	// The clear: screen + scrollback wiped, exactly as conhost emits it.
	// Zero the post-resize ED3 window first — this test is an explicit
	// app clear, not the SIGWINCH echo resizeScreen guards against.
	a.ignoreED3Until = time.Time{}
	a.record([]byte("\x1b[H\x1b[2J\x1b[3J"))
	if n := a.screen.Scrollback().Len(); n != 0 {
		t.Fatalf("setup: scrollback should be empty after ESC[3J, got %d lines", n)
	}
	// Post-clear output, then a snapshot. Before the fix this panicked; the
	// panic needed only that a segment start outlive the lines it indexed.
	a.record(fillerLines("POST", 30))
	frm, _ := a.snapshotFrame()
	if frm == "" {
		t.Fatal("empty snapshot after a scrollback clear")
	}
	view := snapshotText(t, a, frm)
	if !strings.Contains(view, "POST-0030") {
		t.Error("snapshot lost the output written after the clear")
	}
	if strings.Contains(view, "BEFORE-0001") {
		t.Error("snapshot resurrected scrollback the app explicitly erased")
	}
	if len(a.resizeSegs) != 0 {
		t.Errorf("stale segments kept across a scrollback clear: %d", len(a.resizeSegs))
	}

	// And the agent keeps working: more resizes after the clear must rebuild
	// segments against the NEW scrollback, not the retired indices.
	a.resizeScreen(70, 24)
	a.record(fillerLines("TAIL", 20))
	a.resizeScreen(80, 24)
	a.record(fillerLines("TAIL2", 20))
	if frm, _ = a.snapshotFrame(); frm == "" {
		t.Fatal("empty snapshot after post-clear resizes")
	}
	if view = snapshotText(t, a, frm); !strings.Contains(view, "TAIL2-0020") {
		t.Error("snapshot lost output written after the post-clear resizes")
	}
}

// TestRecordKeepsScrollbackWhenResizeEmitsED3 is the Windows keyboard-open
// case: conhost emits ESC[3J on SIGWINCH, which is not the user clearing
// history. For a short window after resizeAnchoredBottom we drop that
// sequence so the lines just moved into scrollback stay there.
func TestRecordKeepsScrollbackWhenResizeEmitsED3(t *testing.T) {
	a := clearingAgent(t, 80, 24)
	a.record(fillerLines("HIST", 40))
	a.resizeScreen(80, 16)
	a.ignoreED3Until = time.Now().Add(time.Second)
	a.record([]byte("\x1b[H\x1b[2J\x1b[3J"))
	if n := a.screen.Scrollback().Len(); n == 0 {
		t.Fatal("ESC[3J during the post-resize window wiped scrollback")
	}
}

// TestRegionWordsInvertedRange pins the last line of defense: even handed a
// stale (from > to) range, regionWords returns nothing instead of panicking.
// The empty word stream contributes no corpus, so it can never cause a drop.
func TestRegionWordsInvertedRange(t *testing.T) {
	if got := regionWords(nil, 12, 0); got != nil {
		t.Errorf("regionWords(nil, 12, 0) = %v, want nil", got)
	}
	lines := []string{"alpha beta", "gamma delta"}
	if got := regionWords(lines, 2, 1); got != nil {
		t.Errorf("regionWords(lines, 2, 1) = %v, want nil", got)
	}
	if got := regionWords(lines, 5, 99); got != nil {
		t.Errorf("regionWords past the end = %v, want nil", got)
	}
	if got := regionWords(lines, 0, 1); len(got) != 2 {
		t.Errorf("regionWords(lines, 0, 1) = %v, want the first line's words", got)
	}
}

// TestGuardPanicConvertsToError pins the property the Windows crash needed: a
// panic in a connection pump becomes an error the reconnect loop can handle,
// not a dead process.
func TestGuardPanicConvertsToError(t *testing.T) {
	// Keep the recovered-panic log out of the developer's real ~/.reminal.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	err := guardPanic("test pump", func() error { panic("boom") })
	if err == nil {
		t.Fatal("panic was swallowed instead of surfacing as an error")
	}
	if !strings.Contains(err.Error(), "boom") || !strings.Contains(err.Error(), "test pump") {
		t.Errorf("error lost the panic value or the label: %v", err)
	}
	want := errors.New("normal failure")
	if got := guardPanic("test pump", func() error { return want }); !errors.Is(got, want) {
		t.Errorf("non-panic error not passed through: %v", got)
	}
	if got := guardPanic("test pump", func() error { return nil }); got != nil {
		t.Errorf("clean run returned %v", got)
	}
}
