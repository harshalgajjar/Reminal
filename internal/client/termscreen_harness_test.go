// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// Every recorded agent screen (testdata/harness-screens), drawn into a real
// sessionScreen of its own width and read back through Render, says the
// same to every screen reader as the recording itself: logged out or not,
// a key refused or not, at a usage limit or not, how many shells and other
// programs are on it. The readers now read what the terminal formats, so a
// difference in trailing spaces, blank cells or wide glyphs would change
// what a session is listed as.
func TestRecordedScreensReadTheSameThroughTheSessionScreen(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "harness-screens", "*.txt"))
	if err != nil || len(files) == 0 {
		t.Fatalf("fixtures: %v (%d)", err, len(files))
	}
	width := regexp.MustCompile(`-(\d+)\.txt$`)
	now := time.Unix(1_800_000_000, 0)
	same, differ := 0, 0
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		name := strings.TrimSuffix(filepath.Base(f), ".txt")
		prog := strings.SplitN(name, "-", 2)[0]
		if prog == "cursor" {
			prog = "cursor-agent"
		}
		// Captured in tmux at 120 columns, or at the width the name ends
		// in (a small number there is a count, not a width).
		cols := 120
		if m := width.FindStringSubmatch(f); m != nil {
			if n, _ := strconv.Atoi(m[1]); n >= 40 {
				cols = n
			}
		}
		lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
		// A recording with a line wider than its own screen (gemini-key-80's
		// first, a long JSON error joined by the capture) is not what that
		// screen showed: any terminal wraps the line, and a reader that
		// wants it whole on one row reads the recording, not a screen.
		if w := widestLine(lines); w > cols {
			t.Logf("%s: skipped, a recorded line is %d cells on a %d-column screen", name, w, cols)
			continue
		}
		// Rows to spare: the blank rows under the screen are not part of it.
		s := testScreen(t, cols, len(lines)+8)
		if _, err := s.Write([]byte(strings.Join(lines, "\r\n"))); err != nil {
			t.Fatal(err)
		}
		rendered := strings.TrimRight(stripANSI(s.Render()), " \n")
		s.Close()

		type reading struct {
			out, refused, limit bool
			shells              int
			others              string
		}
		read := func(screen string) reading {
			tail := harnessTail(screen, harnessRowsFor(prog))
			limit, _ := harnessLimitIn(tail, prog, now)
			return reading{
				out:     harnessProblemIn(tail, prog),
				refused: harnessKeyRefusedBy(tail, prog),
				limit:   limit,
				shells:  attnShellCount(true, tail),
				others:  fmt.Sprint(attnOtherOnScreen(prog, true, screen)),
			}
		}
		a, b := read(string(raw)), read(rendered)
		if a != b {
			differ++
			t.Errorf("%s (%s, %d cols): the recording reads %+v, through the screen %+v", name, prog, cols, a, b)
			continue
		}
		same++
	}
	t.Logf("%d screens read the same through the session's screen, %d differ", same, differ)
}

// widestLine is the widest of lines, in runes (the recordings are text,
// one cell a rune but for the few wide glyphs, which only make a line
// wider).
func widestLine(lines []string) int {
	w := 0
	for _, l := range lines {
		w = max(w, utf8.RuneCountInString(l))
	}
	return w
}
