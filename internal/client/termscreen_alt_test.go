// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
)

// A full-screen program (vim, htop, Claude Code's own full-screen mode) on
// the alternate screen, with mouse tracking on as they turn it on: Render
// is the program's screen, the cursor where it put it, and a join paints
// that screen on the alternate buffer and the shell's history behind it.
// Back on the main screen, what was there is as it was.
func TestAFullScreenProgramOnTheAltScreen(t *testing.T) {
	s := testScreen(t, 40, 6)
	defer s.Close()
	for i := 1; i <= 8; i++ {
		s.Write([]byte("$ echo line-" + strings.Repeat("x", i) + "\r\nline-" + strings.Repeat("x", i) + "\r\n"))
	}
	mainBefore := s.Render()
	histBefore := len(s.History())

	// vim: alt screen, mouse tracking, a cleared screen with a line and a
	// status row, the cursor on row 1 col 3.
	s.Write([]byte("\x1b[?1049h\x1b[?1000h\x1b[?1006h\x1b[2J\x1b[H"))
	s.Write([]byte("hello from vim\r\n~\r\n~\r\n~\r\n~\r\n\x1b[7m-- INSERT --\x1b[0m\x1b[2;4H"))
	if !s.IsAltScreen() {
		t.Fatal("not on the alternate screen")
	}
	rows := strings.Split(s.Render(), "\n")
	if len(rows) != 6 || stripANSI(rows[0]) != "hello from vim" || !strings.Contains(stripANSI(rows[5]), "-- INSERT --") {
		t.Errorf("the alt screen renders as %q", rows)
	}
	if got, want := s.CursorPosition(), uv.Pos(3, 1); got != want {
		t.Errorf("the cursor is at %v, want %v", got, want)
	}
	if n := len(s.History()); n != histBefore {
		t.Errorf("the alt screen changed the history: %d lines, was %d", n, histBefore)
	}
	// A join: the screen is painted on the alternate buffer, after the
	// history, and the cursor set where the program has it.
	paint := paintSnapshot(viewOf(s), s.History(), nil, 0, false)
	if !strings.Contains(paint, "\x1b[?1049h") || !strings.Contains(paint, "hello from vim") || !strings.Contains(paint, "\x1b[2;4H") {
		t.Errorf("the join does not paint the program's screen on the alternate buffer: %q", paint)
	}
	if i, j := strings.Index(paint, "line-xxxxx\x1b"), strings.Index(paint, "\x1b[?1049h"); i < 0 || i > j {
		t.Errorf("the history is not painted before the alternate screen: %q", paint)
	}

	// Back to the shell: the main screen as it was, the mouse off.
	s.Write([]byte("\x1b[?1006l\x1b[?1000l\x1b[?1049l"))
	if s.IsAltScreen() {
		t.Fatal("still on the alternate screen")
	}
	if got := s.Render(); got != mainBefore {
		t.Errorf("the main screen came back as\n%q\nwas\n%q", got, mainBefore)
	}
	if n := len(s.History()); n != histBefore {
		t.Errorf("the history is %d lines, was %d", n, histBefore)
	}
}
