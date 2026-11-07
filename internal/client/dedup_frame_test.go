// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
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
