// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"os"
	"path/filepath"
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
// reprint. With a resize a copy may go (a paragraph printed again as it was
// after a resize, or one inside its frame, can be a reprint), but every row
// is still shown at least once. The one exception is reprint-rewrapped, the
// same paragraph at two widths: with its frame it goes as
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
			got := dedupHistory(append([]string(nil), lines...), m.segs, "", 0, false)
			if m.segs != nil && !reprint {
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

// reviewCorpusMissing is the rows of in, as read, that out no longer shows
// anywhere.
func reviewCorpusMissing(in, out []string) []string {
	shown := map[string]bool{}
	for _, l := range out {
		shown[strings.TrimSpace(dedupANSI.ReplaceAllString(l, ""))] = true
	}
	var missing []string
	for _, l := range in {
		if r := strings.TrimSpace(dedupANSI.ReplaceAllString(l, "")); r != "" && !shown[r] {
			missing = append(missing, r)
		}
	}
	return missing
}
