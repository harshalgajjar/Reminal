// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

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
	// Pay any Resize-owed shim first — this test is an explicit app clear,
	// not the WriteClearScreen echo resizeScreen guards against.
	a.conhostClear.reset()
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
// history. A Resize is owed that one WriteClearScreen, so we drop it
// (even when ConPTY splits it) and the lines just moved into scrollback stay.
func TestRecordKeepsScrollbackWhenResizeEmitsED3(t *testing.T) {
	a := clearingAgent(t, 80, 24)
	a.record(fillerLines("HIST", 40))
	a.resizeScreen(80, 16)
	a.conhostClear.reset()
	a.conhostClear.arm()
	a.record([]byte("\x1b[H\x1b[2J\x1b[3J"))
	if n := a.screen.Scrollback().Len(); n == 0 {
		t.Fatal("ESC[3J during the post-resize window wiped scrollback")
	}
	if lastNonBlankRow(screenRows(a)) < 0 {
		t.Fatal("ESC[2J during the post-resize window wiped the screen")
	}
}

func TestConhostClearFilterStreaming(t *testing.T) {
	const shim = "\x1b[H\x1b[2J\x1b[3J"
	feed := func(chunks ...string) string {
		var f conhostClearFilter
		f.arm()
		var b strings.Builder
		for _, c := range chunks {
			b.Write(f.feed([]byte(c)))
		}
		return b.String()
	}

	t.Run("full shim in one chunk", func(t *testing.T) {
		if got := feed("before" + shim + "after"); got != "beforeafter" {
			t.Errorf("got %q, want beforeafter", got)
		}
	})
	t.Run("ED2+ED3", func(t *testing.T) {
		if got := feed("x\x1b[2J\x1b[3Jy"); got != "xy" {
			t.Errorf("got %q, want xy", got)
		}
	})
	t.Run("untouched", func(t *testing.T) {
		if got := feed("plain"); got != "plain" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("split one byte at a time", func(t *testing.T) {
		chunks := make([]string, 0, 2+len(shim))
		chunks = append(chunks, "keep-")
		for i := 0; i < len(shim); i++ {
			chunks = append(chunks, shim[i:i+1])
		}
		chunks = append(chunks, "-going")
		if got := feed(chunks...); got != "keep--going" {
			t.Errorf("got %q, want keep--going", got)
		}
	})
	t.Run("CUP then ED2+ED3", func(t *testing.T) {
		if got := feed("a\x1b[H", "\x1b[2J\x1b[3Jb"); got != "ab" {
			t.Errorf("got %q, want ab", got)
		}
	})
	t.Run("ED2 then ED3", func(t *testing.T) {
		if got := feed("a\x1b[2J", "\x1b[3Jb"); got != "ab" {
			t.Errorf("got %q, want ab", got)
		}
	})
	t.Run("false prefix is released", func(t *testing.T) {
		if got := feed("a\x1b[", "31mb"); got != "a\x1b[31mb" {
			t.Errorf("got %q, want a ESC[31mb", got)
		}
	})
	t.Run("unarmed passes the shim through", func(t *testing.T) {
		var f conhostClearFilter
		got := string(f.feed([]byte("x" + shim + "y")))
		if got != "x"+shim+"y" {
			t.Errorf("unarmed stripped: got %q", got)
		}
	})
	t.Run("genuine clear after consume still lands", func(t *testing.T) {
		var f conhostClearFilter
		f.arm()
		if got := string(f.feed([]byte("a" + shim + "b"))); got != "ab" {
			t.Fatalf("first shim: got %q, want ab", got)
		}
		if got := string(f.feed([]byte("c" + shim + "d"))); got != "c"+shim+"d" {
			t.Errorf("second clear was eaten: got %q", got)
		}
	})
	t.Run("two resizes two shims", func(t *testing.T) {
		var f conhostClearFilter
		f.arm()
		f.arm()
		if got := string(f.feed([]byte(shim + shim + "xy"))); got != "xy" {
			t.Errorf("got %q, want xy", got)
		}
	})
	t.Run("one resize drains a burst of two shims", func(t *testing.T) {
		// Keyboard collapse changes cols and rows together. ConPTY
		// emits WriteClearScreen once per axis; consecutive copies
		// are one opcode, not two Clears.
		var f conhostClearFilter
		f.arm()
		if got := string(f.feed([]byte(shim + shim + "keep"))); got != "keep" {
			t.Errorf("got %q, want keep", got)
		}
	})
	t.Run("gap after echo is an application clear", func(t *testing.T) {
		var f conhostClearFilter
		f.arm()
		if got := string(f.feed([]byte(shim + "keep" + shim))); got != "keep"+shim {
			t.Errorf("got %q, want keep+shim", got)
		}
	})
	t.Run("unarmed ED3 is still reminal history", func(t *testing.T) {
		var f conhostClearFilter
		f.forceED3 = true
		if got := string(f.feed([]byte("x\x1b[3Jy"))); got != "xy" {
			t.Errorf("got %q, want xy", got)
		}
		if got := string(f.feed([]byte("a\x1b[H\x1b[2Jb"))); got != "a\x1b[H\x1b[2Jb" {
			t.Errorf("ED2 of Clear-Host was eaten: got %q", got)
		}
	})
	t.Run("leftover then burst then prompt", func(t *testing.T) {
		var f conhostClearFilter
		f.arm()
		if got := string(f.feed([]byte("QR-LINE"))); got != "QR-LINE" {
			t.Fatalf("leftover: got %q", got)
		}
		if got := string(f.feed([]byte(shim + shim + "PS>"))); got != "PS>" {
			t.Errorf("burst: got %q, want PS>", got)
		}
		if got := string(f.feed([]byte("c" + shim + "d"))); got != "c"+shim+"d" {
			t.Errorf("later clear was eaten: got %q", got)
		}
	})
}

// TestGrowThenConhostClearKeepsBottomAnchor is the keyboard-collapse case
// on Windows: grow pulls history onto the screen, then conhost emits the
// WriteClearScreen shim. ED2 used to wipe that screen and leave the prompt
// stuck at the top of a tall grid with a void under it.
func TestGrowThenConhostClearKeepsBottomAnchor(t *testing.T) {
	a := clearingAgent(t, 80, 10)
	feedLines(a, 30)
	const grown = 20
	a.resizeScreen(80, grown)

	wantLast := lastNonBlankRow(screenRows(a))
	if wantLast != grown-2 {
		t.Fatalf("setup: last content row %d after grow, want %d", wantLast, grown-2)
	}
	sb := a.screen.Scrollback().Len()
	a.conhostClear.reset()
	a.conhostClear.arm()
	a.record([]byte("\x1b[H\x1b[2J\x1b[3J"))

	rows := screenRows(a)
	if got := lastNonBlankRow(rows); got != wantLast {
		t.Errorf("conhost shim after grow moved last content from %d to %d", wantLast, got)
	}
	if rows[wantLast] != "line-0030" {
		t.Errorf("row %d is %q, want line-0030 — ED2 undid the bottom-anchor", wantLast, rows[wantLast])
	}
	if n := a.screen.Scrollback().Len(); n != sb {
		t.Errorf("conhost shim changed scrollback from %d to %d", sb, n)
	}
}

// TestRecordStripsSplitConhostClear is why a timer-gated ReplaceAll on one
// chunk was not enough: ConPTY delivers WriteClearScreen across Read
// calls. CUP home, then ED2, then ED3 must not reach the emulator.
func TestRecordStripsSplitConhostClear(t *testing.T) {
	a := clearingAgent(t, 80, 10)
	feedLines(a, 30)
	const grown = 20
	a.resizeScreen(80, grown)
	wantLast := lastNonBlankRow(screenRows(a))
	sb := a.screen.Scrollback().Len()
	a.conhostClear.reset()
	a.conhostClear.arm()
	for _, b := range []byte("\x1b[H\x1b[2J\x1b[3J") {
		a.record([]byte{b})
	}
	rows := screenRows(a)
	if got := lastNonBlankRow(rows); got != wantLast {
		t.Errorf("split shim moved last content from %d to %d", wantLast, got)
	}
	if n := a.screen.Scrollback().Len(); n != sb {
		t.Errorf("split shim changed scrollback from %d to %d", sb, n)
	}
}

// TestRecordInfoThenGrowKeepsBanner is the tablet sequence: reminal info
// on the keyboard-open (short) screen, header scrolls off, then keyboard
// collapse grows cols+rows and ConPTY emits a burst of WriteClearScreen.
// The second ED3 used to wipe the header while leaving the QR.
func TestRecordInfoThenGrowKeepsBanner(t *testing.T) {
	a := clearingAgent(t, 80, 12)
	a.record([]byte("  reminal — remote terminal\r\n"))
	a.record([]byte("  Session:  CX2DN9RU\r\n"))
	a.record([]byte("  PIN:      664772\r\n"))
	a.record([]byte("  Scan to join from your phone:\r\n"))
	a.record(fillerLines("QR", 24))
	a.resizeScreen(80, 40)
	a.conhostClear.reset()
	a.conhostClear.arm()
	const shim = "\x1b[H\x1b[2J\x1b[3J"
	a.record([]byte(shim + shim))

	frm, _ := a.snapshotFrame()
	view := snapshotText(t, a, frm)
	for _, want := range []string{"remote terminal", "Session:  CX2DN9RU", "PIN:      664772", "Scan to join"} {
		if !strings.Contains(view, want) {
			t.Errorf("keyboard-collapse burst wiped %q from history", want)
		}
	}
}

// TestWindowsTranscriptED3NeverClearsHistory is the product model: ConPTY's
// CSI 3 J is the cls shim asking the connected terminal to forget a
// scrollback ConPTY does not have. Reminal's session log is not that
// buffer. ED 2 still clears the screen (Clear-Host, cls).
func TestWindowsTranscriptED3NeverClearsHistory(t *testing.T) {
	a := clearingAgent(t, 80, 24)
	a.record(fillerLines("HIST", 40))
	if a.screen.Scrollback().Len() == 0 {
		t.Fatal("setup: expected scrollback")
	}
	a.conhostClear.reset()
	a.conhostClear.forceED3 = true
	a.record([]byte("\x1b[H\x1b[2J\x1b[3J"))
	if n := a.screen.Scrollback().Len(); n == 0 {
		t.Fatal("CSI 3 J wiped reminal history")
	}
	if lastNonBlankRow(screenRows(a)) >= 0 {
		t.Error("ED 2 should still have cleared the screen")
	}
	frm, _ := a.snapshotFrame()
	view := snapshotText(t, a, frm)
	if !strings.Contains(view, "HIST-0001") {
		t.Error("snapshot lost history that CSI 3 J must not delete")
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
