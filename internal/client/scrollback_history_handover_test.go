// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"

	"reminal/internal/crypto"
)

// inkLikeStream is a busy coding agent's output as a terminal gets it: 3000
// shell lines, then an answer of 2000 lines streamed above a live frame of 8
// rows that is erased and rewritten (Ink's log-update), then the frame alone,
// repainting (the next turn thinking) until well past the output ring.
func inkLikeStream() []byte {
	var b strings.Builder
	for i := 1; i <= 3000; i++ {
		fmt.Fprintf(&b, "line %d\r\n", i)
	}
	frameH, tick := 0, 0
	frame := func() {
		for i := 0; i < frameH; i++ {
			b.WriteString("\x1b[2K")
			if i < frameH-1 {
				b.WriteString("\x1b[1A")
			}
		}
		b.WriteString("\r")
		fmt.Fprintf(&b, "\x1b[38;2;215;119;87m* Thinking… (%d.%ds · esc to interrupt)\x1b[39m", tick/20, tick%20/2)
		for i := 0; i < 6; i++ {
			fmt.Fprintf(&b, "\r\n\x1b[2m| live row %d · · · · · · · · · · · · · · · · · · · · · · ·\x1b[22m", i)
		}
		b.WriteString("\r\n\x1b[7m> \x1b[27m")
		frameH = 8
		tick++
	}
	for n := 20001; n <= 22000; n++ {
		if frameH > 0 {
			for i := 0; i < frameH; i++ {
				b.WriteString("\x1b[2K")
				if i < frameH-1 {
					b.WriteString("\x1b[1A")
				}
			}
			b.WriteString("\r")
			frameH = 0
		}
		fmt.Fprintf(&b, "line %d of the answer, committed above the live frame\r\n", n)
		if n%5 == 0 {
			frame()
		}
	}
	for b.Len() < 3*scrollbackBytes {
		frame()
	}
	return []byte(b.String())
}

// distinctLines counts the shell (1..3000) and answer (20001..22000) lines
// text holds.
func distinctLines(text string) (shell, answer int) {
	seen := map[int]bool{}
	for _, m := range regexp.MustCompile(`line (\d+)\b`).FindAllStringSubmatch(text, -1) {
		n, _ := strconv.Atoi(m[1])
		seen[n] = true
	}
	for n := range seen {
		switch {
		case n >= 1 && n <= 3000:
			shell++
		case n >= 20001 && n <= 22000:
			answer++
		}
	}
	return
}

func handoverAgent(t *testing.T, box *crypto.Box) *Agent {
	t.Helper()
	a := &Agent{box: box, buf: newScrollback(scrollbackBytes), scrollbackLines: 10000, sessionID: "HANDOVR1"}
	a.screen = vt.NewEmulator(120, 30)
	a.screen.Scrollback().SetMaxLines(a.scrollbackLines)
	go io.Copy(io.Discard, a.screen)
	a.buf.SetBase(120, 30)
	return a
}

// A hot restart (and a reboot restore, which reads the same dump) hands the
// successor the history a viewer could scroll through, not only what the last
// scrollbackBytes of raw output scrolled. A busy agent's repaints fill that
// ring within minutes: replayed alone it held about a screen, the "scrollback
// is 1.5× the screen" of a session hot-restarted by its upgrades.
func TestAHotRestartKeepsTheWholeHistory(t *testing.T) {
	key, _ := crypto.NewSessionKey()
	box, _ := crypto.NewBox(key)
	a := handoverAgent(t, box)
	raw := inkLikeStream()
	if len(raw) <= scrollbackBytes {
		t.Fatalf("fixture: %d bytes does not overflow the %d-byte ring", len(raw), scrollbackBytes)
	}
	for i := 0; i < len(raw); i += 4096 {
		a.record(raw[i:min(i+4096, len(raw))])
	}
	a.screenMu.Lock()
	before := stripANSI(buildSnapshot(a.screen, a.snapshotHistory(), nil, maxSnapshotPlaintext, false))
	a.screenMu.Unlock()
	if s, an := distinctLines(before); s != 3000 || an != 2000 {
		t.Fatalf("fixture: before the restart a viewer sees %d/3000 shell and %d/2000 answer lines", s, an)
	}

	path, k, err := a.writeScrollbackDump()
	if err != nil || path == "" {
		t.Fatalf("dump: %q %v", path, err)
	}
	b := handoverAgent(t, box)
	b.resumeDump = takeScrollbackDump(path, k, a.sessionID)
	if b.resumeDump == nil {
		t.Fatal("the dump did not come back")
	}
	b.restoreResumedScrollback()
	b.screenMu.Lock()
	after := stripANSI(buildSnapshot(b.screen, b.snapshotHistory(), nil, maxSnapshotPlaintext, false))
	b.screenMu.Unlock()
	s, an := distinctLines(after)
	if s != 3000 || an != 2000 {
		var miss []int
		for n := 20001; n <= 22000; n++ {
			if !strings.Contains(after, fmt.Sprintf("line %d ", n)) {
				miss = append(miss, n)
			}
		}
		t.Fatalf("after a hot restart a viewer sees %d/3000 shell and %d/2000 answer lines; missing %v", s, an, miss)
	}
	// What the ring scrolls again is the history's own tail: it must not
	// show twice.
	for _, n := range []int{1, 1500, 3000, 20001, 21000, 22000} {
		if c := strings.Count(after, fmt.Sprintf("line %d ", n)) + strings.Count(after, fmt.Sprintf("line %d\n", n)); c != 1 {
			t.Errorf("line %d shows %d times after the restart", n, c)
		}
	}
}

// A dump from before History (an older agent's) is replayed as it always was.
func TestADumpWithoutHistoryIsReplayedAsBefore(t *testing.T) {
	key, _ := crypto.NewSessionKey()
	box, _ := crypto.NewBox(key)
	a := handoverAgent(t, box)
	for i := 1; i <= 50; i++ {
		a.record([]byte(fmt.Sprintf("line %d\r\n", i)))
	}
	path, k, err := a.writeScrollbackDump()
	if err != nil {
		t.Fatal(err)
	}
	b := handoverAgent(t, box)
	b.resumeDump = takeScrollbackDump(path, k, a.sessionID)
	b.resumeDump.History, b.resumeDump.HistoryCols = nil, 0
	b.restoreResumedScrollback()
	b.screenMu.Lock()
	after := stripANSI(buildSnapshot(b.screen, b.snapshotHistory(), nil, maxSnapshotPlaintext, false))
	b.screenMu.Unlock()
	if s, _ := distinctLines(after); s != 50 {
		t.Fatalf("an old dump's ring replay gave %d/50 lines", s)
	}
}

// When the ring still holds all of the history (output that cost few bytes:
// a pseudo console sends only the cells a repaint changed), its replay draws
// every line again: put back from the dump as well, each would show twice.
func TestAHotRestartWithTheHistoryInItsRingShowsNothingTwice(t *testing.T) {
	key, _ := crypto.NewSessionKey()
	box, _ := crypto.NewBox(key)
	a := handoverAgent(t, box)
	var b strings.Builder
	for i := 1; i <= 3000; i++ {
		fmt.Fprintf(&b, "line %d\r\n", i)
	}
	for n := 20001; n <= 22000; n++ {
		fmt.Fprintf(&b, "line %d of the answer, committed above the live frame\r\n", n)
	}
	b.WriteString("\x1b[7m> \x1b[27m")
	a.record([]byte(b.String()))
	if len(a.buf.From(0)) == 0 || b.Len() > scrollbackBytes {
		t.Fatal("fixture: the whole history should fit the ring")
	}
	path, k, err := a.writeScrollbackDump()
	if err != nil || path == "" {
		t.Fatalf("dump: %q %v", path, err)
	}
	r := handoverAgent(t, box)
	r.resumeDump = takeScrollbackDump(path, k, a.sessionID)
	r.restoreResumedScrollback()
	r.screenMu.Lock()
	after := stripANSI(buildSnapshot(r.screen, r.snapshotHistory(), nil, maxSnapshotPlaintext, false))
	sb := r.screen.Scrollback().Len()
	r.screenMu.Unlock()
	if s, an := distinctLines(after); s != 3000 || an != 2000 {
		t.Fatalf("after a hot restart a viewer sees %d/3000 shell and %d/2000 answer lines", s, an)
	}
	twice := 0
	for n := 1; n <= 3000; n++ {
		if strings.Count(after, fmt.Sprintf("line %d\n", n)) > 1 {
			twice++
		}
	}
	for n := 20001; n <= 22000; n++ {
		if strings.Count(after, fmt.Sprintf("line %d ", n)) > 1 {
			twice++
		}
	}
	if twice > 0 || sb > 5000 {
		t.Fatalf("%d lines show twice after the restart (scrollback %d lines for 5000)", twice, sb)
	}
}

// historySeam: the ring's replay, however it begins, joins the history it put
// back with nothing lost and nothing twice; and repetitive history costs no
// more than any other (it ran before the PTY pump, on every restart).
func TestTheHistorySeamLosesNothingAndRepeatsNothing(t *testing.T) {
	h := make([]string, 100)
	for i := range h {
		h[i] = fmt.Sprintf("history line %03d", i)
	}
	tail := h[50:]
	withDebris := func(debris ...string) []string { return append(append([]string(nil), debris...), tail...) }
	cases := []struct {
		name  string
		again []string
		want  int
	}{
		{"the ring draws the tail exactly", withDebris(), 50},
		{"a blank line first", withDebris(""), 50},
		{"five lines of frame debris first", withDebris("\x1b[2m| live row 3", "* Thinking… (4.0s)", "| live row 4", "| live row 5", "> "), 50},
		{"cut in the middle of a line", withDebris("ne 049"), 50},
		{"nothing of the history", []string{"something else", "entirely", "on the screen", "now"}, 100},
	}
	for _, c := range cases {
		got := historySeam(h, c.again)
		if got != c.want {
			t.Errorf("%s: seam %d, want %d", c.name, got, c.want)
			continue
		}
		// What a viewer then scrolls through: the history up to the seam, then
		// what the replay draws.
		out := append(append([]string(nil), h[:got]...), c.again...)
		seen := map[string]int{}
		for _, l := range out {
			seen[l]++
		}
		for _, l := range h {
			if seen[l] != 1 {
				t.Errorf("%s: %q shows %d times", c.name, l, seen[l])
			}
		}
	}

	same := make([]string, 10000)
	for i := range same {
		same[i] = "the same line, ten thousand times"
	}
	start := time.Now()
	got := historySeam(same, same[:2000])
	if el := time.Since(start); el > 250*time.Millisecond {
		t.Errorf("10000 identical lines: %v (it runs before the PTY pump)", el)
	}
	if got != 8000 {
		t.Errorf("10000 identical lines: seam %d, want 8000", got)
	}
}

// The rendered history a dump carries is kept between dumps only while the
// scrollback is unchanged — including at its cap, where its length stays the
// same as lines are pushed through it.
func TestTheDumpsRenderedHistoryIsKeptOnlyWhileUnchanged(t *testing.T) {
	key, _ := crypto.NewSessionKey()
	box, _ := crypto.NewBox(key)
	a := handoverAgent(t, box)
	a.scrollbackLines = 50
	a.screen.Scrollback().SetMaxLines(50)
	for i := 1; i <= 200; i++ {
		a.record([]byte(fmt.Sprintf("line %d\r\n", i)))
	}
	a.screenMu.Lock()
	first := a.renderedHistory()
	again := a.renderedHistory()
	a.screenMu.Unlock()
	if len(first) != 50 || &first[0] != &again[0] {
		t.Fatalf("unchanged: %d lines, kept=%v", len(first), &first[0] == &again[0])
	}
	a.record([]byte("line 201\r\n")) // at the cap: one in, one out, the same length
	a.screenMu.Lock()
	after := a.renderedHistory()
	a.screenMu.Unlock()
	if s, _ := distinctLines(strings.Join(after, "\n")); s != 50 {
		t.Fatalf("after one more line at the cap: %d distinct lines", s)
	}
	plain := func(ls []string, i int) string { return strings.TrimSpace(stripANSI(ls[i])) }
	if plain(first, 0) != "line 122" || plain(first, 49) != "line 171" || plain(after, 0) != "line 123" || plain(after, 49) != "line 172" {
		t.Fatalf("at the cap the history must move by the line pushed: before %q…%q, after %q…%q",
			plain(first, 0), plain(first, 49), plain(after, 0), plain(after, 49))
	}
}

// At the cap, a push can leave the length and both end lines as they were
// when repeated lines sit at both ends: the history must still be the new one.
func TestTheDumpsRenderedHistoryIsNotFooledByRepeatsAtBothEnds(t *testing.T) {
	cases := []struct {
		name, fill, push string
	}{
		// Scrollback [X X M1 M2 Y], screen [Y P Q]: the next line pushed is Y.
		{"a repeated line pushed", "X\r\nX\r\nM1\r\nM2\r\nY\r\nY\r\nP\r\nQ", "\r\nR"},
		// Scrollback [X X M "" ""], screen ["" P Q]: the next line pushed is blank.
		{"a blank line pushed", "X\r\nX\r\nM\r\n\r\n\r\n\r\nP\r\nQ", "\r\nR"},
	}
	for _, c := range cases {
		key, _ := crypto.NewSessionKey()
		box, _ := crypto.NewBox(key)
		a := &Agent{box: box, buf: newScrollback(scrollbackBytes), scrollbackLines: 5, sessionID: "HISTKEY1"}
		a.screen = vt.NewEmulator(20, 3)
		a.screen.Scrollback().SetMaxLines(5)
		go io.Copy(io.Discard, a.screen)
		a.buf.SetBase(20, 3)
		a.record([]byte(c.fill))
		a.screenMu.Lock()
		before := strings.Join(a.renderedHistory(), "|")
		a.screenMu.Unlock()
		a.record([]byte(c.push))
		a.screenMu.Lock()
		got := strings.Join(a.renderedHistory(), "|")
		want := strings.Join(renderScrollback(a.screen, a.scrollbackLines), "|")
		a.screenMu.Unlock()
		if before == want {
			t.Fatalf("%s: fixture: the push changed nothing (%q)", c.name, want)
		}
		if got != want {
			t.Errorf("%s: handed out %q, the scrollback is %q", c.name, stripANSI(got), stripANSI(want))
		}
	}
}

// A restore lets go of the scratch emulator it replays the ring into: what a
// restored process keeps, past the agent itself, is nothing of it. It used to
// keep it for good (its drain alive, its scrollback "cleared" but still
// reachable): 10000 lines of 240 columns of scrolling output, ~150 MB.
func TestARestoreLetsGoOfItsScratchEmulator(t *testing.T) {
	heapInUse := func() float64 {
		runtime.GC()
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return float64(m.HeapInuse) / 1e6
	}
	key, _ := crypto.NewSessionKey()
	box, _ := crypto.NewBox(key)
	a := handoverAgent(t, box)
	freeAtEnd(t, a.screen)
	for i := 0; i < 10000; i++ {
		a.record([]byte(fmt.Sprintf("%05d %s\r\n", i, strings.Repeat("x", 234))))
	}
	path, k, err := a.writeScrollbackDump()
	if err != nil || path == "" {
		t.Fatalf("dump: %q %v", path, err)
	}
	id := a.sessionID
	freeEmulator(a.screen)
	a = nil
	base := heapInUse()

	b := handoverAgent(t, box)
	b.resumeDump = takeScrollbackDump(path, k, id)
	if b.resumeDump == nil || len(b.resumeDump.History) == 0 {
		t.Fatal("fixture: no dump with history came back")
	}
	b.restoreResumedScrollback()
	b.resumeDump = nil
	b.screenMu.Lock()
	n := b.screen.Scrollback().Len()
	freeEmulator(b.screen) // the agent's own history let go: what is left, the restore kept
	b.screenMu.Unlock()
	if n < 9000 {
		t.Fatalf("fixture: the restore brought back %d lines", n)
	}
	kept := heapInUse() - base
	t.Logf("after restoring %d lines and letting the agent's own go: %.0f MB kept", n, kept)
	if kept > 40 {
		t.Errorf("a restore kept %.0f MB past the agent (its scratch emulator, not let go)", kept)
	}
	runtime.KeepAlive(b)
}

// A young session's ring still holds its first output, so replayed it draws
// the whole history again: a restart does not put the history back as well.
// Put back, the history came back twice whenever the replay drew a line
// otherwise than the history held it: here a row a redraw overwrote mid-way
// (the start of one line, cut, and the rest of the next).
func TestAYoungSessionsRestartDrawsItsHistoryOnce(t *testing.T) {
	key, _ := crypto.NewSessionKey()
	box, _ := crypto.NewBox(key)
	a := handoverAgent(t, box)
	var b strings.Builder
	for i := 1; i <= 80; i++ {
		fmt.Fprintf(&b, "young %d the quick brown fox jumps over the lazy dog\r\n", i)
	}
	a.record([]byte(b.String()))
	path, k, err := a.writeScrollbackDump()
	if err != nil || path == "" {
		t.Fatalf("dump: %q %v", path, err)
	}
	r := handoverAgent(t, box)
	r.resumeDump = takeScrollbackDump(path, k, a.sessionID)
	if r.resumeDump == nil || len(r.resumeDump.History) < 40 || r.resumeDump.Entries[0].Seq != 1 {
		t.Fatal("fixture: want a dump with history and a ring from its first output")
	}
	h := r.resumeDump.History
	h[20] = "young 21 the quick bro" + " fox jumps over the lazy dog"
	r.resumeDump.History = append(h[:21:21], h[22:]...)
	r.restoreResumedScrollback()
	r.screenMu.Lock()
	history := r.renderedHistory()
	r.screenMu.Unlock()
	n := 0
	for _, l := range history {
		if strings.Contains(stripANSI(l), "young 1 the") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("the first line is in the history %d times after the restart, want once", n)
	}
}

// A ring from the session's first output can still leave history it does not
// draw: lines an older restart put back, outside the ring. Where the history's
// tail matches what the ring draws, those go back as before, once.
func TestARestartKeepsHistoryTheRingDoesNotDraw(t *testing.T) {
	key, _ := crypto.NewSessionKey()
	box, _ := crypto.NewBox(key)
	a := handoverAgent(t, box)
	var b strings.Builder
	for i := 1; i <= 80; i++ {
		fmt.Fprintf(&b, "young %d the quick brown fox jumps over the lazy dog\r\n", i)
	}
	a.record([]byte(b.String()))
	path, k, err := a.writeScrollbackDump()
	if err != nil || path == "" {
		t.Fatalf("dump: %q %v", path, err)
	}
	r := handoverAgent(t, box)
	r.resumeDump = takeScrollbackDump(path, k, a.sessionID)
	if r.resumeDump == nil || r.resumeDump.Entries[0].Seq != 1 {
		t.Fatal("fixture: want a ring from its first output")
	}
	var earlier []string
	for i := 1; i <= 30; i++ {
		earlier = append(earlier, fmt.Sprintf("earlier %d put back by a restart before", i))
	}
	r.resumeDump.History = append(earlier, r.resumeDump.History...)
	r.restoreResumedScrollback()
	r.screenMu.Lock()
	history := r.renderedHistory()
	history = append(history, renderedScreenRows(r.screen)...)
	r.screenMu.Unlock()
	for _, want := range []string{"earlier 1 put", "earlier 30 put", "young 1 the", "young 80 the"} {
		n := 0
		for _, l := range history {
			if strings.Contains(stripANSI(l), want) {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%q is in the history %d times after the restart, want once", want, n)
		}
	}
}

// Where the ring's first lines are in the history more than once (an older
// restart put the same banner back above), the history goes back up to the
// copy from which all that follows is drawn again: never an older one, whose
// lines below it the replay does not draw (they were lost). The ring here is
// a banner, 30 lines and 40 screen rows; each case says what the history
// holds besides it, and nothing of the history may be missing after.
func TestARestartCutsTheHistoryWhereNothingIsLost(t *testing.T) {
	banner := []string{"Welcome to the tool", "Type help for help", "$ make test"}
	var ring []string
	ring = append(ring, banner...)
	for i := 1; i <= 30; i++ {
		ring = append(ring, fmt.Sprintf("ring line %d of the session", i))
	}
	for i := 1; i <= 40; i++ {
		ring = append(ring, fmt.Sprintf("screen row %d", i))
	}
	older := func(gap int) []string {
		h := append([]string(nil), banner...)
		h = append(h, "an older session's line")
		for i := 1; i <= gap; i++ {
			h = append(h, fmt.Sprintf("between line %d put back by a restart", i))
		}
		return h
	}
	drawnOtherwise := append([]string{"Welcome to the tool, again", "Type help for help", "$ make test"}, ring[3:]...)
	for _, c := range []struct {
		name    string
		history []string
		again   []string
	}{
		{"an older copy of the banner 10 lines above", append(older(10), ring...), ring},
		{"an older copy of the banner 40 lines above", append(older(40), ring...), ring},
		// A full-screen app up: the history holds none of the screen's rows.
		{"10 lines above, without the screen rows", append(older(10), ring[:33]...), ring},
		{"40 lines above, without the screen rows", append(older(40), ring[:33]...), ring},
		{"the session's own start pruned away", append(older(10), ring[20:]...), ring},
		{"the ring's start drawn otherwise", append(older(10), ring...), drawnOtherwise},
	} {
		cut := ringStartIn(c.history, c.again)
		shown := append(append([]string(nil), c.history[:cut]...), c.again...)
		drawn := drawnAgain(shown)
		for _, l := range c.history {
			if !drawn(l) {
				t.Errorf("%s: cut at %d of %d, and %q is lost", c.name, cut, len(c.history), l)
				break
			}
		}
	}
}

// A restart's dump is of one moment: the history it holds is drawn by the
// ring it holds. Taken while output arrives, a dump whose history ran a
// chunk ahead of its ring put all of the history back on a restart, on top
// of the ring's replay (the ring never drew those last rows, so no cut
// matched): the same text twice. Output is recorded without pause while
// dumps are written, and every dump's last history line is in its ring.
func TestARestartDumpIsOfOneMoment(t *testing.T) {
	key, _ := crypto.NewSessionKey()
	box, _ := crypto.NewBox(key)
	a := handoverAgent(t, box)
	a.record([]byte("line 0 of the output\r\n")) // a ring to dump: an empty one writes no file
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 1; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			a.record([]byte(fmt.Sprintf("line %d of the output\r\n", i)))
		}
	}()
	identity := func(b []byte) ([]byte, error) { return b, nil }
	path := filepath.Join(t.TempDir(), "dump.json")
	behind := 0
	const dumps = 60 // read apart, every one of hundreds was behind
	for i := 0; i < dumps; i++ {
		if err := a.writeScrollbackDumpTo(path, identity); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var d scrollbackDump
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatal(err)
		}
		last := ""
		for j := len(d.History) - 1; j >= 0; j-- {
			if s := strings.TrimSpace(stripANSI(d.History[j])); s != "" {
				last = s
				break
			}
		}
		var ring strings.Builder
		for _, e := range d.Entries {
			ring.Write(e.Data)
		}
		if last != "" && !strings.Contains(ring.String(), last) {
			behind++
		}
	}
	close(stop)
	<-done
	if behind > 0 {
		t.Errorf("%d of %d dumps hold a history line their ring never drew", behind, dumps)
	}
}
