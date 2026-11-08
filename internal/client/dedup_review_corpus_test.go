// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// What the strict rules (REMINAL_DEDUP=strict) guarantee, through the join's
// history pass (dedupHistory): no row is ever lost entirely, though a repeat
// may show once. Each file in testdata/dedup/review is a shape review found
// dropped on some head of this change, read three ways:
//
//   - no resize: nothing the session remembers could be a reprint;
//   - a resize with no frame: a segment over the whole history whose
//     captured screen is empty;
//   - a resize with its frame: one as it happens, right where the second
//     copy begins, its frame the screenful above it (reviewCorpusFrame).
//
// The default rules drop some of these shapes entirely, by design.
//
// With no resize every case comes back exactly as given: nothing can be a
// reprint. A command run again at a shell's prompt (reviewCorpusCommands)
// comes back exactly in every mode: running it again is news, not a reprint.
// Otherwise, with a resize a copy may go (a paragraph printed again as it was
// after a resize, or one inside its frame, can be a reprint), but every row
// is still shown at least once: its text as printed, not just its words,
// though maybe inside a longer row or wrapped otherwise (reviewCorpusMissing).
// The one exception is reprint-rewrapped, the same paragraph at two widths: with its frame it goes as
// reprint-rewrapped.want.txt says (the reprint dropped). Without a frame it
// is not checked, since a word-level match there is how a reconnect's
// stacked copies are cleared when the resizes behind them are forgotten.
func TestDedupReviewCorpus(t *testing.T) {
	withDedupStrict(t)
	files, err := filepath.Glob("testdata/dedup/review/*.txt")
	if err != nil || len(files) == 0 {
		t.Fatalf("no review corpus: %v", err)
	}
	read := func(f string) []string {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	}
	for _, f := range files {
		if strings.HasSuffix(f, ".want.txt") {
			continue
		}
		name := strings.TrimSuffix(filepath.Base(f), ".txt")
		lines := read(f)
		want := lines
		reprint := false
		if w := strings.TrimSuffix(f, ".txt") + ".want.txt"; reviewCorpusFileExists(w) {
			want, reprint = read(w), true
		}
		for _, m := range []struct {
			mode  string
			segs  []resizeSeg
			check bool
		}{
			{"no resize", nil, !reprint},
			{"a resize with no frame", []resizeSeg{{start: 0, maxOver: len(lines)}}, !reprint},
			{"a resize with its frame", []resizeSeg{reviewCorpusFrame(lines)}, true},
		} {
			if !m.check {
				continue
			}
			expect := lines
			if m.segs != nil && m.segs[0].words != nil {
				expect = want
			}
			got := dedupHistory(append([]string(nil), lines...), m.segs, resizesOf(m.segs), "", 0)
			if m.segs != nil && !reprint && !reviewCorpusCommand(name) {
				// After a resize a copy can be a reprint and may go: nothing
				// may be lost, every row still shown at least once.
				if missing := reviewCorpusMissing(lines, got); len(missing) > 0 {
					t.Errorf("%s, %s: rows no longer shown at all: %q", name, m.mode, missing)
				}
				continue
			}
			if strings.Join(got, "\n") != strings.Join(expect, "\n") {
				t.Errorf("%s, %s: %d lines in, %d out, want %d:\n--- got\n%s\n--- want\n%s",
					name, m.mode, len(lines), len(got), len(expect), strings.Join(got, "\n"), strings.Join(expect, "\n"))
			}
		}
	}
}

// reviewCorpusCommands are the cases of a command run twice at a prompt.
var reviewCorpusCommands = []string{"go-test-twice-", "cat-tail-", "fmt-after-cat-"}

func reviewCorpusCommand(name string) bool {
	return slices.ContainsFunc(reviewCorpusCommands, func(p string) bool { return strings.HasPrefix(name, p) })
}

func reviewCorpusFileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// reviewCorpusFrame is a resize as one happens: right where the second copy
// begins (the first row whose first words a row before it began with), its
// captured frame the screenful above it, its window a frame's height.
func reviewCorpusFrame(lines []string) resizeSeg {
	const screen = 24
	head := func(s string) string {
		w := dedupWords(s)
		return strings.Join(w[:min(3, len(w))], " ")
	}
	start := len(lines)
	seen := map[string]bool{}
	for i, l := range lines {
		h := head(l)
		if h == "" {
			continue
		}
		if seen[h] {
			start = i
			break
		}
		seen[h] = true
	}
	return resizeSeg{start: start, maxOver: screen, words: regionWords(lines, max(0, start-screen), start)}
}

// reviewCorpusMissing is the rows of in whose text out no longer shows: its
// tokens as printed (split at spaces, colours aside) are no unbroken run of
// out's, read as one text, rows wrapped however they are.
func reviewCorpusMissing(in, out []string) []string {
	text := func(l string) string {
		return strings.Join(strings.Fields(dedupANSI.ReplaceAllString(l, "")), "\x00")
	}
	var all []string
	for _, l := range out {
		if t := text(l); t != "" {
			all = append(all, t)
		}
	}
	shown := "\x00" + strings.Join(all, "\x00") + "\x00"
	var missing []string
	for _, l := range in {
		if t := text(l); t != "" && !strings.Contains(shown, "\x00"+t+"\x00") {
			missing = append(missing, strings.TrimSpace(dedupANSI.ReplaceAllString(l, "")))
		}
	}
	return missing
}

// The join's last pass, against the live screen, keeps the strict promise
// too: history rows whose words the screen holds, but not their text (a
// test that passed then, failing now), are still shown.
func TestStrictFramePassKeepsRowsTheScreenChanged(t *testing.T) {
	withDedupStrict(t)
	line := "running the suite against the staging database with every fixture loaded first"
	history := []string{"$ npm test", line, "✓ login accepts a valid password", "✓ login refuses a wrong password"}
	screen := []string{line, "✗ login accepts a valid password", "✗ login refuses a wrong password"}
	got := dedupAgainstFrame(append([]string(nil), history...), screen)
	if missing := reviewCorpusMissing(history, append(got, screen...)); len(missing) > 0 {
		t.Errorf("rows no longer shown at all: %q (history kept %q)", missing, got)
	}
	// Nor is a line shown only by its quoted form on the screen: whether a
	// resize came between is not known there.
	said := "Thanks, the deploy finished at noon and every host came back healthy after the restart"
	history = []string{said}
	screen = []string{"> " + said, "Great, closing the ticket now."}
	if got := dedupAgainstFrame(append([]string(nil), history...), screen); len(got) != 1 {
		t.Errorf("the line went, shown only quoted on the screen: kept %q", got)
	}
}

// What the row-text check counts as shown: a reprint at another width, cut
// at the screen's top and bottom, and a quote whose bar wrapping repeats on
// every row. Neither is restored; a shorter line inside one longer row is.
func TestUnshownRowsReadsReprintsAsShown(t *testing.T) {
	item := func(n int) string {
		return fmt.Sprintf("%d. T5L%02d alpha bravo charlie delta echo foxtrot golf-T5L%02d hotel india juliet kilo lima mike ENDT5L%02d", n, n, n, n)
	}
	var kept, narrow []string
	for n := 9; n <= 18; n++ {
		kept = append(kept, item(n))
		narrow = append(narrow, dfWrap(item(n), 50)...)
	}
	narrow = narrow[1 : len(narrow)-2] // cut at both edges, mid-item
	quote := "▎ You've met him by now, I expect. He doesn't want anything except for his father to get home safe."
	cases := []struct {
		name    string
		kept    []string
		dropped []string
		want    int // rows restored
	}{
		{"a reprint cut at both edges", kept, narrow, 0},
		{"a quote wrapped with its bar", []string{quote}, []string{
			"▎ You've met him by now, I expect. He doesn't want",
			"▎ anything except for his father to get home safe.",
		}, 0},
		{"a shorter line inside one row", []string{"Deploying service api to cluster prod-east with three replicas and the canary on"},
			[]string{"Deploying service api to cluster prod-east with three replicas"}, 1},
	}
	for _, c := range cases {
		lines := append(append(append([]string(nil), c.kept...), ""), c.dropped...)
		drop := make([]bool, len(lines))
		for x := len(c.kept) + 1; x < len(lines); x++ {
			drop[x] = true
		}
		anywhere := func(int, int) bool { return true }
		if got := unshownRows(lines, drop, nil, anywhere, true); len(got) != c.want {
			t.Errorf("%s: %d rows restored, want %d", c.name, len(got), c.want)
		}
	}
}
