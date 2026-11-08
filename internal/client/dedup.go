// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"regexp"
	"slices"
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
	return compactDropped(lines, dedupBlocksMask(lines))
}

// dedupBlocksMask marks the lines dedupBlocks drops: the later verbatim
// copies, and the blank each absorbs. dedupRepaints starts from it, so the
// rules that read a dropped neighbour (a heading left on its own) see the
// copies this one took.
func dedupBlocksMask(lines []string) []bool {
	seen := make(map[string]bool)
	var seenParas [][]string
	drop := make([]bool, len(lines))
	i := 0
	for i < len(lines) {
		if dedupBlank(lines[i]) {
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
			for x := i; x < j; x++ {
				drop[x] = true
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
			for x := i; x < j; x++ {
				drop[x] = true
			}
			i = j
			continue
		}
		if len(words) >= dedupMinParaWords {
			seen[key] = true
			seenParas = append(seenParas, words)
		}
		i = j
	}
	return drop
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
	return coverageMask(history, shingles, refWords, false)
}

// liveFrameDropMask is frameMatchedDropMask against the live screen at a
// join, where a matched stretch that begins inside a line starts at the
// next line instead of being given up (coverageMask, fromNextLine): the
// screen's top row can be a wrapped tail ("beard. He climbed the rocks…"),
// and the copy above it then matches from the last word of the row before
// it. Only here: between copies in the history (dedupRepaints) the stretch
// given up whole is what keeps the complete copy over a partial one.
func liveFrameDropMask(history []string, refWords []string) []bool {
	set := make(map[string]bool, len(refWords))
	for _, w := range refWords {
		set[w] = true
	}
	return coverageMask(history, buildShingles(refWords), set, true)
}

func coverageMask(history []string, shingles map[shingle]bool, refWords map[string]bool, fromNextLine bool) []bool {
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
	// alpha … foxtrot / golf hotel … mike") — the only copy of it. With
	// fromNextLine (liveFrameDropMask) such a stretch loses only its words up
	// to the next line's start, and what is left is gated again.
	for i := 0; i < len(covered); {
		if !covered[i] {
			i++
			continue
		}
		j := i
		for j < len(covered) && covered[j] {
			j++
		}
		if i > 0 && lineOf[i-1] == lineOf[i] {
			if !fromNextLine {
				for k := i; k < j; k++ {
					covered[k] = false
				}
				i = j
				continue
			}
			for i < j && lineOf[i] == lineOf[i-1] {
				covered[i] = false
				i++
			}
		}
		if j-i < dedupFrameRunMin {
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
// Then the heading rules (dropOrphanHeadings, dropHeadingDebris) are read
// across the seam: the history's tail and the screen as one text, the
// screen's rows never dropped. A frame that begins with the heading the
// history ends on (a redraw after the terminal got shorter: "IV. The
// Letter", the debris it left, then the screen's "IV. The Letter" over its
// text) otherwise read the heading twice, since neither side alone holds
// both copies.
func dedupAgainstFrame(history, screen []string) []string {
	var fw []string
	for _, r := range screen {
		fw = append(fw, dedupWords(r)...)
	}
	drop := liveFrameDropMask(history, fw)
	both := append(append(make([]string, 0, len(history)+len(screen)), history...), screen...)
	mask := append(append(make([]bool, 0, len(both)), drop...), make([]bool, len(screen))...)
	paras := dedupParagraphs(both)
	dropOrphanHeadings(both, paras, mask)
	dropHeadingDebris(both, paras, mask)
	for i := range history {
		drop[i] = mask[i]
	}
	dropSeamDebris(history, drop)
	return compactDropped(history, drop)
}

// dedupRepaints drops paragraphs that re-emit text found elsewhere in the
// history. An inline TUI's reprint of its frame at a new width lands in
// history as a copy of paragraphs already there, wrapped differently: no
// paragraph key pairs it (dedupBlocks wants the whole paragraph, verbatim),
// and once the app has printed more it is no longer the live frame
// (dedupAgainstFrame). A viewer that joined then read the same passage
// twice, at two widths. Which copy is whole depends on the order: the rows
// that scrolled off before a reprint are a partial first copy, the reprint
// below them complete; or the complete copy came first and the reprint is
// the frame's tail. So a paragraph goes when it is a reprint of the text
// kept BEFORE it (reprintOf), and then, over what is left, of the text kept
// AFTER it. A paragraph goes whole or stays whole: dropping some of its
// lines split a sentence between two places. The app's own rows (its
// prompt, its spinner and status rows: dedupChrome) bound paragraphs, since
// a frame's bottom lands them right under its last row of text, and a copy
// they are glued to would otherwise never match. A short paragraph (a
// heading) a dropped copy carried goes with it (dropOrphanHeadings); a
// heading printed twice with only a redraw's debris between goes with the
// debris (dropHeadingDebris).
func dedupRepaints(lines []string) []string {
	paras := dedupParagraphs(lines)
	// The verbatim copies first (dedupBlocks' rule), in the same mask.
	drop := dedupBlocksMask(lines)
	pass := func(order []int, reversed bool) {
		var src reprintSource
		for _, k := range order {
			p := paras[k]
			if drop[p.i] {
				continue
			}
			words := p.words(lines)
			if reversed {
				slices.Reverse(words)
			}
			if src.reprintOf(lines[p.i:p.j], words, reversed) {
				for x := p.i; x < p.j; x++ {
					drop[x] = true
				}
				continue
			}
			src.add(lines[p.i:p.j], words)
		}
	}
	forward := make([]int, len(paras))
	backward := make([]int, len(paras))
	for k := range paras {
		forward[k], backward[len(paras)-1-k] = k, k
	}
	pass(forward, false)
	pass(backward, true)
	dropOrphanHeadings(lines, paras, drop)
	dropHeadingDebris(lines, paras, drop)
	return compactDropped(lines, drop)
}

// reprintSource is the text kept so far in one pass of dedupRepaints: its
// words as one stream (in reading order, or reversed for the pass from the
// bottom), where each word starts in it, and its rows as printed.
type reprintSource struct {
	words []string
	at    map[string][]int
	rows  map[string]bool
}

func (s *reprintSource) add(rows, words []string) {
	if s.at == nil {
		s.at, s.rows = map[string][]int{}, map[string]bool{}
	}
	for _, w := range words {
		s.at[w] = append(s.at[w], len(s.words))
		s.words = append(s.words, w)
	}
	for _, r := range rows {
		if t := dedupRow(r); t != "" {
			s.rows[t] = true
		}
	}
}

// reprintOf reports whether a paragraph (its rows, and its words in the
// pass's order) is a reprint of the source at another width: all its words,
// at least a matched run's worth, in one unbroken run of the source, the
// last word allowed a resize's cut ("unwrapped" as "unwrapp"); and wrapped
// otherwise than the source, some row of it printed nowhere in it. Words
// gathered from separate places are not a copy (two paragraphs that share
// halves with a third), and rows printed again as they were (a `tail` of a
// file just shown) are the program saying it again, not a reprint.
func (s *reprintSource) reprintOf(rows, words []string, reversed bool) bool {
	if len(words) < dedupFrameRunMin || s.at == nil {
		return false
	}
	rewrapped := false
	for _, r := range rows {
		if t := dedupRow(r); t != "" && !s.rows[t] {
			rewrapped = true
			break
		}
	}
	if !rewrapped {
		return false
	}
	cut := len(words) - 1 // the paragraph's last word, wherever the pass puts it
	if reversed {
		cut = 0
	}
	same := func(a, b string, k int) bool {
		return a == b || (k == cut && len(a) >= 3 && len(b) >= 3 && (strings.HasPrefix(a, b) || strings.HasPrefix(b, a)))
	}
	starts := s.at[words[0]]
	if reversed {
		// The first word is the cut one: try every place the second is.
		starts = nil
		for _, i := range s.at[words[1]] {
			if i > 0 {
				starts = append(starts, i-1)
			}
		}
	}
	for _, i := range starts {
		if i+len(words) > len(s.words) {
			continue
		}
		ok := true
		for k, w := range words {
			if !same(w, s.words[i+k], k) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// dedupRow is a row as read: its text without styling or the space around it.
func dedupRow(s string) string {
	if len(dedupWords(s)) == 0 {
		return ""
	}
	return strings.TrimSpace(dedupANSI.ReplaceAllString(s, ""))
}

// dedupPara is one paragraph of history: lines[i:j].
type dedupPara struct{ i, j int }

func (p dedupPara) words(lines []string) []string {
	var w []string
	for _, l := range lines[p.i:p.j] {
		w = append(w, dedupWords(l)...)
	}
	return w
}

// dedupParagraphs splits history into paragraphs: runs of lines between
// blank ones, with the app's own rows (dedupChrome) paragraphs of their own.
func dedupParagraphs(lines []string) []dedupPara {
	var paras []dedupPara
	for i := 0; i < len(lines); {
		if dedupBlank(lines[i]) {
			i++
			continue
		}
		if dedupChrome(lines[i]) {
			paras = append(paras, dedupPara{i, i + 1})
			i++
			continue
		}
		j := i
		for j < len(lines) && !dedupBlank(lines[j]) && !dedupChrome(lines[j]) {
			j++
		}
		paras = append(paras, dedupPara{i, j})
		i = j
	}
	return paras
}

// dedupChrome says a line is the app's own furniture, not its text: a
// prompt, a spinner or status row (Claude Code's "❯", "✻ Worked for 3s",
// "⏵⏵ auto mode on"), which the app draws under its last row of text.
func dedupChrome(s string) bool {
	t := strings.TrimSpace(dedupANSI.ReplaceAllString(s, ""))
	for _, g := range []string{"❯", "⏵", "✻", "✳", "✢", "✶", "✽"} {
		if strings.HasPrefix(t, g) {
			return true
		}
	}
	return false
}

// dropOrphanHeadings drops, from the lines marked for dropping, the short
// paragraphs a dropped reprint carried with it: a heading, a rule, a label
// (too short for the word match) that has appeared elsewhere verbatim and
// sits next to a dropped paragraph. Left, a join read a stack of headings
// with nothing under them, one per reprint. A short paragraph with kept
// neighbours, or one seen nowhere else, stays.
func dropOrphanHeadings(lines []string, paras []dedupPara, drop []bool) {
	count := map[string]int{}
	keys := make([]string, len(paras))
	for k, p := range paras {
		keys[k] = strings.Join(p.words(lines), "\x00")
		if !drop[p.i] && keys[k] != "" {
			count[keys[k]]++
		}
	}
	dropped := func(k int) bool { return k >= 0 && k < len(paras) && drop[paras[k].i] }
	for k, p := range paras {
		if drop[p.i] || keys[k] == "" || len(strings.Split(keys[k], "\x00")) >= dedupMinParaWords {
			continue
		}
		if count[keys[k]] > 1 && (dropped(k-1) || dropped(k+1)) {
			for x := p.i; x < p.j; x++ {
				drop[x] = true
			}
			count[keys[k]]--
		}
	}
}

// dropHeadingDebris drops a heading printed twice with only debris between
// the copies: a redraw after the terminal got shorter (a phone's keyboard)
// prints the frame's heading again under rows the old frame left behind, so
// a join read "IV. The Letter", a row of the paragraph before ("stairs."),
// a bare quote bar, then "IV. The Letter" with its text. Nothing between
// the copies is a paragraph of its own: debris is a row with no words, or a
// short paragraph whose words lie, in order, inside a kept paragraph of
// full length (a row's tail printed on its own). The first copy and the
// debris go; the second copy, with its text, stays. A short paragraph
// between the copies that is nowhere else is text, and everything stays.
func dropHeadingDebris(lines []string, paras []dedupPara, drop []bool) {
	keys := make([]string, len(paras))
	var long [][]string
	for k, p := range paras {
		w := p.words(lines)
		keys[k] = strings.Join(w, "\x00")
		if !drop[p.i] && len(w) >= dedupMinParaWords {
			long = append(long, w)
		}
	}
	debris := func(k int) bool {
		if keys[k] == "" {
			return true
		}
		w := paras[k].words(lines)
		if len(w) >= dedupMinParaWords {
			return false
		}
		for _, l := range long {
			if wordsContainRun(l, w) {
				return true
			}
		}
		return false
	}
	kept := map[string]int{}
	for k, p := range paras {
		if !drop[p.i] {
			kept[keys[k]]++
		}
	}
	for k, p := range paras {
		if keys[k] == "" || len(strings.Split(keys[k], "\x00")) >= dedupMinParaWords {
			continue
		}
		// The next copy, with only debris on the way to it. Paragraphs
		// dropped already (the copies a reprint carried) are passed over; a
		// heading dropped already (an orphan) still sheds its debris; a next
		// copy dropped itself counts when a copy is kept elsewhere, so the
		// heading is still read.
		seen := 0
		for n := k + 1; n < len(paras) && seen <= dedupDebrisParas; n++ {
			if keys[n] == keys[k] {
				need := 2
				if drop[p.i] {
					need = 1
				}
				if drop[paras[n].i] && kept[keys[k]] < need {
					break
				}
				if !drop[p.i] {
					kept[keys[k]]--
				}
				for x := p.i; x < paras[n].i; x++ {
					drop[x] = true
				}
				break
			}
			if drop[paras[n].i] {
				continue
			}
			if !debris(n) {
				break
			}
			seen++
		}
	}
}

// dedupDebrisParas is the most debris paragraphs a redraw leaves between a
// heading and its reprint: a frame's few stale rows, each a paragraph.
const dedupDebrisParas = 4

// wordsContainRun reports whether frag appears in words, in order and
// unbroken.
func wordsContainRun(words, frag []string) bool {
	if len(frag) == 0 || len(frag) > len(words) {
		return false
	}
	for i := 0; i+len(frag) <= len(words); i++ {
		if slices.Equal(words[i:i+len(frag)], frag) {
			return true
		}
	}
	return false
}

// dropSeamDebris drops, from the end of the history (only blank rows between
// them and the screen), rows that carry no words: a bare quote bar or a box
// edge a redraw left after the old frame's text ("…salt in his beard.", then
// "▎", then the screen's "beard. He climbed…"). One stays when the row with
// words above it begins with the same mark: a quote's own closing bar.
func dropSeamDebris(history []string, drop []bool) {
	lead := func(s string) string {
		t := strings.TrimSpace(dedupANSI.ReplaceAllString(s, ""))
		for _, r := range t {
			return string(r)
		}
		return ""
	}
	var tail []int
	above := -1
	for i := len(history) - 1; i >= 0; i-- {
		if drop[i] || dedupBlank(history[i]) {
			continue
		}
		if len(dedupWords(history[i])) > 0 {
			above = i
			break
		}
		tail = append(tail, i)
	}
	mark := ""
	if above >= 0 {
		mark = lead(history[above])
	}
	for _, i := range tail {
		if l := lead(history[i]); l != "" && l != mark {
			drop[i] = true
		}
	}
}
