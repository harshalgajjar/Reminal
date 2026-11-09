// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The session's copy of its screen (sessionScreen), through everything the
// agent asks of it, against recorded expectations: what each input must
// read as, whatever engine is behind the copy. The expectations are in
// testdata/screencopy/*.golden.gz, one file a case, a checkpoint at a time:
// the size, the cursor, whether the alternate screen is up, the history's
// row and line counts, the screen row by row and (at the end) the history
// line by line, each as the copy renders it, styles included.
//
// A change in what the copy reads as, a new engine behind it most of all,
// shows as a diff of these files: UPDATE_GOLDEN=1 re-records them, and the
// diff is the change of behaviour, to be read line by line. Nothing here
// passes because an engine agrees with itself.
//
// The cases:
//   - rings: two recorded Claude Code sessions, replayed with the sizes they
//     were written at (a story written, the phone folded and unfolded, the
//     keyboard opened and closed), a checkpoint at every resize;
//   - screens: every recorded agent screen, at its width;
//   - random: random streams of what a session writes, at one size;
//   - widths, heights: the same with the width, then the height, changed
//     between writes;
//   - semantics: one sequence each for what a terminal must get right: a
//     screen cleared, the scrollback erased, the alternate screen entered and
//     left, a scroll region, wide characters at the edge, a line exactly as
//     wide as the screen, saved and restored cursors, lines inserted and
//     deleted, a title holding a C1 byte, tabs, a resize on the alternate
//     screen, a line rewritten in place.

// copyRecord is one case's expectations as text.
type copyRecord struct {
	t *testing.T
	b strings.Builder
}

func (r *copyRecord) checkpoint(name string, s *sessionScreen, history bool) {
	cur := s.CursorPosition()
	fmt.Fprintf(&r.b, "== %s\nsize %dx%d\ncursor %d,%d\nalt %v\nhistory rows %d\n", name, s.Width(), s.Height(), cur.X, cur.Y, s.IsAltScreen(), s.HistoryLen())
	if !s.IsAltScreen() {
		fmt.Fprintf(&r.b, "history lines %d\n", s.HistoryLines())
	}
	rows := strings.Split(s.Render(), "\n")
	fmt.Fprintf(&r.b, "-- screen (%d rows)\n", len(rows))
	for _, row := range rows {
		fmt.Fprintf(&r.b, "%q\n", strings.TrimRight(row, " "))
	}
	if history {
		lines := s.History()
		fmt.Fprintf(&r.b, "-- history (%d lines)\n", len(lines))
		for _, l := range lines {
			fmt.Fprintf(&r.b, "%q\n", l)
		}
	}
}

// done compares the record with its golden file, or records it.
func (r *copyRecord) done(name string) {
	r.t.Helper()
	path := filepath.Join("testdata", "screencopy", name+".golden.gz")
	got := r.b.String()
	if os.Getenv("UPDATE_GOLDEN") != "" {
		var z bytes.Buffer
		w := gzip.NewWriter(&z)
		_, _ = w.Write([]byte(got))
		_ = w.Close()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			r.t.Fatal(err)
		}
		if err := os.WriteFile(path, z.Bytes(), 0o644); err != nil {
			r.t.Fatal(err)
		}
		r.t.Logf("recorded %s (%d lines)", path, strings.Count(got, "\n"))
		return
	}
	f, err := os.Open(path)
	if err != nil {
		r.t.Fatalf("%s: no expectations recorded (UPDATE_GOLDEN=1 records them): %v", name, err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		r.t.Fatal(err)
	}
	var want bytes.Buffer
	if _, err := want.ReadFrom(zr); err != nil {
		r.t.Fatal(err)
	}
	if got == want.String() {
		return
	}
	g, w := strings.Split(got, "\n"), strings.Split(want.String(), "\n")
	section := ""
	for i := 0; i < len(g) || i < len(w); i++ {
		gl, wl := "", ""
		if i < len(g) {
			gl = g[i]
		}
		if i < len(w) {
			wl = w[i]
		}
		if strings.HasPrefix(wl, "== ") {
			section = wl
		}
		if gl != wl {
			r.t.Errorf("%s: reads otherwise than recorded, at line %d (%s):\n  recorded: %s\n  now:      %s\n(UPDATE_GOLDEN=1 re-records; the diff of the file is the change)", name, i+1, section, wl, gl)
			return
		}
	}
}

func newCopyRecord(t *testing.T) *copyRecord { return &copyRecord{t: t} }

// sessionWrites are the things a session writes, by a number that varies
// them: lines, wide characters, a full-screen program entered and left, the
// screen cleared, the scrollback erased, a line erased and rewritten in
// place with the row above, a styled status row.
var sessionWrites = []func(r int) []byte{
	func(r int) []byte {
		var b strings.Builder
		for i := 0; i < 5+r%60; i++ {
			fmt.Fprintf(&b, "P%d-%02d the quick brown fox jumps over the lazy dog, %d times\r\n", r%7, i, r)
		}
		return []byte(b.String())
	},
	func(r int) []byte {
		return []byte(strings.Repeat("宽字符テスト🙂 wide "+fmt.Sprint(r)+"\r\n", 3+r%20))
	},
	func(r int) []byte {
		return []byte("\x1b[?1049h\x1b[2J\x1b[Han app\r\nrow two " + strings.Repeat("x", r%50))
	},
	func(r int) []byte { return []byte("\x1b[?1049l") },
	func(r int) []byte { return []byte("\x1b[H\x1b[2J") },
	func(r int) []byte { return []byte("\x1b[H\x1b[2J\x1b[3J") },
	func(r int) []byte {
		return []byte(fmt.Sprintf("\x1b[2K\rrewritten in place %d\x1b[1A\x1b[2K\rand the row above %d\r\n\r\n", r, r))
	},
	func(r int) []byte {
		return []byte(fmt.Sprintf("\x1b[38;2;215;119;87m* Thinking… (%d s)\x1b[39m\r\n\x1b[2m| a dim row\x1b[22m\r\n", r%100))
	},
}

func TestTheScreenCopyReadsRecordedSessionsAsRecorded(t *testing.T) {
	for _, f := range ringFixtures(t) {
		d := readRingFixture(t, f)
		name := strings.TrimSuffix(filepath.Base(f), ".dump.json.gz")
		s := testScreen(t, max(d.BaseCols, 1), max(d.BaseRows, 1))
		s.SetMaxLines(20000)
		rec := newCopyRecord(t)
		rec.checkpoint("start", s, false)
		resizes := 0
		for i, en := range d.Entries {
			if en.Cols > 0 && en.Rows > 0 {
				resizes++
				s.Resize(en.Cols, en.Rows)
				rec.checkpoint(fmt.Sprintf("resize %d at entry %d", resizes, i), s, false)
				continue
			}
			if len(en.Data) > 0 {
				_, _ = s.Write(en.Data)
			}
			if i%200 == 199 {
				rec.checkpoint(fmt.Sprintf("entry %d", i), s, false)
			}
		}
		rec.checkpoint("end", s, true)
		rec.done("rings/" + name)
	}
}

func TestTheScreenCopyReadsRecordedScreensAsRecorded(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "harness-screens", "*.txt"))
	if err != nil || len(files) == 0 {
		t.Fatalf("fixtures: %v (%d)", err, len(files))
	}
	width := regexp.MustCompile(`-(\d+)\.txt$`)
	rec := newCopyRecord(t)
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		cols := 120
		if m := width.FindStringSubmatch(f); m != nil {
			fmt.Sscan(m[1], &cols)
			if cols < 20 { // a counter ("-1"), not a width
				cols = 120
			}
		}
		s := testScreen(t, cols, 60)
		_, _ = s.Write([]byte(strings.ReplaceAll(strings.TrimRight(string(raw), "\n"), "\n", "\r\n")))
		rec.checkpoint(filepath.Base(f), s, true)
	}
	rec.done("screens")
}

func TestTheScreenCopyReadsRandomStreamsAsRecorded(t *testing.T) {
	for seed := int64(1); seed <= 6; seed++ {
		rnd := rand.New(rand.NewSource(seed))
		s := testScreen(t, 80+int(seed)*7, 24)
		rec := newCopyRecord(t)
		for step := 0; step < 150; step++ {
			r := rnd.Intn(1000)
			_, _ = s.Write(sessionWrites[rnd.Intn(len(sessionWrites))](r))
			if step%25 == 24 {
				rec.checkpoint(fmt.Sprintf("step %d", step), s, step == 149)
			}
		}
		rec.done(fmt.Sprintf("random/seed-%d", seed))
	}
}

func TestTheScreenCopyReadsWidthChangesAsRecorded(t *testing.T) {
	for seed := int64(1); seed <= 6; seed++ {
		rnd := rand.New(rand.NewSource(seed))
		s := testScreen(t, 100, 30)
		rec := newCopyRecord(t)
		for step := 0; step < 120; step++ {
			if step%10 == 9 {
				s.Resize(50+rnd.Intn(80), 30)
				rec.checkpoint(fmt.Sprintf("step %d, width %d", step, s.Width()), s, false)
				continue
			}
			_, _ = s.Write(sessionWrites[rnd.Intn(len(sessionWrites))](rnd.Intn(1000)))
		}
		_, _ = s.Write([]byte("\x1b[?1049l"))
		rec.checkpoint("end", s, true)
		rec.done(fmt.Sprintf("widths/seed-%d", seed))
	}
}

func TestTheScreenCopyReadsHeightChangesAsRecorded(t *testing.T) {
	for seed := int64(1); seed <= 6; seed++ {
		rnd := rand.New(rand.NewSource(seed))
		s := testScreen(t, 60, 40)
		rec := newCopyRecord(t)
		for step := 0; step < 120; step++ {
			if step%10 == 9 {
				// A keyboard opened (shorter), then closed (taller), or a
				// window dragged to any height.
				switch rnd.Intn(3) {
				case 0:
					s.Resize(60, 16)
				case 1:
					s.Resize(60, 40)
				default:
					s.Resize(60, 10+rnd.Intn(40))
				}
				rec.checkpoint(fmt.Sprintf("step %d, height %d", step, s.Height()), s, false)
				continue
			}
			_, _ = s.Write(sessionWrites[rnd.Intn(len(sessionWrites))](rnd.Intn(1000)))
		}
		_, _ = s.Write([]byte("\x1b[?1049l"))
		rec.checkpoint("end", s, true)
		rec.done(fmt.Sprintf("heights/seed-%d", seed))
	}
}

func TestTheScreenCopyReadsTerminalSemanticsAsRecorded(t *testing.T) {
	lines := func(n int) []byte {
		var b strings.Builder
		for i := 1; i <= n; i++ {
			fmt.Fprintf(&b, "line %d of the text written before\r\n", i)
		}
		return []byte(b.String())
	}
	cases := []struct {
		name  string
		cols  int
		rows  int
		steps []string // each written in turn, a checkpoint after each
	}{
		{"a screen cleared (ED 2) keeps the history and erases the screen in place", 80, 24, []string{string(lines(40)), "\x1b[H\x1b[2J", "after the clear\r\n"}},
		{"a screen cleared without the cursor moved first", 80, 24, []string{string(lines(40)), "\x1b[2J", "x"}},
		{"the scrollback erased (ED 3) erases the history alone", 80, 24, []string{string(lines(40)), "\x1b[3J", "after the erase\r\n"}},
		{"the screen and scrollback erased together, as clear does", 80, 24, []string{string(lines(40)), "\x1b[H\x1b[2J\x1b[3J", "a fresh start\r\n"}},
		{"the alternate screen entered and left keeps the main screen and its history", 80, 24, []string{string(lines(40)), "\x1b[?1049h\x1b[H\x1b[2Jfull-screen program\r\nits second row", "\x1b[H\x1b[2J", "\x1b[?1049l", "back on the main screen\r\n"}},
		{"a resize on the alternate screen, then back", 80, 24, []string{string(lines(40)), "\x1b[?1049h\x1b[H\x1b[2Jfull-screen program", "\x1b[?1049l"}},
		{"a scroll region keeps the rows outside it", 80, 24, []string{"\x1b[H\x1b[2Jheader row\r\n\x1b[3;10r\x1b[3;1H" + strings.Repeat("inside the region\r\n", 20), "\x1b[r\x1b[24;1Hfooter row"}},
		{"wide characters at the right edge wrap whole", 20, 10, []string{"abcdefghijklmnopq宽字符\r\n", "🙂🙂🙂🙂🙂🙂🙂🙂🙂🙂🙂\r\n"}},
		{"a line exactly as wide as the screen, then a newline, adds no blank row", 40, 10, []string{strings.Repeat("x", 40) + "\r\n", "next\r\n", strings.Repeat("y", 40), "\r\n", "last\r\n"}},
		{"a line wider than the screen wraps and is one line of history", 40, 5, []string{strings.Repeat("0123456789", 12) + "\r\n", string(lines(10))}},
		{"a cursor saved and restored", 80, 24, []string{"one\r\ntwo\r\nthree\x1b7\r\nfour\r\nfive\x1b8 back at three", "\r\n"}},
		{"lines inserted and deleted", 80, 24, []string{string(lines(10)), "\x1b[3;1H\x1b[2L", "inserted A\r\ninserted B", "\x1b[6;1H\x1b[3M", "\x1b[24;1H"}},
		{"a title holding a C1 byte ends the title, not the text after", 80, 24, []string{"\x1b]0;a title with \x9c in it\x07visible text after\r\n", "\x1b]2;title ended by ST\x1b\\and more text\r\n"}},
		{"tabs land on tab stops", 80, 24, []string{"a\tb\tc\r\n\tindented\r\n" + strings.Repeat("\t", 12) + "far\r\n"}},
		{"a line rewritten in place, and the row above it", 80, 24, []string{string(lines(5)), "progress 1%", "\r\x1b[2Kprogress 50%", "\x1b[1A\x1b[2K\rthe row above, rewritten\r\n\x1b[2Kprogress 100%\r\n"}},
		{"a carriage return without a newline overwrites", 80, 24, []string{"hello world", "\rHELLO", "\r\n"}},
		{"backspace and erase to the end of the line", 80, 24, []string{"typed text\b\b\b\b\x1b[Kmore\r\n"}},
		{"reverse index at the top scrolls down", 80, 5, []string{string(lines(8)), "\x1b[H\x1bM", "a row scrolled in at the top"}},
		{"colours and styles are kept on the screen and in history", 80, 5, []string{"\x1b[1mbold\x1b[22m \x1b[31mred\x1b[39m \x1b[38;2;10;20;30mtruecolor\x1b[39m \x1b[4munder\x1b[24m\r\n", string(lines(8))}},
	}
	for _, c := range cases {
		s := testScreen(t, c.cols, c.rows)
		rec := newCopyRecord(t)
		for i, step := range c.steps {
			_, _ = s.Write([]byte(step))
			if strings.Contains(c.name, "resize on the alternate") && i == 1 {
				s.Resize(60, 20)
				rec.checkpoint("resized to 60x20 on the alternate screen", s, false)
			}
			rec.checkpoint(fmt.Sprintf("after write %d", i+1), s, true)
		}
		rec.done("semantics/" + strings.NewReplacer(" ", "-", ",", "", "(", "", ")", "").Replace(c.name))
	}
}

// readRingFixture is a recorded session's restart dump (testdata/restart):
// its output ring, with the sizes it was written at.
func readRingFixture(t *testing.T, path string) *scrollbackDump {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	var raw bytes.Buffer
	if _, err := raw.ReadFrom(z); err != nil {
		t.Fatal(err)
	}
	var d scrollbackDump
	if err := json.Unmarshal(raw.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Entries) == 0 {
		t.Fatalf("%s: an empty ring", path)
	}
	return &d
}

func ringFixtures(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("testdata", "restart", "*.dump.json.gz"))
	if err != nil || len(files) == 0 {
		t.Fatalf("fixtures: %v (%d)", err, len(files))
	}
	return files
}
