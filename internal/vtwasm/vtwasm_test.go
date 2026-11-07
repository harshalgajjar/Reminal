// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package vtwasm

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
)

// The module kept in the tree is the one scripts/ghostty-vt/build.sh builds
// and records.
func TestModuleIsTheRecordedBuild(t *testing.T) {
	rec, err := os.ReadFile("ghostty-vt.wasm.sha256")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(module)
	if got, want := hex.EncodeToString(sum[:]), strings.Fields(string(rec))[0]; got != want {
		t.Fatalf("embedded module %s, recorded %s: run scripts/ghostty-vt/build.sh", got, want)
	}
}

func newTerm(t *testing.T, cols, rows int) *Terminal {
	t.Helper()
	v, err := New(cols, rows, 20000)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(v.Close)
	return v
}

// longLines writes n lines wider than a phone, each with a marker at both
// ends.
func longLines(v *Terminal, n int) {
	for i := 1; i <= n; i++ {
		fmt.Fprintf(v, "L%02d alpha bravo charlie delta echo foxtrot golf hotel india juliet kilo lima mike november oscar papa END%02d\r\n", i, i)
	}
}

// lost counts the lines of longLines whose start or end is not in s.
func lost(s string, n int) int {
	k := 0
	for i := 1; i <= n; i++ {
		if !strings.Contains(s, fmt.Sprintf("L%02d alpha", i)) || !strings.Contains(s, fmt.Sprintf("papa END%02d", i)) {
			k++
		}
	}
	return k
}

var sgr = regexp.MustCompile(`\x1b\[[0-9;:]*[A-Za-z]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)`)

// A narrower screen re-wraps what scrolled off, as a terminal's own resize
// does: no line loses a word, and a wider one takes it back.
func TestResizeLosesNothing(t *testing.T) {
	v := newTerm(t, 120, 10)
	longLines(v, 30)
	for _, size := range [][2]int{{50, 10}, {120, 10}, {40, 30}, {180, 8}} {
		if err := v.Resize(size[0], size[1]); err != nil {
			t.Fatal(err)
		}
		if c, r := v.Size(); c != size[0] || r != size[1] {
			t.Fatalf("size %dx%d, want %dx%d", c, r, size[0], size[1])
		}
		text, err := v.Text()
		if err != nil {
			t.Fatal(err)
		}
		if n := lost(text, 30); n != 0 {
			t.Errorf("at %dx%d: %d of 30 lines lost", size[0], size[1], n)
		}
	}
}

// History is what scrolled off, a line each as written (its wrapped rows
// joined), with its styles; Screen is the rest, a row each. Together they
// are all of it, nothing twice.
func TestHistoryAndScreenSplitWhatItHolds(t *testing.T) {
	v := newTerm(t, 50, 10)
	fmt.Fprint(v, "\x1b[31mred\x1b[0m start\r\n")
	longLines(v, 30)
	hist, err := v.History()
	if err != nil {
		t.Fatal(err)
	}
	screen, err := v.Screen()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(hist, "\x1b[") || !strings.Contains(sgr.ReplaceAllString(hist, ""), "red start") {
		t.Errorf("history lacks its colour or first line: %q", hist[:min(80, len(hist))])
	}
	plainHist := sgr.ReplaceAllString(hist, "")
	for _, ln := range strings.Split(strings.TrimRight(plainHist, "\r\n"), "\n") {
		ln = strings.TrimRight(ln, "\r")
		if strings.HasPrefix(ln, "L") && !strings.Contains(ln, "END") {
			t.Errorf("a history line is not whole: %q", ln)
		}
	}
	rows := strings.Split(strings.TrimRight(sgr.ReplaceAllString(screen, ""), "\r\n"), "\n")
	if len(rows) > 10 {
		t.Errorf("screen has %d rows, want at most 10", len(rows))
	}
	// Every line's start and end is in one or the other (a line the
	// screen's top cut in two has its start above and its end below).
	all := plainHist + "\n" + strings.Join(rows, "\n")
	for i := 1; i <= 30; i++ {
		if !strings.Contains(all, fmt.Sprintf("L%02d alpha", i)) || !strings.Contains(all, fmt.Sprintf("END%02d", i)) {
			t.Errorf("L%02d in neither history nor screen", i)
		}
	}
	for i := 1; i <= 30; i++ {
		m := fmt.Sprintf("L%02d alpha", i)
		if strings.Contains(plainHist, m) && strings.Contains(sgr.ReplaceAllString(screen, ""), m) {
			t.Errorf("%s in both history and screen", m)
		}
	}
}

// The cursor, the alternate screen, and a reset.
func TestCursorAltScreenAndReset(t *testing.T) {
	v := newTerm(t, 80, 24)
	fmt.Fprint(v, "\x1b[5;10H")
	if x, y := v.Cursor(); x != 9 || y != 4 {
		t.Errorf("cursor %d,%d, want 9,4", x, y)
	}
	if v.AltScreen() {
		t.Error("alternate screen before one was asked for")
	}
	fmt.Fprint(v, "\x1b[?1049h")
	if !v.AltScreen() {
		t.Error("no alternate screen after ?1049h")
	}
	longLines(v, 40)
	v.Reset()
	if v.AltScreen() || v.ScrollbackRows() != 0 {
		t.Errorf("after a reset: alt %v, scrollback %d", v.AltScreen(), v.ScrollbackRows())
	}
}

// Claude Code's title carries its spinner ("✳ Claude Code"); ✳ is E2 9C B3
// in UTF-8, and 9C is a C1 string terminator to a terminal that reads it so.
// It is a title, never text on the screen.
func TestATitleWithByte9CStaysATitle(t *testing.T) {
	v := newTerm(t, 80, 5)
	fmt.Fprint(v, "\x1b]0;✳ Claude Code\x07Done with turn 2.")
	text, err := v.Text()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "Done with turn 2.") || strings.Contains(text, "Claude") {
		t.Errorf("screen: %q", strings.TrimSpace(text))
	}
}

// Contents is all of it as written, painted anew at any width: a narrower
// terminal fed it holds every line whole.
func TestContentsPaintAtAnyWidth(t *testing.T) {
	v := newTerm(t, 120, 10)
	longLines(v, 30)
	all, err := v.Contents()
	if err != nil {
		t.Fatal(err)
	}
	phone := newTerm(t, 40, 20)
	fmt.Fprint(phone, strings.ReplaceAll(all, "\n", "\r\n"))
	text, err := phone.Text()
	if err != nil {
		t.Fatal(err)
	}
	if n := lost(text, 30); n != 0 {
		t.Errorf("painted at 40 wide: %d of 30 lines lost", n)
	}
}

// Used after Close, it does nothing and says so: never a panic in the
// process it runs in.
func TestUsedAfterCloseIsHarmless(t *testing.T) {
	v, err := New(80, 24, 100)
	if err != nil {
		t.Fatal(err)
	}
	v.Close()
	if _, err := v.Write([]byte("hi")); err == nil {
		t.Error("a write after Close said nothing")
	}
	if err := v.Resize(10, 10); err == nil {
		t.Error("a resize after Close said nothing")
	}
	if c, r := v.Size(); c != 0 || r != 0 || v.AltScreen() || v.ScrollbackRows() != 0 {
		t.Error("read something after Close")
	}
	if _, err := v.History(); err == nil {
		t.Error("history after Close said nothing")
	}
	v.Close()
}

// Text drawn with the cursor stepped over its gaps (as Claude Code draws)
// comes back with spaces, its words apart and wrapping in any terminal; a
// background run comes back as spaces in it, and the pen is kept around a
// gap.
func TestGapsComeBackAsSpaces(t *testing.T) {
	v := newTerm(t, 40, 3)
	fmt.Fprint(v, "I\x1b[1Cgot\x1b[1Ctired \x1b[1;31mred\x1b[3Cstill\x1b[0m \x1b[44m\x1b[4X\x1b[0m end")
	s, err := v.Screen()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(s, "C") && regexp.MustCompile(`\x1b\[\d*C`).MatchString(s) {
		t.Errorf("a cursor step is left: %q", s)
	}
	if plain := sgr.ReplaceAllString(s, ""); !strings.Contains(plain, "I got tired red   still") {
		t.Errorf("words not apart: %q", plain)
	}
	for _, c := range []struct{ in, want string }{
		{"a\x1b[3Cb", "a   b"},
		{"\x1b[31ma\x1b[2Cb", "\x1b[31ma\x1b[0m  \x1b[31mb"},
		{"\x1b[44m\x1b[4X\x1b[4C", "\x1b[44m    "},
		{"x\x1b[Cy", "x y"},
		{"\x1b[0m\x1b[1m\x1b[38;5;2mok\x1b[0m\x1b[1Cz", "\x1b[0m\x1b[1m\x1b[38;5;2mok\x1b[0m z"},
	} {
		if got := stepsToSpaces(c.in); got != c.want {
			t.Errorf("%q: %q, want %q", c.in, got, c.want)
		}
	}
}
