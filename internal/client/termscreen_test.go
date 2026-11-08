// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"fmt"
	"math/rand"
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

// The line bound is in lines, whatever the width: a session at its 20,000
// lines of 180 columns narrowed to 60 (a phone joining) keeps them all,
// three rows each. With the terminal's own cap at 20,000 rows it kept 6,539
// lines, the oldest two thirds gone (measured on a recorded session).
func TestTheLineBoundHoldsThroughANarrowing(t *testing.T) {
	const n = 20000
	s := testScreen(t, 180, 50)
	s.SetMaxLines(n)
	filler := strings.Repeat("x", 170)
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "row-%05d %s\r\n", i, filler)
	}
	if _, err := s.Write([]byte(b.String())); err != nil {
		t.Fatal(err)
	}
	before := len(s.History())
	s.Resize(60, 50)
	hist := s.History()
	if len(hist) < before-50 || len(hist) > n {
		t.Fatalf("narrowed to 60: %d lines of %d kept (cap %d)", len(hist), before, n)
	}
	if !strings.HasPrefix(hist[0], "row-000") {
		t.Errorf("the oldest line is %q, want row-000…: the oldest lines went", hist[0][:min(len(hist[0]), 12)])
	}
}

// A height change is done as the viewer's xterm.js does it, since what the
// viewer shows is what the person sees and what the program redraws over.
// With the cursor above the bottom row (Claude Code's always is: its prompt
// and status rows sit below it): a shrink first drops the rows below the
// cursor, then scrolls the top into history, the cursor pinned to the
// bottom; a grow pulls the rows back out of history, the cursor ending on
// the bottom row. The terminal on its
// own kept the rows below the cursor on the shrink and added blank rows on
// the grow, so Claude's redraw after a phone keyboard closed left a stale
// copy of its frame in the history, which every join then showed twice.
// (a recorded control: 100 lines at 102x45, the cursor 5 rows up.)
func TestAHeightChangeIsDoneAsTheViewerDoesIt(t *testing.T) {
	s := testScreen(t, 102, 45)
	defer s.Close()
	var b strings.Builder
	for i := 1; i <= 100; i++ {
		fmt.Fprintf(&b, "L%03d\r\n", i)
	}
	b.WriteString("\x1b[5A") // the cursor 5 rows above the bottom, as Claude's
	s.Write([]byte(b.String()))
	// The first row and the last row with text (after "L100\r\n" the
	// bottom row is blank), and the cursor's row.
	first := func() (string, string, int) {
		rows := strings.Split(s.Render(), "\n")
		last := len(rows) - 1
		for last > 0 && strings.TrimSpace(stripANSI(rows[last])) == "" {
			last--
		}
		return strings.TrimSpace(rows[0]), strings.TrimSpace(rows[last]), s.CursorPosition().Y
	}
	if f, l, y := first(); f != "L057" || l != "L100" || y != 39 {
		t.Fatalf("setup: rows %s..%s, cursor row %d, want L057..L100 and 39", f, l, y)
	}
	s.Resize(102, 24)
	if f, l, y := first(); f != "L073" || l != "L096" || y != 23 {
		t.Errorf("shrunk to 24: rows %s..%s, cursor row %d; xterm.js: L073..L096, cursor 23 (the rows below the cursor dropped, then scrolled)", f, l, y)
	}
	s.Resize(102, 45)
	if f, l, y := first(); f != "L052" || l != "L096" || y != 44 {
		t.Errorf("grown to 45: rows %s..%s, cursor row %d; xterm.js: L052..L096, cursor 44 (the history pulled back down)", f, l, y)
	}
	if n := len(s.History()); n != 51 {
		t.Errorf("history is %d lines, xterm.js: 51", n)
	}
}

// A session's screen whose terminal failed (a call trapped, or ran past its
// memory or its time) is dropped, and a join is given the raw output instead,
// as with REMINAL_SNAPSHOT=0: kept, every join was painted a blank screen
// with no history.
func TestAFailedScreenIsDroppedAndAJoinFallsBack(t *testing.T) {
	a := colouredSession(t, 20, 0)
	if s, _ := a.snapshotFrame(); s == "" {
		t.Fatal("control: no snapshot from a working screen")
	}
	a.screen.t.Close() // as a trap or a deadline leaves it
	a.record([]byte("after\r\n"))
	if a.haveScreen() {
		t.Fatal("the failed screen is still in use")
	}
	if s, _ := a.snapshotFrame(); s != "" {
		t.Fatalf("a join was painted from a failed screen: %d bytes", len(s))
	}
	a.resizeScreen(100, 30) // and nothing reads it after
}

// The count of the history's lines a resize reads (HistoryLines, kept from
// the rows added since and carried across resizes) is what formatting all of
// it gives, through writes of every length, width and height changes both
// ways, a program that leaves rows under its cursor, and clears.
func TestTheHistoryLineCountMatchesTheHistory(t *testing.T) {
	resizes, carried := 0, 0
	for seed := int64(1); seed <= 12; seed++ {
		rng := rand.New(rand.NewSource(seed))
		s := testScreen(t, 80, 24)
		s.SetMaxLines(100000)
		full := func() int {
			out, _ := s.t.History()
			return len(splitRows(out))
		}
		word := func() string { return []string{"alpha", "be", "charlie", "delta", "e", "foxtrot", "golf"}[rng.Intn(7)] }
		for step := 0; step < 120; step++ {
			switch op := rng.Intn(10); {
			case op < 6:
				var b strings.Builder
				for n := rng.Intn(6) + 1; n > 0; n-- {
					for w := rng.Intn(40); w > 0; w-- {
						b.WriteString(word() + " ")
					}
					b.WriteString(fmt.Sprintf("L%d.%d\r\n", seed, step))
				}
				if rng.Intn(4) == 0 {
					b.WriteString("\x1b[A\x1b[A") // the cursor left above the bottom, as a TUI does
				}
				_, _ = s.Write([]byte(b.String()))
			case op < 9:
				s.Resize(20+rng.Intn(140), 8+rng.Intn(50))
				resizes++
				if s.count.valid {
					carried++
				}
			default:
				if rng.Intn(5) == 0 {
					s.ClearScrollback()
				}
			}
			if got, want := s.HistoryLines(), full(); got != want {
				t.Fatalf("seed %d step %d: the count says %d lines, the history holds %d", seed, step, got, want)
			}
		}
	}
	// Carried, not counted again, on most resizes: what makes it cheap.
	if carried*10 < resizes*8 {
		t.Errorf("the count was carried across %d of %d resizes", carried, resizes)
	}
	t.Logf("carried across %d of %d resizes", carried, resizes)
}
