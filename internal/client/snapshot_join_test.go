// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

// The join's snapshot made outside screenMu, from a copy and three kept
// stages (joinInputs, joinHistory, paintSnapshot), is byte for byte what the
// old path made under the lock: these ref* functions are that path as it was
// (main e3c9c03f), verbatim but for their names — with the dedup helpers
// that changed since (the shingle index's keys) as they were too. The ones
// that did not (dedupBlocks, dedupWords, compactDropped, regionWords) are
// shared. Don't edit them to match a change; a change that makes them differ
// is the regression this guards.

import (
	"fmt"
	"math/rand"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/x/vt"
)

func refRenderScrollback(e *vt.Emulator, maxLines int) []string {
	lines := e.Scrollback().Lines()
	if maxLines > 0 && len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	rendered := make([]string, len(lines))
	for i, ln := range lines {
		rendered[i] = strings.TrimRight(ln.Render(), " ")
	}
	return rendered
}

func refSnapshotHistory(a *Agent) []string {
	lines := refRenderScrollback(a.screen, 0) // uncapped: segment indices are absolute
	if !dedupStrict {                         // mirrors dedupHistory's default rules
		if drop := refDropResizeRepaints(a, lines); drop != nil {
			lines = compactDropped(lines, drop)
		}
		if a.scrollbackLines > 0 && len(lines) > a.scrollbackLines {
			lines = lines[len(lines)-a.scrollbackLines:]
		}
		return refDedupAgainstFrame(dedupRepaintsCovered(lines), strings.Split(a.screen.Render(), "\n"))
	}
	// Mirrors dedupHistory, given the segments joinInputs passes: the lines
	// that can be reprints, carried through the segment pass and the trim.
	var segs []resizeSeg
	if !a.staleSegs(len(lines)) {
		segs = a.resizeSegs
	}
	reprints := repaintLines(lines, segs)
	if drop := refDropResizeRepaints(a, lines); drop != nil {
		lines, reprints = compactDroppedAlong(lines, drop, reprints)
	}
	if a.scrollbackLines > 0 && len(lines) > a.scrollbackLines {
		k := len(lines) - a.scrollbackLines
		lines, reprints = lines[k:], reprints[k:]
	}
	lines = dedupRepaintsIn(lines, reprints)
	return refDedupAgainstFrame(lines, strings.Split(a.screen.Render(), "\n"))
}

func refDropResizeRepaints(a *Agent, lines []string) []bool {
	if len(a.resizeSegs) < 2 {
		return nil
	}
	if a.scrollbackLines > 0 && len(lines) >= a.scrollbackLines {
		a.resizeSegs = nil // eviction possible: indices stale, start fresh
		return nil
	}
	drop := make([]bool, len(lines))
	// Existence corpus, built newest-to-oldest: a row may be dropped only if its
	// content provably appears again LATER — in a newer segment's region or on the
	// live screen. Stale repaint copies always do (that's what makes them stale);
	// a row pushed off the screen by a repaint (content ABOVE the app's frame,
	// whose pushed copy is the ONLY copy) never does, so it can never be lost —
	// even for apps whose repaint covers less than the captured screen.
	var scw []string
	for _, r := range strings.Split(a.screen.Render(), "\n") {
		scw = append(scw, dedupWords(r)...)
	}
	last := a.resizeSegs[len(a.resizeSegs)-1]
	corpus := refBuildShingles(scw)
	corpusWords := make(map[string]bool, len(scw))
	addWords := func(ws []string) {
		for _, w := range ws {
			corpusWords[w] = true
		}
	}
	addWords(scw)
	lastRegion := regionWords(lines, last.start, len(lines))
	refAddShingles(corpus, lastRegion)
	addWords(lastRegion)
	for i := len(a.resizeSegs) - 2; i >= 0; i-- {
		seg := a.resizeSegs[i]
		end := seg.start + seg.maxOver
		if nxt := a.resizeSegs[i+1].start; nxt < end {
			end = nxt
		}
		if end > len(lines) {
			end = len(lines)
		}
		if seg.start < 0 || seg.start >= end {
			continue
		}
		capMask := refFrameMatchedDropMask(lines[seg.start:end], seg.words)
		laterMask := refCoverageDropMask(lines[seg.start:end], corpus, corpusWords)
		for k := range capMask {
			if capMask[k] && laterMask[k] {
				drop[seg.start+k] = true
			}
		}
		// This segment's region is now "later" for every older segment.
		segRegion := regionWords(lines, seg.start, end)
		refAddShingles(corpus, segRegion)
		addWords(segRegion)
	}
	return drop
}

func refBuildSnapshot(e *vt.Emulator, history, screenRows []string, maxBytes int, reserveLastRow bool) string {
	alt := e.IsAltScreen()
	width, height := e.Width(), e.Height()
	if alt {
		// The full-screen app owns the view; the rebuild only reconstructs the
		// main buffer behind it, which is already folded into history.
		screenRows = nil
	}

	// Byte budget: walk newest→oldest, keep lines until we'd exceed
	// maxBytes, then drop the older remainder.
	if maxBytes > 0 && len(history) > 0 {
		total, start := 0, len(history)
		for i := len(history) - 1; i >= 0; i-- {
			total += len(history[i]) + 2 // +CRLF
			if total > maxBytes {
				break
			}
			start = i
		}
		history = history[start:]
	}

	// Exactly `height` rows for the screen, padded if the source omits
	// trailing blank lines, so the viewport lines up precisely.
	fromRebuild := screenRows != nil
	var screen []string
	if fromRebuild {
		screen = append([]string(nil), screenRows...)
	} else {
		screen = strings.Split(e.Render(), "\n")
	}
	if len(screen) > height {
		screen = screen[:height]
	}
	for len(screen) < height {
		screen = append(screen, "")
	}

	if !fromRebuild {
		// Seam-cut: a rebuilt history replays the same bytes the live screen
		// came from, so it may END with content the screen below paints again.
		// Trim that overlap or a (re)join shows the current screen twice in a
		// row. Unnecessary when the screen came from the same rebuild as the
		// history — the two are disjoint by construction.
		history = trimHistorySeam(history, screen)
	}
	// Blank the status-bar row so the snapshot never carries a transient bar (see
	// reserveLastRow). Main screen only — the alt buffer has no reserved row.
	if reserveLastRow && !alt && len(screen) > 0 {
		screen[len(screen)-1] = ""
	}

	// Height-tolerance: with no scrollback history to paint, drop trailing blank
	// screen rows before emitting. The row loop below paints its rows as a run of
	// `\r\n`-separated lines from the home position, so emitting all `height` rows
	// fills the viewport to the last line — and if the joining viewer's terminal
	// is even one row SHORTER than `height` at the instant it applies this paint
	// (a common transient while its size settles on connect), that overflow
	// scrolls the top rows — the prompt — up into scrollback, where they stay:
	// the viewer lands on a blank screen until the next keystroke forces a repaint.
	// The trailing rows are blank (the screen was already cleared), so not painting
	// them is visually identical while making the paint immune to a height
	// undershoot. Only safe with no history: when history IS present those trailing
	// rows are what push the live screen down into the viewport (history scrolls up
	// into scrollback), so keep the full-height paint then.
	if len(history) == 0 {
		for len(screen) > 0 && strings.TrimRight(screen[len(screen)-1], " ") == "" {
			screen = screen[:len(screen)-1]
		}
	}

	var b strings.Builder
	b.Grow(len(history)*width + height*width + 64)
	b.WriteString("\x1b[?25l")            // hide cursor while painting
	b.WriteString("\x1b[?1049l")          // ensure the main screen is active
	b.WriteString("\x1b[2J\x1b[3J\x1b[H") // clear screen + the viewer's old scrollback, home
	b.WriteString("\x1b[?7h")             // mirrors paintSnapshot: autowrap on throughout

	// History lines flow into the main buffer's scrollback as the screen
	// scrolls; each is newline-terminated so the next pushes it up.
	for _, ln := range history {
		b.WriteString(ln)
		b.WriteString("\x1b[0m\r\n")
	}

	if alt {
		// A full-screen app (vim, claude, …) is on the alternate screen. The
		// history above went into the main buffer (visible after the app
		// exits); now switch to the alt buffer and paint the live screen there.
		b.WriteString("\x1b[?1049h\x1b[2J\x1b[H")
	}
	for i, row := range screen {
		if i > 0 {
			b.WriteString("\r\n")
		}
		b.WriteString(row)
		b.WriteString("\x1b[0m")
	}

	pos := e.CursorPosition()
	if len(history) > 0 && !alt {
		// The screen is the bottom block of what was painted, and in a viewer
		// taller than it that block sits lower than its row numbers: an
		// absolute row put the cursor too high, and the shell's next output
		// (a prompt redrawn on the viewer's resize) was written over the
		// newest lines. So from where the paint ended — the screen's last
		// row — up to the cursor's row, then to its column: right in any
		// viewer's height.
		if up := len(screen) - 1 - pos.Y; up > 0 {
			fmt.Fprintf(&b, "\x1b[%dA", up)
		}
		fmt.Fprintf(&b, "\x1b[%dG", pos.X+1)
	} else {
		// Painted from the top-left: the rows are the viewer's own.
		fmt.Fprintf(&b, "\x1b[%d;%dH", pos.Y+1, pos.X+1) // restore cursor (1-based)
	}
	b.WriteString("\x1b[?25h") // show cursor
	return b.String()
}

func refBuildShingles(words []string) map[string]bool {
	m := make(map[string]bool, len(words))
	for i := 0; i+dedupFrameShingle <= len(words); i++ {
		m[strings.Join(words[i:i+dedupFrameShingle], "\x00")] = true
	}
	return m
}

func refAddShingles(m map[string]bool, words []string) {
	for i := 0; i+dedupFrameShingle <= len(words); i++ {
		m[strings.Join(words[i:i+dedupFrameShingle], "\x00")] = true
	}
}

func refCoverageDropMask(history []string, shingles map[string]bool, refWords map[string]bool) []bool {
	return refCoverageMask(history, shingles, refWords, false)
}

func refCoverageMask(history []string, shingles map[string]bool, refWords map[string]bool, fromNextLine bool) []bool {
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
		if shingles[strings.Join(hw[i:i+dedupFrameShingle], "\x00")] {
			for k := i; k < i+dedupFrameShingle; k++ {
				covered[k] = true
			}
		}
	}
	// Length gate: a covered stretch shorter than dedupFrameRunMin is a phrase that
	// may legitimately recur, not an unambiguous re-emit — uncover it.
	for i := 0; i < len(covered); {
		if !covered[i] {
			i++
			continue
		}
		j := i
		for j < len(covered) && covered[j] {
			j++
		}
		// Mirrors coverageMask: a stretch starting inside a line is given up,
		// or with fromNextLine starts at the next line and is gated again.
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

func refFrameMatchedDropMask(history []string, refWords []string) []bool {
	set := make(map[string]bool, len(refWords))
	for _, w := range refWords {
		set[w] = true
	}
	return refCoverageDropMask(history, refBuildShingles(refWords), set)
}

func refDedupAgainstFrame(history, screen []string) []string {
	var fw []string
	for _, r := range screen {
		fw = append(fw, dedupWords(r)...)
	}
	set := make(map[string]bool, len(fw))
	for _, w := range fw {
		set[w] = true
	}
	drop := refCoverageMask(history, refBuildShingles(fw), set, true)
	dropSeamDebris(history, drop)
	return compactDropped(history, drop)
}

// refFrame is the old snapshotFrame's plaintext: everything under screenMu.
func refFrame(a *Agent) string {
	a.screenMu.Lock()
	defer a.screenMu.Unlock()
	budget := a.scrollbackBytes
	if budget <= 0 || budget > maxSnapshotPlaintext {
		budget = maxSnapshotPlaintext
	}
	return refBuildSnapshot(a.screen, refSnapshotHistory(a), nil, budget, false)
}

// resetAgent puts an agent back to an empty 80×24 session with a scrollback
// cap of maxLines, for the tests that run many sessions one after another:
// an emulator holds a 4 MB parser buffer for as long as its reader runs
// (clearingAgent's io.Copy), so one per seed or attempt is gigabytes under
// -race — enough for the package's race run to be killed. Closing one would
// end its reader, but vt's Close races its Read (an unguarded flag). Exact
// freshness does not matter here: each step compares the two paths on the
// same agent.
func resetAgent(a *Agent, maxLines int) {
	a.screenMu.Lock()
	defer a.screenMu.Unlock()
	_, _ = a.screen.Write([]byte("\x1bc"))
	a.screen.ClearScrollback()
	a.screen.Resize(80, 24)
	a.scrollbackLines = maxLines
	a.screen.Scrollback().SetMaxLines(maxLines)
	a.resizeSegs, a.sbLen = nil, 0
	a.segsGen++
}

// refParts is the old snapshotParts' snapshot, before gzip and parts: all
// under screenMu, at its budget (scrollbackBytes; 0: none).
func refParts(a *Agent) string {
	a.screenMu.Lock()
	defer a.screenMu.Unlock()
	budget := a.scrollbackBytes
	if budget <= 0 {
		budget = 1 << 40
	}
	return refBuildSnapshot(a.screen, refSnapshotHistory(a), nil, budget, false)
}

// joinFrame is snapshotFrame's plaintext.
func joinFrame(t *testing.T, a *Agent) string {
	t.Helper()
	enc, _ := a.snapshotFrame()
	if enc == "" {
		t.Fatal("no snapshot")
	}
	pt, err := a.box.Decrypt(enc)
	if err != nil {
		t.Fatal(err)
	}
	return string(pt)
}

// sameAsRef joins (twice: the second from what the first kept), as a plain
// frame and in parts, and checks each is byte for byte the old path's
// snapshot of the same state.
func sameAsRef(t *testing.T, a *Agent, step string) {
	t.Helper()
	// The old path's snapshot is of the state, which the joins leave as it
	// is: built once for the two joins that compare to it (each twice its
	// size to build, and this is called some hundreds of times).
	var refF, refP *string
	for _, try := range []string{"join", "join again", "join in parts", "join in parts again"} {
		var got, want string
		if strings.Contains(try, "parts") {
			parts, _, err := partsFor(t, a)
			if err != nil || len(parts) == 0 {
				t.Fatalf("%s, %s: no parts (%v)", step, try, err)
			}
			got = unpackParts(t, a, parts)
			if refP == nil {
				r := refParts(a)
				refP = &r
			}
			want = *refP
		} else {
			got = joinFrame(t, a)
			if refF == nil {
				r := refFrame(a)
				refF = &r
			}
			want = *refF
		}
		if got != want {
			i := 0
			for i < len(got) && i < len(want) && got[i] == want[i] {
				i++
			}
			lo := max(0, i-60)
			t.Fatalf("%s, %s: the snapshot differs from the old path's at byte %d of %d/%d:\n got …%q\nwant …%q", step, try, i, len(got), len(want), got[lo:min(len(got), i+60)], want[lo:min(len(want), i+60)])
		}
	}
}

func TestJoinHistoryIsSnapshotHistory(t *testing.T) {
	a := clearingAgent(t, 80, 24)
	sameAsRef(t, a, "nothing yet")
	a.record(fillerLines("FIRST", 300))
	sameAsRef(t, a, "300 lines")
	a.record(fillerLines("MORE", 5))
	sameAsRef(t, a, "5 more")
	a.resizeScreen(76, 24)
	a.record(fillerLines("MIDDLE", 40))
	a.resizeScreen(80, 24)
	a.record(fillerLines("AFTER", 40))
	if len(a.resizeSegs) < 2 {
		t.Fatalf("setup: want at least 2 resize segments, got %d", len(a.resizeSegs))
	}
	sameAsRef(t, a, "two resizes")
	a.record([]byte("\x1b[5;1Hchanged on the screen only"))
	sameAsRef(t, a, "the screen changed, the history did not")
	a.resizeScreen(100, 30)
	sameAsRef(t, a, "a third resize")
	a.record([]byte("\x1b[?1049h\x1b[2J\x1b[Han app on the alternate screen\r\nline two"))
	sameAsRef(t, a, "alternate screen")
	a.record([]byte("\x1b[?1049l"))
	sameAsRef(t, a, "back from the alternate screen")
	// Paragraphs re-emitted verbatim (dedupBlocks drops them).
	para := "the agent read the file and found that the function returns early\r\nwhen the buffer is empty so the caller never sees the error\r\n\r\n"
	a.record([]byte(strings.Repeat(para, 6)))
	sameAsRef(t, a, "repeated paragraphs")
	a.record([]byte("\x1b[H\x1b[2J\x1b[3J"))
	sameAsRef(t, a, "scrollback erased")
	a.record(fillerLines("FRESH", 50))
	sameAsRef(t, a, "after the erase")

	// An inline TUI repainting its frame on each resize (as in
	// TestResizeSegmentsDropRepaintsKeepInterleaved): the stamps it leaves in
	// the history are dropped, by the resize segments and by the screen.
	r := clearingAgent(t, 70, 20)
	var hb strings.Builder
	for i := 1; i <= 40; i++ {
		fmt.Fprintf(&hb, "HIST-%04d committed line scrolled off before resizes\r\n", i)
	}
	for i := 1; i <= 20; i++ {
		fmt.Fprintf(&hb, "FRAME-%02d frame row words kept short to fit unwrapped\r\n", i)
	}
	r.record([]byte(hb.String()))
	repaint := func(w int) {
		r.screenMu.Lock()
		cur := strings.Split(r.screen.Render(), "\n")
		r.screenMu.Unlock()
		var rb strings.Builder
		rb.WriteString("\x1b[H")
		for _, row := range cur {
			if row = strings.TrimRight(row, " "); row != "" {
				for _, wr := range nsWrap(row, w) {
					rb.WriteString("\r\x1b[K" + wr + "\n")
				}
			}
		}
		r.record([]byte(rb.String()))
	}
	r.resizeScreen(50, 20)
	repaint(50)
	r.record(fillerLines("GENUINE", 30))
	r.resizeScreen(65, 20)
	repaint(65)
	sameAsRef(t, r, "a TUI's repaints after two resizes")
	// Only the segments change (a width-only resize the screen's rows fit
	// either side of): the history is the same, and what may be dropped is
	// not — the second resize's region is no longer the newest.
	r.resizeScreen(66, 20)
	sameAsRef(t, r, "a third segment, nothing else changed")
	// Only the screen changes, written over in place: the history is the
	// same, and the frame's stale copies in it are no longer on screen.
	var ob strings.Builder
	for i := 1; i <= 20; i++ {
		fmt.Fprintf(&ob, "\x1b[%d;1H\x1b[2KOTHER-%02d a different screen now", i, i)
	}
	r.record([]byte(ob.String()))
	sameAsRef(t, r, "the screen written over, the history unchanged")

	// Only the screen changes, with no resize segments: a frame's stale
	// copies in the history are dropped while it is on screen
	// (dedupAgainstFrame), and kept once it is written over in place.
	f := clearingAgent(t, 70, 20)
	f.record(fillerLines("OLD", 30))
	var fb strings.Builder
	for k := 0; k < 2; k++ {
		for i := 1; i <= 20; i++ {
			fmt.Fprintf(&fb, "FRAME-%02d frame row words kept short to fit unwrapped\r\n", i)
		}
	}
	f.record([]byte(fb.String()))
	sameAsRef(t, f, "a frame on screen and its copy in the history")
	var wb strings.Builder
	for i := 1; i <= 20; i++ {
		fmt.Fprintf(&wb, "\x1b[%d;1H\x1b[2KOTHER-%02d a different screen now", i, i)
	}
	f.record([]byte(wb.String()))
	sameAsRef(t, f, "that frame written over, the history unchanged")

	// At the cap, a line in and a line out: the history's length and ends
	// alone would not tell the change (historyKey's collision case).
	b := clearingAgent(t, 80, 24)
	b.scrollbackLines = 60
	b.screen.Scrollback().SetMaxLines(60)
	b.record(fillerLines("CAP", 100))
	sameAsRef(t, b, "at the cap")
	for i := 0; i < 3; i++ {
		b.record([]byte("same line\r\n"))
		sameAsRef(t, b, fmt.Sprintf("a repeated line pushed at the cap (%d)", i))
		b.record([]byte("\r\n"))
		sameAsRef(t, b, fmt.Sprintf("a blank line pushed at the cap (%d)", i))
	}

	// Coloured lines, as a coding agent writes them.
	c := clearingAgent(t, 200, 40)
	pal := []string{"38;2;215;119;87", "38;2;86;156;214", "38;2;78;201;176", "1;38;5;197"}
	for i := 1; i <= 800; i++ {
		var s strings.Builder
		fmt.Fprintf(&s, "line %d ", i)
		for k := 0; k < 12; k++ {
			fmt.Fprintf(&s, "\x1b[%smword%d\x1b[39;22m ", pal[(i+k)%len(pal)], k)
		}
		c.record([]byte(s.String() + "\r\n"))
	}
	sameAsRef(t, c, "coloured")
}

// A join, plain or in parts, renders the history outside screenMu while
// output and resizes go on:
// the rows it copied are the scrollback's own, never changed once pushed
// (renderLines). -race would see it otherwise.
func TestJoinRacesOutput(t *testing.T) {
	a := clearingAgent(t, 80, 24)
	// At its cap the scrollback shifts its row list in place on each push: a
	// join must render from its own copy of it.
	a.scrollbackLines = 100
	a.screen.Scrollback().SetMaxLines(100)
	a.record(fillerLines("START", 200))
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 300; i++ {
			a.record(fillerLines(fmt.Sprintf("W%03d", i), 3))
			if i%50 == 0 {
				a.resizeScreen(uint16(70+i%20), 24)
			}
		}
	}()
	for i := 0; i < 40; i++ {
		if enc, _ := a.snapshotFrame(); enc == "" {
			t.Fatal("no snapshot")
		}
		if parts, _, err := partsFor(t, a); err != nil || len(parts) == 0 {
			t.Fatalf("no parts (%v)", err)
		}
	}
	<-done
	sameAsRef(t, a, "after the race")
}

var _ = vt.NewEmulator

// Each kept stage is keyed by all it is made of, so none is handed out for a
// state it was not made from: the screen in the last, in the middle one while
// there are resize segments (dropRepaints reads it), and the segments' every
// change (segsGen) — a resize, their eviction at the cap, the scrollback
// shrinking under them.
func TestJoinKeysAreOfWhatTheStagesRead(t *testing.T) {
	in := func(a *Agent) joinIn {
		a.screenMu.Lock()
		defer a.screenMu.Unlock()
		return a.joinInputs()
	}
	// No segments: the screen changes the last key, not the middle one.
	a := clearingAgent(t, 70, 20)
	a.record(fillerLines("LINE", 40))
	d0, f0 := joinKeys(in(a))
	a.record([]byte("\x1b[3;1H\x1b[2Kwritten over in place"))
	d1, f1 := joinKeys(in(a))
	if f0 == f1 {
		t.Error("the screen changed and the last stage's key did not")
	}
	if d0 != d1 {
		t.Error("with no segments the middle stage does not read the screen, yet its key changed")
	}
	// Segments: the screen changes the middle key too.
	a.resizeScreen(60, 20)
	a.record(fillerLines("MID", 10))
	a.resizeScreen(70, 20)
	if g := in(a); g.segs == nil {
		t.Fatal("setup: want resize segments in use")
	}
	d2, _ := joinKeys(in(a))
	a.record([]byte("\x1b[3;1H\x1b[2Kwritten over again"))
	if d3, _ := joinKeys(in(a)); d2 == d3 {
		t.Error("with segments the middle stage reads the screen, and its key did not change with it")
	}
	// A resize that changes nothing else: a new segment, a new key.
	g0 := in(a).segsGen
	a.resizeScreen(71, 20)
	if in(a).segsGen == g0 {
		t.Error("a resize added a segment and segsGen did not change")
	}
	// The scrollback shrinking under the segments (an erase) forgets them.
	g1 := in(a).segsGen
	a.record([]byte("\x1b[H\x1b[2J\x1b[3J"))
	a.screenMu.Lock()
	a.noteScrollbackLen()
	a.screenMu.Unlock()
	if len(a.resizeSegs) != 0 || in(a).segsGen == g1 {
		t.Errorf("an erased scrollback: segments %d, segsGen %d (was %d) — want none and a change", len(a.resizeSegs), in(a).segsGen, g1)
	}
	// Their eviction at the cap.
	b := clearingAgent(t, 70, 20)
	b.scrollbackLines = 50
	b.screen.Scrollback().SetMaxLines(50)
	b.record(fillerLines("A", 20))
	b.resizeScreen(60, 20)
	b.record(fillerLines("B", 5))
	b.resizeScreen(70, 20)
	g2 := b.segsGen
	b.record(fillerLines("C", 80))
	if in(b).segs != nil || b.segsGen == g2 {
		t.Errorf("at the cap: segments kept %v, segsGen %d (was %d) — want them forgotten and a change", in(b).segs != nil, b.segsGen, g2)
	}
}

// The shingle index keyed by its 8 words holds exactly the windows the old one
// keyed by their \x00-joined string did, and every mask made with it is the
// old one's — over word streams and histories drawn from a small vocabulary
// (so windows repeat and runs cover), with letters of other scripts, digits,
// colour codes and blank lines.
func TestShingleIndexIsTheJoinedOne(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	vocab := []string{"the", "agent", "read", "file", "and", "found", "Ünïcode", "日本語", "x2", "42", "error", "buffer"}
	line := func() string {
		if rng.Intn(9) == 0 {
			return ""
		}
		var b strings.Builder
		for k, n := 0, 3+rng.Intn(12); k < n; k++ {
			w := vocab[rng.Intn(len(vocab))]
			if rng.Intn(5) == 0 {
				w = "\x1b[38;5;" + strconv.Itoa(rng.Intn(256)) + "m" + w + "\x1b[0m"
			}
			b.WriteString(w + " ")
		}
		return b.String()
	}
	for c := 0; c < 300; c++ {
		ref := make([]string, 1+rng.Intn(30))
		for i := range ref {
			ref[i] = line()
		}
		var refWords []string
		for _, r := range ref {
			refWords = append(refWords, dedupWords(r)...)
		}
		got, want := buildShingles(refWords), refBuildShingles(refWords)
		if len(got) != len(want) {
			t.Fatalf("case %d: %d windows, want %d", c, len(got), len(want))
		}
		for k := range got {
			if !want[strings.Join(k[:], "\x00")] {
				t.Fatalf("case %d: window %q is not the old index's", c, k)
			}
		}
		history := make([]string, 1+rng.Intn(80))
		for i := range history {
			if rng.Intn(3) == 0 {
				history[i] = ref[rng.Intn(len(ref))]
			} else {
				history[i] = line()
			}
		}
		set := map[string]bool{}
		for _, w := range refWords {
			set[w] = true
		}
		if g, w := fmt.Sprint(coverageDropMask(history, got, set)), fmt.Sprint(refCoverageDropMask(history, want, set)); g != w {
			t.Fatalf("case %d: coverage mask %s, want %s", c, g, w)
		}
		if g, w := fmt.Sprint(frameMatchedDropMask(history, refWords)), fmt.Sprint(refFrameMatchedDropMask(history, refWords)); g != w {
			t.Fatalf("case %d: frame mask %s, want %s", c, g, w)
		}
	}
}

// Two different histories that historyKey might name alike (from the
// review of #213) — the same length, first line and last line, and, after the
// first is erased and collected, a newest row allocated where an old one was.
func TestJoinHistoryKeyNotReused(t *testing.T) {
	hist := func(mid string) []byte {
		var b strings.Builder
		b.WriteString("FIRST line of the history\r\n")
		for i := 0; i < 40; i++ {
			fmt.Fprintf(&b, "%s-%02d a middle line that differs between the two\r\n", mid, i)
		}
		for i := 0; i < 30; i++ {
			b.WriteString("TRAILER the same line at the end of both\r\n")
		}
		return []byte(b.String())
	}
	// A search for a reuse, not a proof of one case: under the race detector
	// (6x slower, and run in the same job as the plain build, which searches
	// all 300) a sixth of the attempts.
	attempts := 300
	if raceDetector {
		attempts = 50
	}
	found := 0
	a := clearingAgent(t, 80, 24)
	for attempt := 0; attempt < attempts; attempt++ {
		resetAgent(a, 20000)
		a.record(hist("AAAA"))
		_ = joinFrame(t, a) // fills the kept stages
		a.screenMu.Lock()
		k1 := historyKey(a.screen.Scrollback().Lines())
		a.screenMu.Unlock()
		a.record([]byte("\x1b[H\x1b[2J\x1b[3J"))
		runtime.GC()
		b := hist("BBBB")
		half := len(b) / 2
		a.record(b[:half])
		runtime.GC()
		a.record(b[half:])
		a.screenMu.Lock()
		k2 := historyKey(a.screen.Scrollback().Lines())
		a.screenMu.Unlock()
		got, want := joinFrame(t, a), refFrame(a)
		if k1 == k2 || got != want {
			found++
			if found == 1 {
				t.Errorf("attempt %d: same key %v, join differs from the old path %v (got AAAA %v, want AAAA %v)", attempt, k1 == k2, got != want, strings.Contains(got, "AAAA"), strings.Contains(want, "AAAA"))
			}
		}
	}
	t.Logf("collisions: %d of %d", found, attempts)
}

// Random sequences of what a session writes and how it is resized (from the
// review of #213): after each step a join, plain and in parts, must be byte
// for byte the old path's.
func TestJoinRandomMatchesOldPath(t *testing.T) {
	ops := []func(a *Agent, r int) string{
		func(a *Agent, r int) string { a.record(fillerLines(fmt.Sprintf("P%d", r%7), 5+r%60)); return "lines" },
		func(a *Agent, r int) string { a.resizeScreen(uint16(40+r%80), uint16(10+r%30)); return "resize" },
		func(a *Agent, r int) string {
			a.record([]byte("\x1b[?1049h\x1b[2J\x1b[Han app\r\nrow two " + strings.Repeat("x", r%50)))
			return "alt in"
		},
		func(a *Agent, r int) string { a.record([]byte("\x1b[?1049l")); return "alt out" },
		func(a *Agent, r int) string { a.record([]byte("\x1b[H\x1b[2J")); return "clear screen" },
		func(a *Agent, r int) string { a.record([]byte("\x1b[H\x1b[2J\x1b[3J")); return "erase scrollback" },
		func(a *Agent, r int) string {
			a.record([]byte(strings.Repeat("宽字符テスト🙂 wide "+fmt.Sprint(r)+"\r\n", 3+r%20)))
			return "wide"
		},
		func(a *Agent, r int) string {
			// Colour opened in history, closed on the screen: across the seam.
			a.record([]byte(fmt.Sprintf("\x1b[38;5;%dmcoloured start %d\r\n", r%256, r) + strings.Repeat("still coloured\r\n", 30) + "\x1b[0mreset\r\n"))
			return "sgr across the seam"
		},
		func(a *Agent, r int) string {
			a.record([]byte(fmt.Sprintf("\x1b[%d;1H\x1b[2Kin place %d", 1+r%20, r)))
			return "in place"
		},
		func(a *Agent, r int) string {
			para := fmt.Sprintf("a paragraph %d the agent re-emits verbatim across the screen\r\n\r\n", r%3)
			a.record([]byte(strings.Repeat(para, 4)))
			return "repeats"
		},
	}
	// Random sequences search for a difference; they prove no one case. Under
	// the race detector (6x slower, and run in the same job as the plain
	// build, which runs all 60) the first 10 seeds.
	seeds := 60
	if raceDetector {
		seeds = 10
	}
	a := clearingAgent(t, 80, 24)
	for seed := 1; seed <= seeds; seed++ {
		rng := seed * 7919
		next := func() int { rng = (rng*1103515245 + 12345) & 0x7fffffff; return rng }
		maxLines := 20000
		if seed%3 == 0 { // a small cap: eviction, and stale segments
			maxLines = 120
		}
		resetAgent(a, maxLines)
		var trail []string
		for step := 0; step < 40; step++ {
			r := next()
			trail = append(trail, ops[r%len(ops)](a, r/len(ops)))
			sameAsRef(t, a, fmt.Sprintf("seed %d step %d after %v", seed, step, trail[max(0, len(trail)-4):]))
		}
	}
}
