// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"fmt"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"

	"reminal/internal/crypto"
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

// A hot restart while a program is on the alternate screen (Claude Code's
// full-screen mode, vim): the dump carries the main screen's history, so
// the restarted session has its scrollback. The terminal itself has no
// history to format on the alternate screen.
func TestADumpWrittenOnTheAltScreenCarriesTheHistory(t *testing.T) {
	key, _ := crypto.NewSessionKey()
	box, _ := crypto.NewBox(key)
	a := handoverAgent(t, box)
	for i := 0; i < 40; i++ {
		a.record([]byte(fmt.Sprintf("before-%02d some words\r\n", i)))
	}
	a.record([]byte("\x1b[?1049h\x1b[2J\x1b[Hfull screen program\x1b[?1000h"))
	path, k, err := a.writeScrollbackDump()
	if err != nil || path == "" {
		t.Fatalf("dump: %q %v", path, err)
	}
	d := takeScrollbackDump(path, k, a.sessionID)
	if d == nil {
		t.Fatal("no dump")
	}
	// 40 lines on 30 rows: the first 10 or so scrolled off the main screen.
	joined := strings.Join(d.History, "\n")
	if !strings.Contains(joined, "before-00") || !strings.Contains(joined, "before-09") {
		t.Errorf("the dump's history lost the main screen's lines on the alt screen: %d lines:\n%s", len(d.History), joined)
	}
	if strings.Contains(joined, "full screen program") {
		t.Errorf("the alt screen's content is in the history: %q", joined)
	}
}

// The switch to the alternate screen is found among other modes ("ESC[?1049;
// 2004h", as programs combine them) and split across two writes, as a PTY
// read may cut it: the main screen's history is held either way.
func TestTheSwitchToTheAltScreenIsFoundCombinedOrSplit(t *testing.T) {
	for name, writes := range map[string][]string{
		"combined": {"\x1b[?1049;2004h\x1b[2J\x1b[Hhello from vim"},
		"split":    {"\x1b[?10", "49h\x1b[2J\x1b[Hhello from vim"},
	} {
		s := testScreen(t, 40, 5)
		for i := 1; i <= 10; i++ {
			_, _ = s.Write([]byte(fmt.Sprintf("line %d\r\n", i)))
		}
		before := len(s.History())
		for _, w := range writes {
			_, _ = s.Write([]byte(w))
		}
		if !s.IsAltScreen() {
			t.Fatalf("%s: not on the alternate screen", name)
		}
		if got := len(s.History()); got != before || got == 0 {
			t.Errorf("%s: %d history lines on the alternate screen, want %d", name, got, before)
		}
	}
}
