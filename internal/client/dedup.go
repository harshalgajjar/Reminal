// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"regexp"
	"strings"
)

// The reconnect scrollback-duplication bug: inline TUIs (Claude Code / Ink) repaint
// their ENTIRE visible transcript on a SIGWINCH resize, and because the app pre-wraps
// its own output (it emits each row already broken to the terminal width — verified: no
// emitted run ever exceeds the column count), each repaint re-emits a full copy of recent
// history, re-wrapped to the new width. Over a session of resizes/reconnects a whole
// transcript block can end up stamped many times over.
//
// dedupBlocks removes those re-emitted copies from rebuilt history. It works on whole
// PARAGRAPHS (maximal runs of non-blank rows), compared by their alphanumeric word
// sequence:
//
//   - WORD-level, not line-level, because the app re-wraps on resize: the 119-col copy
//     and the 58-col copy of a paragraph have different line breaks but identical words.
//   - Alphanumeric-only, so per-line chrome (the `▎` quote-bar, `---`, tree glyphs) —
//     which lands at wrap-dependent positions — is dropped and doesn't defeat the match.
//   - PARAGRAPH granularity (not sliding word-runs) so a single drifting line inside a
//     frame (a spinner, a token counter) only spares its own paragraph, and — crucially —
//     so we never collapse a short phrase that legitimately recurs. Real sessions repeat
//     text (successive drafts, retried commands); only a substantial VERBATIM paragraph
//     repeating is an unambiguous re-emit.
//
// It keeps the FIRST occurrence (the copy in its original scrollback position, preserving
// reading order) and drops later stamped copies. Conservative: a paragraph shorter than
// dedupMinParaWords is never deduped, so headings, prompts, and one-liners always survive.
const dedupMinParaWords = 12

var (
	dedupANSI = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]|\x1b\][^\x07]*(?:\x07|\x1b\\)`)
	dedupWord = regexp.MustCompile(`[\p{L}\p{N}]+`)
)

// dedupWords lowercases a (possibly styled) line and returns only its alphanumeric word
// tokens — the wrap-invariant content of the line.
func dedupWords(s string) []string {
	return dedupWord.FindAllString(strings.ToLower(dedupANSI.ReplaceAllString(s, "")), -1)
}

func dedupBlank(s string) bool {
	return strings.TrimSpace(dedupANSI.ReplaceAllString(s, "")) == ""
}

// dedupBlocks returns history with later verbatim copies of substantial paragraphs
// removed. Blank separators around a dropped paragraph are absorbed so the surviving
// history doesn't accumulate blank gaps.
func dedupBlocks(lines []string) []string {
	seen := make(map[string]bool)
	var seenParas [][]string
	out := make([]string, 0, len(lines))
	i := 0
	for i < len(lines) {
		if dedupBlank(lines[i]) {
			out = append(out, lines[i])
			i++
			continue
		}
		// Gather one paragraph: consecutive non-blank rows.
		j := i
		var words []string
		for j < len(lines) && !dedupBlank(lines[j]) {
			words = append(words, dedupWords(lines[j])...)
			j++
		}
		key := strings.Join(words, "\x00")
		if len(words) >= dedupFrameRunMin && len(words) < dedupMinParaWords && isTruncatedPrefix(words, seenParas) {
			// Repaint FRAGMENT: an interrupted re-emit commits just a paragraph's
			// head — often with its final word cut mid-word — too short for the
			// verbatim key above and possibly out of the live frame. A >=10-word
			// prefix (last word tolerant) of a paragraph we already emitted is
			// unambiguous: drop it like any other re-emitted copy.
			if j < len(lines) && dedupBlank(lines[j]) {
				j++
			}
			i = j
			continue
		}
		if len(words) >= dedupMinParaWords && seen[key] {
			// Re-emitted copy: drop it and absorb the ONE trailing blank that followed
			// it, so the blank preceding it survives as the separator between its
			// neighbours (removing that leading blank instead would merge them).
			if j < len(lines) && dedupBlank(lines[j]) {
				j++
			}
			i = j
			continue
		}
		if len(words) >= dedupMinParaWords {
			seen[key] = true
			seenParas = append(seenParas, words)
		}
		out = append(out, lines[i:j]...)
		i = j
	}
	return out
}

// isTruncatedPrefix reports whether frag (>= dedupFrameRunMin words) is a prefix of
// any paragraph in seen, tolerating a mid-word cut of its final token ("sur" matching
// "surface"). Word-exact on everything before the last token, so legit short
// paragraphs that merely open with similar phrasing don't reach it (they'd have to
// share the first 9+ words verbatim AND the 10th as a prefix).
func isTruncatedPrefix(frag []string, seen [][]string) bool {
	n := len(frag)
	for _, para := range seen {
		if len(para) < n {
			continue
		}
		match := true
		for k := 0; k < n-1; k++ {
			if para[k] != frag[k] {
				match = false
				break
			}
		}
		if match && strings.HasPrefix(para[n-1], frag[n-1]) {
			return true
		}
	}
	return false
}

// Frame-matched dedup: the second, width-INDEPENDENT half of the resize story.
//
// dedupBlocks above only collapses paragraphs re-emitted at the SAME width (identical
// word key). But an inline TUI repaints its frame on EVERY SIGWINCH — so a session
// that bounced between widths stamps copies of the CURRENT frame's content into
// committed scrollback re-wrapped differently each time. Those copies have identical
// words but different paragraph boundaries (blank/wrap structure shifts with width),
// so no paragraph-key scheme can pair them with each other.
//
// What CAN be paired is each copy against the live frame itself: every stale copy is,
// by definition, a re-emission of content the app is showing RIGHT NOW. So we match
// committed paragraphs against the current screen's word stream using word shingles
// (order-preserving 8-word windows — immune to wrapping, blank lines, and per-line
// chrome). A paragraph whose shingles are almost entirely present in the frame is a
// stale repaint copy: drop it. This is provably lossless — the dropped words are still
// painted, in the frame, at the snapshot's bottom; nothing the user could scroll to
// disappears. Content the app has scrolled PAST (no longer in its frame) is never
// touched, no matter how similar.
// dedupFrameShingle is the matched-run window: 8 words, below the run gate so
// every gated stretch contains several windows.
const dedupFrameShingle = 8

// dedupFrameRunMin gates matched runs at one full shingle: an ordered 8-word match
// against a specific frame is already near-certain re-emission, and resize
// truncation corrupts the LAST word of most committed rows ("unwrapped"→"unwrapp",
// "land"→"landg"), shattering runs to ~9 words — a higher gate let whole stale
// copies survive. Coincidence is further excluded where this matters most: the
// resize-segment path only drops lines that ALSO provably re-occur later.
const dedupFrameRunMin = 8

// frameMatchedDropMask marks the lines of history that are re-emissions of refWords
// (an ordered word stream — a captured or live app frame).
//
// Granularity is LINE-within-matched-RUN, not whole paragraphs: resize repaints get
// interrupted mid-frame, committing truncated fragments that sit contiguous with
// unrelated leftover rows (old welcome-box borders etc.). A whole-paragraph coverage
// test dilutes on that junk and misses the fragment. Instead we mark every history
// word that lies inside an 8-word window also present (in order) in the reference
// stream, keep only maximal covered stretches of >= dedupFrameRunMin words, and mark
// exactly the lines ALL of whose words are covered. Junk rows with words the frame
// doesn't contain survive (they're not duplicates); short or coincidental overlaps
// never reach the stretch gate.
func frameMatchedDropMask(history []string, refWords []string) []bool {
	set := make(map[string]bool, len(refWords))
	for _, w := range refWords {
		set[w] = true
	}
	return coverageDropMask(history, buildShingles(refWords), set)
}

// shingle is one 8-word window of a word stream. Kept as the words
// themselves, not joined into one string: looking one up then allocates
// nothing (coverageDropMask looks up one for every word of the history). The
// same windows match as joined ones would: a word (dedupWord) never holds the
// \x00 they were joined with.
type shingle [dedupFrameShingle]string

// buildShingles indexes an ordered word stream by its 8-word windows.
func buildShingles(words []string) map[shingle]bool {
	m := make(map[shingle]bool, len(words))
	addShingles(m, words)
	return m
}

// addShingles extends an existing shingle index with another word stream.
func addShingles(m map[shingle]bool, words []string) {
	for i := 0; i+dedupFrameShingle <= len(words); i++ {
		m[shingle(words[i:i+dedupFrameShingle])] = true
	}
}

// coverageDropMask marks history lines whose words are covered by matched runs
// against a pre-built shingle index (see frameMatchedDropMask's doc above).
func coverageDropMask(history []string, shingles map[shingle]bool, refWords map[string]bool) []bool {
	mask := make([]bool, len(history))
	if len(shingles) == 0 {
		return mask
	}

	// History word stream, remembering which line each word came from.
	var hw []string
	var lineOf []int
	for li, l := range history {
		for _, w := range dedupWords(l) {
			hw = append(hw, w)
			lineOf = append(lineOf, li)
		}
	}
	covered := make([]bool, len(hw))
	for i := 0; i+dedupFrameShingle <= len(hw); i++ {
		if shingles[shingle(hw[i:i+dedupFrameShingle])] {
			for k := i; k < i+dedupFrameShingle; k++ {
				covered[k] = true
			}
		}
	}
	// Length gate: a covered stretch shorter than dedupFrameRunMin is a phrase that
	// may legitimately recur, not an unambiguous re-emit — uncover it.
	// Start gate: a repaint re-emits rows, so a stretch that is one begins
	// where a history line begins. One that begins inside a line is a line
	// that only CONTINUES like a row of the frame: the wrapped tail of a
	// list item, the same words as the tail of another item on the screen
	// ("4. T3L04 alpha … foxtrot / golf hotel … mike" against "19. T3L19
	// alpha … foxtrot / golf hotel … mike") — the only copy of it.
	for i := 0; i < len(covered); {
		if !covered[i] {
			i++
			continue
		}
		j := i
		for j < len(covered) && covered[j] {
			j++
		}
		if j-i < dedupFrameRunMin || (i > 0 && lineOf[i-1] == lineOf[i]) {
			for k := i; k < j; k++ {
				covered[k] = false
			}
		}
		i = j
	}

	wordCount := make([]int, len(history))
	covCount := make([]int, len(history))
	for k, li := range lineOf {
		wordCount[li]++
		if covered[k] {
			covCount[li]++
		}
	}
	// Locate each line's single uncovered word (when there is exactly one).
	lastWord := make([]string, len(history))
	uncovered := make([]string, len(history))
	uncoveredIsLast := make([]bool, len(history))
	{
		idx := make([]int, len(history)) // words seen per line while walking the stream
		for k, li := range lineOf {
			idx[li]++
			lastWord[li] = hw[k]
			if !covered[k] {
				uncovered[li] = hw[k]
				uncoveredIsLast[li] = idx[li] == wordCount[li]
			}
		}
	}
	for li := range history {
		if wordCount[li] == 0 {
			continue
		}
		if covCount[li] == wordCount[li] {
			mask[li] = true
			continue
		}
		// Splice tolerance — NARROW by design: resize truncation corrupts a row's
		// FINAL word into a prefix/extension of the real one ("unwrapped"→"unwrapp",
		// "land"→"landg"). Forgive exactly that: one uncovered word, at the END of
		// the line, that is a truncation-relative of a real reference word. An
		// arbitrary differing token (a list number, an ID) is NOT forgiven — that
		// is what distinguishes a corrupted copy from genuinely distinct content.
		if wordCount[li] >= 5 && covCount[li] == wordCount[li]-1 && uncoveredIsLast[li] &&
			spliceRelative(uncovered[li], refWords) {
			mask[li] = true
		}
	}
	return mask
}

// spliceRelative reports whether w looks like a truncation artifact of some real
// reference word: one is a strict prefix of the other, both at least 3 chars.
func spliceRelative(w string, refWords map[string]bool) bool {
	if len(w) < 3 {
		return false
	}
	for r := range refWords {
		if len(r) >= 3 && r != w && (strings.HasPrefix(r, w) || strings.HasPrefix(w, r)) {
			return true
		}
	}
	return false
}

// compactDropped removes mask-marked lines, absorbing ONE blank after each dropped
// block so separators don't pile up.
func compactDropped(lines []string, drop []bool) []string {
	out := make([]string, 0, len(lines))
	justDropped := false
	for i, l := range lines {
		if drop[i] {
			justDropped = true
			continue
		}
		if justDropped && dedupBlank(l) && (len(out) == 0 || dedupBlank(out[len(out)-1])) {
			justDropped = false
			continue
		}
		justDropped = false
		out = append(out, l)
	}
	return out
}

// dedupAgainstFrame drops committed lines that are stale re-emissions of the app's
// current frame (see block comment above). screen is the live screen's rows.
func dedupAgainstFrame(history, screen []string) []string {
	var fw []string
	for _, r := range screen {
		fw = append(fw, dedupWords(r)...)
	}
	return compactDropped(history, frameMatchedDropMask(history, fw))
}

// dedupRepaints drops lines that re-emit text already above them. An inline
// TUI's reprint of its frame at a new width lands in history as a PARTIAL
// copy of paragraphs already there, wrapped differently: no paragraph key
// pairs it (dedupBlocks wants the whole paragraph, verbatim), and once the
// app has printed more it is no longer the live frame (dedupAgainstFrame).
// A viewer that joined then read the same passage twice, at two widths.
// Each paragraph is matched against the 8-word windows of everything kept
// before it, as a frame is (coverageDropMask): a line whose words are all
// inside matched runs that begin where a line begins goes; a line with a
// word of its own, or a short one, stays. The first copy, in its place, is
// what survives.
func dedupRepaints(lines []string) []string {
	shingles := map[shingle]bool{}
	refWords := map[string]bool{}
	drop := make([]bool, len(lines))
	for i := 0; i < len(lines); {
		if dedupBlank(lines[i]) {
			i++
			continue
		}
		j := i
		for j < len(lines) && !dedupBlank(lines[j]) {
			j++
		}
		para := lines[i:j]
		var kept []string
		for k, d := range coverageDropMask(para, shingles, refWords) {
			if d {
				drop[i+k] = true
			} else {
				kept = append(kept, dedupWords(para[k])...)
			}
		}
		addShingles(shingles, kept)
		for _, w := range kept {
			refWords[w] = true
		}
		i = j
	}
	return compactDropped(lines, drop)
}
