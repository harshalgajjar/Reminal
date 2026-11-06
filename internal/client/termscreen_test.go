// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"strings"
	"testing"
)

// testScreen is a session's screen for a test, freed when it ends.
func testScreen(t testing.TB, cols, rows int) *sessionScreen {
	t.Helper()
	s, err := newSessionScreen(cols, rows, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

// The session's screen keeps what scrolled off whole through any resize, and
// Render is always as many rows as it is tall.
func TestSessionScreenKeepsItsHistoryWholeThroughResizes(t *testing.T) {
	s := testScreen(t, 100, 8)
	for i := 0; i < 20; i++ {
		s.Write([]byte("row " + strings.Repeat("word ", 30) + "end\r\n"))
	}
	for _, size := range [][2]int{{40, 8}, {100, 12}, {30, 5}} {
		s.Resize(size[0], size[1])
		if got := len(strings.Split(s.Render(), "\n")); got != size[1] {
			t.Errorf("at %dx%d: Render has %d rows", size[0], size[1], got)
		}
		h := s.History()
		// Its last line may go on in the screen's first row.
		for _, ln := range h[:max(len(h)-1, 0)] {
			plain := stripANSI(ln)
			if strings.HasPrefix(plain, "row ") && !strings.HasSuffix(strings.TrimSpace(plain), "end") {
				t.Errorf("at %dx%d: a history line is cut: %q", size[0], size[1], plain)
			}
		}
	}
	gen := s.Gen()
	_ = s.History()
	if s.Gen() != gen {
		t.Error("reading it changed it")
	}
}

// historyCapped is s's history, its newest n lines (all of it for n <= 0).
func historyCapped(s *sessionScreen, n int) []string {
	h := s.History()
	if n > 0 && len(h) > n {
		h = h[len(h)-n:]
	}
	return h
}
