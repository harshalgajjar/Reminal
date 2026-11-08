// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/x/vt"
)

// The session's copy of its screen was charmbracelet/x/vt and is now
// libghostty-vt. While both are here, the same bytes go to both, and they
// read the same, except exactly where they are known to differ:
//
//   - with no resize, the screen's text, the cursor and the history's text
//     are identical (parityNoResize): two recorded Claude Code sessions,
//     every recorded agent screen, random streams of what a session writes;
//   - across width changes the history's text is the same, wrapped otherwise
//     (parityAcrossWidths): the old copy kept each row at the width it was
//     printed, the new one keeps the line as written and wraps it anew;
//   - and the differences, each asserted on both so that a change to either
//     shows (TestTheScreenCopyDiffersFromTheOldOneOnlyWhere):
//     1. a screen cleared (ED 2): the old copy pushed the cleared rows into
//        history, the new erases them in place;
//     2. the scrollback erased (ED 3): the old copy erased the screen too,
//        the new the scrollback alone;
//     3. a height shrink with the cursor above the bottom (a frame being
//        repainted): the old copy, resized as the agent resized it
//        (resizeAnchoredBottom), kept the rows under the cursor and moved
//        everything above the last row with text into history; the new, as
//        xterm.js does, deletes the rows under the cursor and keeps the
//        cursor's row on screen, moving only the rows above the new height
//        into history;
//     4. the history's row count on the alternate screen: the old copy
//        counted the main screen's, the new counts the alternate's (none),
//        though it still hands the main screen's history out;
//     5. a variation selector after a character (U+FE0E, U+FE0F) is dropped
//        by the new copy;
//     6. the alternate screen left without having been entered: the old copy
//        did nothing, the new moves the cursor home (a never-saved cursor
//        restored, as xterm.js does);
//     7. a width shrink: the old copy cut every row on screen at the new
//        width, and the text past it was lost for good; the new re-wraps the
//        line and keeps it all. (Why the copy changed.)
//
// What the copy must read as, for any engine, is recorded by
// screencopy_conformance_test.go; this file goes with the old engine.

// oldEngine is a session's copy of its screen as it was, drained as the
// agent drained it (its answers to queries block a write otherwise).
type oldEngine struct {
	e       *vt.Emulator
	drained chan struct{}
}

func newOldEngine(cols, rows int) *oldEngine {
	e := vt.NewEmulator(cols, rows)
	e.Scrollback().SetMaxLines(20000)
	o := &oldEngine{e: e, drained: make(chan struct{})}
	go func() {
		_, _ = io.Copy(io.Discard, e)
		close(o.drained)
	}()
	return o
}

func (o *oldEngine) close() {
	if pw, ok := o.e.InputPipe().(io.Closer); ok {
		_ = pw.Close()
		<-o.drained
	}
}

// engines is both copies at one size, fed the same bytes and sizes: the old
// one resized as the agent resized it (resizeAnchoredBottom).
type engines struct {
	old *oldEngine
	new *sessionScreen
}

func newEngines(t testing.TB, cols, rows int) *engines {
	t.Helper()
	n, err := newSessionScreen(cols, rows, 20000)
	if err != nil {
		t.Fatal(err)
	}
	o := newOldEngine(cols, rows)
	t.Cleanup(func() { n.Close(); o.close() })
	return &engines{old: o, new: n}
}

func (p *engines) write(b []byte) {
	_, _ = p.old.e.Write(b)
	_, _ = p.new.Write(b)
}

func (p *engines) resize(cols, rows int) {
	resizeAnchoredBottom(p.old.e, cols, rows)
	p.new.Resize(cols, rows)
}

func (p *engines) oldScreen() []string  { return strings.Split(p.old.e.Render(), "\n") }
func (p *engines) newScreen() []string  { return strings.Split(p.new.Render(), "\n") }
func (p *engines) oldHistory() []string { return renderScrollback(p.old.e, 0) }

// parityText is a row's text as read: styles, variation selectors (which
// the new copy drops, difference 5) and the space around it gone.
func parityText(row string) string {
	return strings.TrimSpace(strings.NewReplacer("\ufe0e", "", "\ufe0f", "").Replace(stripANSI(row)))
}

func parityRows(rows []string) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = parityText(r)
	}
	return out
}

// parityTokens is rows' text as one stream of words, however they wrap.
func parityTokens(rows []string) []string {
	var out []string
	for _, r := range rows {
		out = append(out, strings.Fields(parityText(r))...)
	}
	return out
}

// parityNoResize says both copies read the same: the screen row for row,
// the cursor, and the history's words and blank lines.
func parityNoResize(t *testing.T, name string, p *engines) {
	t.Helper()
	oldScreen, newScreen := parityRows(p.oldScreen()), parityRows(p.newScreen())
	if strings.Join(oldScreen, "\n") != strings.Join(newScreen, "\n") {
		t.Errorf("%s: the screens differ\n--- old\n%s\n--- new\n%s", name, strings.Join(oldScreen, "\n"), strings.Join(newScreen, "\n"))
	}
	if o, n := p.old.e.CursorPosition(), p.new.CursorPosition(); o != n {
		t.Errorf("%s: cursor old %v, new %v", name, o, n)
	}
	oldHist, newHist := p.oldHistory(), p.new.History()
	if o, n := parityTokens(oldHist), parityTokens(newHist); strings.Join(o, " ") != strings.Join(n, " ") {
		t.Errorf("%s: the history's words differ (old %d rows, new %d lines) %s", name, len(oldHist), len(newHist), parityDiff(o, n))
	}
	blanks := func(rows []string) int {
		n := 0
		for _, r := range rows {
			if parityText(r) == "" {
				n++
			}
		}
		return n
	}
	if o, n := blanks(oldHist), blanks(newHist); o != n {
		t.Errorf("%s: blank history lines old %d, new %d", name, o, n)
	}
}

// parityDiff is where two word streams first differ, with what is around it.
func parityDiff(a, b []string) string {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	from := max(0, i-8)
	return fmt.Sprintf("at word %d:\n  old: … %s\n  new: … %s", i,
		strings.Join(a[from:min(len(a), i+8)], " "), strings.Join(b[from:min(len(b), i+8)], " "))
}

// parityAcrossWidths says the new copy holds every row of the old copy's
// history, spaces aside (the old copy's rows are cut at the width they were
// printed at, mid-word): nothing the old copy kept is lost. The other way
// round is only counted: the old copy lost text at width shrinks
// (difference 7), which the new keeps.
func parityAcrossWidths(t *testing.T, name string, p *engines) {
	t.Helper()
	nospace := func(rows []string) string {
		var b strings.Builder
		for _, r := range rows {
			b.WriteString(strings.Join(strings.Fields(parityText(r)), ""))
		}
		return b.String()
	}
	oldAll := nospace(append(p.oldHistory(), p.oldScreen()...))
	newAll := nospace(append(p.new.History(), p.newScreen()...))
	missingIn := func(rows []string, in string, report bool) int {
		missing := 0
		for _, r := range rows {
			if s := strings.Join(strings.Fields(parityText(r)), ""); s != "" && !strings.Contains(in, s) {
				if report && missing < 5 {
					t.Errorf("%s: a row of the old copy's history, %q, is nowhere in the new copy", name, parityText(r))
				}
				missing++
			}
		}
		return missing
	}
	missingIn(p.oldHistory(), newAll, true)
	if lost := missingIn(p.new.History(), oldAll, false); lost > 0 {
		t.Logf("%s: the old copy lost %d lines of history the new copy keeps (difference 7)", name, lost)
	}
}

// sameWrites are sessionWrites that the copies read alike: without the two
// that clear the screen (ED 2, difference 1), and with the alternate screen
// left only after it was entered (difference 6).
func sameWrites() []func(r int) []byte {
	var out []func(r int) []byte
	for _, w := range sessionWrites {
		b := string(w(0))
		switch {
		case strings.Contains(b, "\x1b[2J") && !strings.Contains(b, "\x1b[?1049h"):
			continue
		case b == "\x1b[?1049l":
			enter := sessionWrites[2]
			out = append(out, func(r int) []byte { return append(enter(r), []byte("\x1b[?1049l")...) })
			continue
		}
		out = append(out, w)
	}
	return out
}

// harnessScreenWidth is the width a recorded agent screen was taken at,
// from its name ("claude-theme-120.txt"); a counter ("codex-background-1")
// is not one.
func harnessScreenWidth(f string) int {
	if m := regexp.MustCompile(`-(\d+)\.txt$`).FindStringSubmatch(f); m != nil {
		cols := 0
		fmt.Sscan(m[1], &cols)
		if cols >= 20 {
			return cols
		}
	}
	return 120
}

// Two recorded Claude Code sessions, their output written to both copies at
// one size: the same screen, cursor and history.
func TestTheScreenCopyReadsRecordedSessionsAsTheOldOne(t *testing.T) {
	for _, f := range ringFixtures(t) {
		d := readRingFixture(t, f)
		p := newEngines(t, 102, 45)
		n := 0
		for _, en := range d.Entries {
			if len(en.Data) > 0 {
				p.write(en.Data)
				n += len(en.Data)
			}
		}
		parityNoResize(t, fmt.Sprintf("%s (%d bytes)", filepath.Base(f), n), p)
	}
}

// Every recorded agent screen, written to both copies at its own width: the
// same screen and cursor.
func TestTheScreenCopyReadsRecordedScreensAsTheOldOne(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "harness-screens", "*.txt"))
	if err != nil || len(files) == 0 {
		t.Fatalf("fixtures: %v (%d)", err, len(files))
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		p := newEngines(t, harnessScreenWidth(f), 60)
		p.write([]byte(strings.ReplaceAll(strings.TrimRight(string(raw), "\n"), "\n", "\r\n")))
		parityNoResize(t, filepath.Base(f), p)
	}
}

// Random streams of what a session writes, to both copies at one size: the
// same screen, cursor and history, in each of several hundred steps.
func TestTheScreenCopyReadsRandomStreamsAsTheOldOne(t *testing.T) {
	writes := sameWrites()
	for seed := int64(1); seed <= 6; seed++ {
		rnd := rand.New(rand.NewSource(seed))
		p := newEngines(t, 80+int(seed)*7, 24)
		for step := 0; step < 150; step++ {
			p.write(writes[rnd.Intn(len(writes))](rnd.Intn(1000)))
			if step%25 == 24 {
				parityNoResize(t, fmt.Sprintf("seed %d step %d", seed, step), p)
				if t.Failed() {
					return
				}
			}
		}
	}
}

// Across width changes (a phone turned, a window dragged), the new copy
// holds every row the old one kept. Without the write that erases the row
// above the cursor: after a width change that row is, in the new copy, the
// whole of a line it re-wrapped, and in the old the last fragment of the
// rows it cut, so the program erases different text in each (both as a
// terminal of that kind would).
func TestTheScreenCopyKeepsTheSameTextAcrossWidths(t *testing.T) {
	var writes []func(r int) []byte
	for _, w := range sameWrites() {
		if !strings.Contains(string(w(0)), "\x1b[1A") {
			writes = append(writes, w)
		}
	}
	for seed := int64(1); seed <= 6; seed++ {
		rnd := rand.New(rand.NewSource(seed))
		p := newEngines(t, 100, 30)
		for step := 0; step < 120; step++ {
			if step%10 == 9 {
				p.resize(50+rnd.Intn(80), 30)
				continue
			}
			p.write(writes[rnd.Intn(len(writes))](rnd.Intn(1000)))
		}
		p.write([]byte("\x1b[?1049l")) // out of any full-screen program: history is read
		parityAcrossWidths(t, fmt.Sprintf("seed %d", seed), p)
		if t.Failed() {
			return
		}
	}
}

// The differences, each as both copies do it.
func TestTheScreenCopyDiffersFromTheOldOneOnlyWhere(t *testing.T) {
	lines := func(n int) []byte {
		var b strings.Builder
		for i := 1; i <= n; i++ {
			fmt.Fprintf(&b, "line %d\r\n", i)
		}
		return []byte(b.String())
	}
	textRows := func(rows []string) int {
		n := 0
		for _, r := range rows {
			if parityText(r) != "" {
				n++
			}
		}
		return n
	}
	shows := func(rows []string, s string) bool {
		for _, r := range rows {
			if parityText(r) == s {
				return true
			}
		}
		return false
	}

	t.Run("1 a screen cleared", func(t *testing.T) {
		p := newEngines(t, 80, 24)
		p.write(lines(40))
		before := len(p.oldHistory())
		p.write([]byte("\x1b[H\x1b[2J"))
		if o, n := len(p.oldHistory()), p.new.HistoryLen(); o != before+23 || n != before {
			t.Errorf("history rows after ED 2: old %d (want %d, the screen pushed in), new %d (want %d, erased in place)", o, before+23, n, before)
		}
		if o, n := textRows(p.oldScreen()), textRows(p.newScreen()); o != 0 || n != 0 {
			t.Errorf("screen rows with text after ED 2: old %d, new %d, want none", o, n)
		}
	})
	t.Run("2 the scrollback erased", func(t *testing.T) {
		p := newEngines(t, 80, 24)
		p.write(lines(40))
		p.write([]byte("\x1b[3J"))
		if o, n := len(p.oldHistory()), p.new.HistoryLen(); o != 0 || n != 0 {
			t.Errorf("history rows after ED 3: old %d, new %d, want none", o, n)
		}
		if o, n := textRows(p.oldScreen()), textRows(p.newScreen()); o != 0 || n != 23 {
			t.Errorf("screen rows with text after ED 3: old %d (erased too), new %d (want 23, kept)", o, n)
		}
	})
	t.Run("3 a height shrink with the cursor above the bottom", func(t *testing.T) {
		p := newEngines(t, 80, 24)
		p.write(lines(20))         // rows 1 to 20 hold text, the cursor on row 21
		p.write([]byte("\x1b[8A")) // the cursor up to row 13, as a frame being repainted
		p.resize(80, 10)
		// The old copy: everything above the last row with text (20), less
		// the 10 rows kept, into history: 10 rows; "line 20" still shown.
		if o := len(p.oldHistory()); o != 10 {
			t.Errorf("old history rows after the shrink: %d, want 10", o)
		}
		if !shows(p.oldScreen(), "line 20") {
			t.Errorf("old: the rows under the cursor went: %q", parityRows(p.oldScreen()))
		}
		// The new copy: the rows under the cursor (14 to 24) deleted, the
		// cursor's row kept on screen with the 9 above it, the 3 above
		// those into history; "line 20" shown nowhere.
		if n := p.new.HistoryLen(); n != 3 {
			t.Errorf("new history rows after the shrink: %d, want 3", n)
		}
		if !shows(p.newScreen(), "line 13") || shows(p.newScreen(), "line 20") || shows(p.new.History(), "line 20") {
			t.Errorf("new: want the cursor's row kept and the rows under it gone: screen %q", parityRows(p.newScreen()))
		}
	})
	t.Run("4 the history's rows on the alternate screen", func(t *testing.T) {
		p := newEngines(t, 80, 24)
		p.write(lines(40))
		p.write([]byte("\x1b[?1049h\x1b[H\x1b[2Jfull-screen"))
		if o, n := len(p.oldHistory()), p.new.HistoryLen(); o != 17 || n != 0 {
			t.Errorf("history rows on the alternate screen: old %d (want 17, the main screen's), new %d (want 0, the alternate's)", o, n)
		}
		if got := len(p.new.History()); got != 17 {
			t.Errorf("the new copy's History on the alternate screen: %d lines, want the main screen's 17", got)
		}
	})
	t.Run("5 a variation selector", func(t *testing.T) {
		p := newEngines(t, 80, 24)
		p.write([]byte("\u25cf\ufe0e Auto mode enabled.\r\n"))
		o, n := strings.TrimSpace(stripANSI(p.oldScreen()[0])), strings.TrimSpace(stripANSI(p.newScreen()[0]))
		if !strings.Contains(o, "\ufe0e") || strings.Contains(n, "\ufe0e") || parityText(o) != parityText(n) {
			t.Errorf("old %q, new %q: want the selector kept by the old copy, dropped by the new, the text alike", o, n)
		}
	})
	t.Run("6 the alternate screen left without having been entered", func(t *testing.T) {
		p := newEngines(t, 80, 24)
		p.write(lines(40))
		p.write([]byte("\x1b[?1049l"))
		if o, n := p.old.e.CursorPosition(), p.new.CursorPosition(); o.Y != 23 || n.Y != 0 || n.X != 0 {
			t.Errorf("cursor after a stray ?1049l: old %v (want row 23, untouched), new %v (want home)", o, n)
		}
		if o, n := textRows(p.oldScreen()), textRows(p.newScreen()); o != 23 || n != 23 {
			t.Errorf("screen rows with text after a stray ?1049l: old %d, new %d, want 23 on both", o, n)
		}
	})
}

// Difference 7, on its own: a width shrink with text on screen.
func TestTheScreenCopyKeepsTextTheOldOneCutAtAWidthShrink(t *testing.T) {
	p := newEngines(t, 80, 24)
	var b strings.Builder
	for i := 1; i <= 10; i++ {
		fmt.Fprintf(&b, "row %02d: the quick brown fox jumps over the lazy dog and keeps on running\r\n", i)
	}
	p.write([]byte(b.String()))
	p.resize(50, 24)
	oldText := strings.Join(parityTokens(append(p.oldHistory(), p.oldScreen()...)), " ")
	newText := strings.Join(parityTokens(append(p.new.History(), p.newScreen()...)), " ")
	if strings.Contains(oldText, "keeps on running") {
		t.Errorf("old: the text past column 50 is still there; the old copy no longer cuts rows at a width shrink: %q", oldText)
	}
	if strings.Count(newText, "keeps on running") != 10 {
		t.Errorf("new: want all 10 rows whole after the width shrink, got %q", newText)
	}
}
