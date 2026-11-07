// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// dfWrap wraps a word sequence at width w, one paragraph (no internal blanks).
func dfWrap(text string, w int) []string {
	return nsWrap(text, w)
}

// TestDedupAgainstFrame covers the cross-width resize-repaint case dedupBlocks
// can't pair: the same paragraph stamped into committed history at several widths
// (different wrap points → different paragraph keys) while the app still shows it
// in its live frame. All stamped copies must go; everything else must survive.
func TestDedupAgainstFrame(t *testing.T) {
	para := "the tide retreats and leaves behind a shallow basin of trapped seawater where anemones and hermit crabs wait out the hours until the ocean returns to reclaim them"
	old := "an entirely different committed passage about glaciers carving valleys over millennia that the app has long scrolled past and must never be touched by any dedup"

	// Committed history: an old unique paragraph, then the SAME para stamped at
	// three widths (as resize repaints do), blank-separated.
	var history []string
	history = append(history, dfWrap(old, 70)...)
	history = append(history, "")
	for _, w := range []int{44, 61, 78} {
		history = append(history, dfWrap(para, w)...)
		history = append(history, "")
	}

	// Live frame: shows para at the current width plus app chrome.
	screen := append([]string{"╭─ app chrome ─╮"}, dfWrap(para, 90)...)
	screen = append(screen, "│ > input box  │", "╰──────────────╯")

	out := dedupAgainstFrame(history, screen)
	joined := strings.Join(out, "\n")

	if !strings.Contains(joined, "glaciers carving valleys") {
		t.Errorf("unique committed paragraph was deleted:\n%s", joined)
	}
	if n := strings.Count(joined, "hermit crabs"); n != 0 {
		t.Errorf("stale frame re-emissions survived: %d copies still in history (want 0 — the frame itself paints it)\n%s", n, joined)
	}
}

// TestDedupAgainstFrameNeverTouchesScrolledPast: content similar to — but not a
// re-emission of — the frame must survive. A paragraph sharing SOME words with the
// frame (below shingle coverage) stays.
func TestDedupAgainstFrameNeverTouchesScrolledPast(t *testing.T) {
	frame := dfWrap("the quick brown fox jumps over the lazy dog and then runs far away into the deep dark forest never to be seen again by anyone", 60)
	// Shares vocabulary but different sequence → different shingles.
	past := dfWrap("the lazy dog watches the brown forest while the quick fox sleeps and anyone seen running away returns into the deep dark night again", 45)

	out := dedupAgainstFrame(append(append([]string{}, past...), ""), frame)
	if len(out) < len(past) {
		t.Errorf("similar-but-distinct paragraph was deleted (%d of %d lines survived)", len(out), len(past))
	}
}

// TestDedupAgainstFrameShortParagraphsSafe: paragraphs under the word gate are never
// dropped even if fully present in the frame (prompts, headings, one-liners recur
// legitimately).
func TestDedupAgainstFrameShortParagraphsSafe(t *testing.T) {
	frame := []string{"build passed all tests green deploy now ok done finished complete", "extra frame words to satisfy shingle minimum here now"}
	short := []string{"build passed all tests green"} // 5 words < dedupMinParaWords
	out := dedupAgainstFrame(append(append([]string{}, short...), ""), frame)
	if len(out) < 1 || out[0] != short[0] {
		t.Errorf("short paragraph dropped: %v", out)
	}
}

// TestDedupAgainstFrameTruncatedFragment reproduces the live miss that motivated
// line-level matching: a resize repaint commits a TRUNCATED copy of the frame's
// paragraph, contiguous with leftover unrelated rows (welcome-box borders). The
// fragment lines must go; the junk rows (words the frame doesn't contain) stay.
func TestDedupAgainstFrameTruncatedFragment(t *testing.T) {
	para := "oceans cover most of our planet holding nearly all its water in constant motion waves rise and fall across vast distances shaped by wind and gravity beneath the surface currents carry warmth between distant continents"
	frame := append([]string{"⏺ MARKER-AAA"}, dfWrap(para, 90)...)

	fragment := append([]string{"⏺ MARKER-AAA"}, dfWrap(para, 55)[:2]...) // truncated: first 2 wrapped rows only
	junk := []string{
		"│                       │ Tips for getting started        │",
		"│                       │ Run /init to create CLAUDE.md   │",
	}
	history := append(append(append([]string{}, fragment...), junk...), "")

	out := dedupAgainstFrame(history, frame)
	joined := strings.Join(out, "\n")
	if strings.Contains(joined, "MARKER-AAA") || strings.Contains(joined, "oceans cover most") {
		t.Errorf("truncated fragment survived:\n%s", joined)
	}
	if !strings.Contains(joined, "Tips for getting started") {
		t.Errorf("non-duplicate junk row was deleted:\n%s", joined)
	}
}

// TestDedupAgainstFrameOneLineFragment: the shortest real fragment — a header plus a
// single truncated wrapped row (~11 words) — must be dropped (this survived the old
// 12-word gate in live testing).
func TestDedupAgainstFrameOneLineFragment(t *testing.T) {
	para := "mountains rise where tectonic plates collide folding rock upward over millions of years into peaks that catch snow feeding rivers below"
	frame := append([]string{"⏺ MARKER-AAA"}, dfWrap(para, 90)...)
	fragment := []string{"⏺ MARKER-AAA", dfWrap(para, 60)[0]}
	junk := []string{"│                │ Tips for getting started │"}
	history := append(append(append([]string{}, fragment...), junk...), "")

	out := dedupAgainstFrame(history, frame)
	joined := strings.Join(out, "\n")
	if strings.Contains(joined, "MARKER-AAA") {
		t.Errorf("one-line truncated fragment survived:\n%s", joined)
	}
	if !strings.Contains(joined, "Tips for getting") {
		t.Errorf("junk row deleted:\n%s", joined)
	}
}

// TestDedupBlocksTruncatedPrefixFragment: a repaint fragment (10-11 words, final word
// cut mid-word) of an ALREADY-COMMITTED paragraph must be dropped even when the
// content is no longer in the live frame (the narrow-width overflow case).
func TestDedupBlocksTruncatedPrefixFragment(t *testing.T) {
	full := dfWrap("deserts cover roughly a fifth of earths land surface defined not by heat but by scarce rainfall some like antarcticas dry valleys are bitterly cold", 52)
	frag := []string{"MARKER-X", "  deserts cover roughly a fifth of earths land sur,"}
	distinct := []string{"  deserts cover roughly a fifth of mars land surface today"} // 10 words, diverges at word 8

	var history []string
	history = append(history, "MARKER-X")
	history = append(history, full...)
	history = append(history, "")
	history = append(history, frag...)
	history = append(history, "")
	history = append(history, distinct...)
	history = append(history, "")

	out := dedupBlocks(history)
	joined := strings.Join(out, "\n")
	if n := strings.Count(joined, "deserts cover roughly a fifth of earths land sur"); n != 1 {
		t.Errorf("truncated fragment not collapsed: %d occurrences (want 1 — the full paragraph)\n%s", n, joined)
	}
	if !strings.Contains(joined, "mars land surface") {
		t.Errorf("legitimately distinct paragraph deleted:\n%s", joined)
	}
}

// A list item's wrapped tail reads like another item's: item 4's "golf hotel
// … mike" row in the history, item 19's identical row on the screen (a real
// Claude Code list at 50 columns). The history row is the only copy of
// item 4's continuation, and a repaint re-emits rows from where a row
// begins: a match that begins inside a line ("4. T3L04" is not on the
// screen) is no repaint, and the row stays.
func TestDedupAgainstFrameKeepsARowThatOnlyContinuesLikeTheFrame(t *testing.T) {
	history := []string{
		"  4. T3L04 alpha bravo charlie delta echo foxtrot",
		"     golf hotel india juliet kilo lima mike",
		"     ENDT3L04",
	}
	screen := []string{
		"  19. T3L19 alpha bravo charlie delta echo foxtrot",
		"      golf hotel india juliet kilo lima mike",
		"      ENDT3L19",
	}
	out := dedupAgainstFrame(history, screen)
	if strings.Join(out, "\n") != strings.Join(history, "\n") {
		t.Errorf("a row of the history went, or moved:\n%s\nwant it unchanged:\n%s", strings.Join(out, "\n"), strings.Join(history, "\n"))
	}
	// The same rows re-emitted whole (a repaint of item 19) still go.
	stale := append([]string{"  3. T3L03 something else entirely here"}, screen...)
	out = dedupAgainstFrame(stale, screen)
	if strings.Join(out, "\n") != stale[0] {
		t.Errorf("a repaint of the frame's rows survived:\n%s", strings.Join(out, "\n"))
	}
}

// A passage printed at one width, then part of it again at another (the
// app's reprint of its frame after the phone turned), is read once by a
// joiner: the later rows, each a run of words already above, go; the copy in
// its place stays. A paragraph that only shares a few words stays, and so
// does a short line.
func TestDedupRepaintsCollapsesACopyAtAnotherWidth(t *testing.T) {
	para := "deserts cover roughly a fifth of earths land surface defined not by heat but by scarce rainfall and some like antarcticas dry valleys are bitterly cold places where almost nothing grows at all"
	wide := dfWrap(para, 104)
	narrow := dfWrap(para, 50)
	// The reprint starts mid-paragraph: the frame's top row.
	reprint := narrow[1:]
	other := dfWrap("deserts cover a fifth of mars where rainfall is unknown and the cold is of another kind entirely", 50)
	var history []string
	history = append(history, wide...)
	history = append(history, "")
	history = append(history, "MARK-BETWEEN")
	history = append(history, "")
	history = append(history, reprint...)
	history = append(history, "")
	history = append(history, other...)
	history = append(history, "", "a short line")

	out := dedupRepaints(history)
	joined := strings.Join(out, "\n")
	if n := strings.Count(joined, "antarcticas"); n != 1 {
		t.Errorf("the copy at the other width survived: %d occurrences\n%s", n, joined)
	}
	for _, keep := range []string{"MARK-BETWEEN", "mars where rainfall", "a short line", strings.Join(wide, "\n")} {
		if !strings.Contains(joined, keep) {
			t.Errorf("lost %q:\n%s", keep, joined)
		}
	}
}

// A partial copy ABOVE the complete one (an earlier reprint, kept as the
// first in its place) must not eat the complete copy's tail: a paragraph is
// dropped whole or kept whole, so a sentence is never split between two
// places. Here the complete copy's first row has words the partial copy
// lacks, so it stays, all of it; the later partial copy goes.
func TestDedupRepaintsNeverSplitsAParagraph(t *testing.T) {
	para := "deserts cover roughly a fifth of earths land surface defined not by heat but by scarce rainfall and some like antarcticas dry valleys are bitterly cold places where almost nothing grows at all"
	wide := dfWrap(para, 104)
	narrow := dfWrap(para, 50)
	var history []string
	history = append(history, narrow[2:]...) // an earlier partial copy
	history = append(history, "", "MARK-A", "")
	history = append(history, wide...) // the complete copy
	history = append(history, "", "MARK-B", "")
	history = append(history, narrow[1:]...) // a later partial copy
	history = append(history, "")

	out := dedupRepaints(history)
	joined := strings.Join(out, "\n")
	if !strings.Contains(joined, strings.Join(wide, "\n")) {
		t.Errorf("the complete copy was cut:\n%s", joined)
	}
	// The complete copy wins: both partial copies go, the earlier one too
	// (the backward pass), so the passage is read exactly once.
	if n := strings.Count(joined, "antarcticas"); n != 1 {
		t.Errorf("want the complete copy alone (1 occurrence), both partial copies gone: %d\n%s", n, joined)
	}
}

// A reprint carries its headings: when its paragraphs go, a heading that
// appeared above verbatim and now sits next to a dropped paragraph goes
// too, or a join read a stack of headings with nothing under them (one per
// reprint: 42 "Section" headings for 8 sections in the browser rig). A
// heading in its first place, with its paragraph kept, stays.
func TestDedupRepaintsTakesAReprintsHeadingsWithIt(t *testing.T) {
	p1 := dfWrap("deserts cover roughly a fifth of earths land surface defined not by heat but by scarce rainfall and some like antarcticas dry valleys are bitterly cold", 104)
	p2 := dfWrap("mountains rise where tectonic plates collide folding rock upward over millions of years into peaks that catch snow feeding rivers below them", 104)
	var history []string
	history = append(history, "## 1. Deserts", "")
	history = append(history, p1...)
	history = append(history, "", "## 2. Mountains", "")
	history = append(history, p2...)
	history = append(history, "")
	// The reprint, narrower, headings and all.
	history = append(history, "## 1. Deserts", "")
	history = append(history, dfWrap("deserts cover roughly a fifth of earths land surface defined not by heat but by scarce rainfall and some like antarcticas dry valleys are bitterly cold", 50)...)
	history = append(history, "", "## 2. Mountains", "")
	history = append(history, dfWrap("mountains rise where tectonic plates collide folding rock upward over millions of years into peaks that catch snow feeding rivers below them", 50)...)
	history = append(history, "", "## 3. Rivers", "", "a short new section")

	out := dedupRepaints(history)
	joined := strings.Join(out, "\n")
	for _, h := range []string{"## 1. Deserts", "## 2. Mountains"} {
		if n := strings.Count(joined, h); n != 1 {
			t.Errorf("%s: %d copies, want 1 (the reprint's heading goes with its paragraph)\n%s", h, n, joined)
		}
	}
	if !strings.Contains(joined, "## 3. Rivers") || !strings.Contains(joined, "a short new section") {
		t.Errorf("a new heading or its text went:\n%s", joined)
	}
}

// The partial copy FIRST: the rows of a list that scrolled off before the
// phone turned (items 1 to 3 of 6, the frame's bottom cut mid-item, the
// app's status row glued under it), then the app's reprint of the whole
// list at the new width. The complete copy stays and the partial one goes
// (a reader finds every item once); the status row stays.
func TestDedupRepaintsKeepsTheCompleteCopyWhenThePartialCameFirst(t *testing.T) {
	item := func(n int) string {
		return fmt.Sprintf("%d. T4L%02d alpha bravo charlie delta echo foxtrot golf-T4L%02d hotel india juliet kilo lima mike ENDT4L%02d", n, n, n, n)
	}
	var history []string
	history = append(history, "● Reply for turn 4:", "")
	for n := 1; n <= 3; n++ {
		history = append(history, dfWrap(item(n), 50)...)
	}
	history = append(history, dfWrap(item(4), 50)[0]) // cut at the frame's bottom
	history = append(history, "✻ Cooked for 0s · done 9:33 PM", "", "❯ turn 4", "", "● Reply for turn 4:", "")
	for n := 1; n <= 6; n++ {
		history = append(history, item(n))
	}
	history = append(history, "", "Done with turn 4.", "")

	out := dedupRepaints(history)
	joined := strings.Join(out, "\n")
	for n := 1; n <= 6; n++ {
		if c := strings.Count(joined, fmt.Sprintf("ENDT4L%02d", n)); c != 1 {
			t.Errorf("item %d appears %d times, want once:\n%s", n, c, joined)
		}
	}
	if !strings.Contains(joined, item(1)) {
		t.Errorf("the complete copy (one row an item) is not the one kept:\n%s", joined)
	}
	for _, keep := range []string{"✻ Cooked for 0s", "❯ turn 4", "Done with turn 4."} {
		if !strings.Contains(joined, keep) {
			t.Errorf("lost %q:\n%s", keep, joined)
		}
	}
}

// A redraw after the terminal got shorter (a phone's keyboard) prints the
// frame's heading again under rows the old frame left: a join on a real
// session read "IV. The Letter", "stairs." (the tail of the paragraph
// before), a bare quote bar, then "IV. The Letter" with its text. The first
// heading and the debris go; a short line between two headings that is
// nowhere else is text, and stays.
func TestDedupRepaintsDropsAHeadingReprintedOverARedrawsDebris(t *testing.T) {
	before := dfWrap("the fog came in during the second week thick as wool at three in the morning while she wound the clockwork she heard it three slow knocks echoing up the spiral stairs.", 55)
	after := dfWrap("the next morning a fishing boat came into the cove its captain was a tired man with salt in his beard he climbed the rocks to thank her", 55)
	var history []string
	history = append(history, "III. The Knocking", "")
	history = append(history, before...)
	history = append(history, "", "", "IV. The Letter", "", "stairs.", "", "▎", "", "IV. The Letter", "")
	history = append(history, after...)
	history = append(history, "")
	out := dedupRepaints(history)
	joined := strings.Join(out, "\n")
	if n := strings.Count(joined, "IV. The Letter"); n != 1 {
		t.Errorf("the heading is read %d times, want once:\n%s", n, joined)
	}
	// "stairs." once: the paragraph's own last row, not the debris row too.
	if n := strings.Count(joined, "\nstairs.\n"); n != 1 || strings.Contains(joined, "▎") {
		t.Errorf("the redraw's debris stayed (\"stairs.\" rows %d):\n%s", n, joined)
	}
	for _, keep := range []string{"III. The Knocking", strings.Join(before, "\n"), strings.Join(after, "\n")} {
		if !strings.Contains(joined, keep) {
			t.Errorf("lost %q:\n%s", keep, joined)
		}
	}

	// Text between two copies of a heading, nowhere else: all of it stays.
	var text []string
	text = append(text, before...)
	text = append(text, "", "Note", "", "Checked twice.", "", "Note", "")
	text = append(text, after...)
	if got := dedupRepaints(text); len(got) != len(text) {
		t.Errorf("a line of its own between two headings was dropped:\n%s", strings.Join(got, "\n"))
	}
}

// The history of a real session (Claude Code 2.1.319 on a folding phone:
// the fold closed while it printed, then the keyboard opened and closed;
// testdata/dedup/fold-keyboard-history.txt, the agent's History() before
// any dedup, recorded from the session's ring). A join read "IV. The
// Letter", "stairs.", a bare quote bar, then "IV. The Letter" again. Once
// deduped as a join's history is (dedupBlocks, then dedupRepaints), the
// heading and its paragraph are read once, "stairs." is the row of its own
// paragraph only, and every paragraph of the story is there.
func TestDedupRepaintsOnAFoldAndKeyboardSession(t *testing.T) {
	data, err := os.ReadFile("testdata/dedup/fold-keyboard-history.txt")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	out := dedupRepaints(lines)
	plain := make([]string, len(out))
	for i, l := range out {
		plain[i] = strings.TrimSpace(dedupANSI.ReplaceAllString(l, ""))
	}
	count := func(row string) int {
		n := 0
		for _, l := range plain {
			if l == row {
				n++
			}
		}
		return n
	}
	joined := strings.Join(plain, "\n")
	// The bare quote bar once: the quote's own blank row, not the debris one.
	for row, want := range map[string]int{"IV. The Letter": 1, "stairs.": 1, "▎": 1, "III. The Knocking": 1} {
		if n := count(row); n != want {
			t.Errorf("%q is read %d times, want %d:\n%s", row, n, want, joined)
		}
	}
	if n := strings.Count(joined, "The next morning a fishing boat"); n != 1 {
		t.Errorf("the paragraph under the heading is read %d times, want once", n)
	}
	for _, keep := range []string{"The fog came in during the second week", "She took the lantern down", "Mara ran back up the stairs", "Saw your light just in time", "After he left, Mara finally opened the envelope", "You've met him by now, I expect"} {
		if !strings.Contains(joined, keep) {
			t.Errorf("lost %q", keep)
		}
	}
}

// The same session at the seam: the history as the agent holds it and the
// live screen under it (testdata/dedup/fold-keyboard-screen.txt, the 58
// rows the join painted last). The screen begins with "IV. The Letter"
// over its text, and the history ends on the heading and the redraw's
// debris: painted together, the heading read twice. The screen is never
// changed; the history's tail is.
func TestJoinReadsAHeadingAtTheSeamOnce(t *testing.T) {
	read := func(name string) []string {
		data, err := os.ReadFile("testdata/dedup/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	}
	history, screen := read("fold-keyboard-history.txt"), read("fold-keyboard-screen.txt")
	out := dedupAgainstFrame(dedupRepaints(history), screen)
	plain := func(rows []string) string {
		var b strings.Builder
		for _, l := range rows {
			b.WriteString(strings.TrimSpace(dedupANSI.ReplaceAllString(l, "")) + "\n")
		}
		return b.String()
	}
	painted := plain(out) + plain(screen)
	for row, want := range map[string]int{"\nIV. The Letter\n": 1, "\nstairs.\n": 1, "\nIII. The Knocking\n": 1, "\nV. The Last Winter\n": 1} {
		if n := strings.Count(painted, row); n != want {
			t.Errorf("%q is read %d times, want %d:\n%s", strings.TrimSpace(row), n, want, painted)
		}
	}
	if n := strings.Count(painted, "The next morning a fishing boat"); n != 1 {
		t.Errorf("the paragraph under the heading is read %d times, want once", n)
	}
	for _, keep := range []string{"The fog came in during the second week", "Mara ran back up the stairs", "Saw your light just in time", "You've met him by now, I expect", "The End."} {
		if !strings.Contains(painted, keep) {
			t.Errorf("lost %q", keep)
		}
	}
}
