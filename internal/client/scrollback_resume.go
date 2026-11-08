// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unsafe"

	uv "github.com/charmbracelet/ultraviolet"

	"reminal/internal/atomicfile"
	"reminal/internal/atrest"
)

// envResumeScrollback is the path of a 0600 dump written just before a hot
// restart. The session key is NOT carried across the exec (viewers re-EKE),
// so the dump is sealed under a one-time key handed to the successor in
// envResumeScrollbackKey — the key never touches disk — and the successor
// re-encrypts the history under its own session key.
const (
	envResumeScrollback    = "REMINAL_RESUME_SCROLLBACK"
	envResumeScrollbackKey = "REMINAL_RESUME_SCROLLBACK_KEY"
)

const kindHandoff = "handoff"

const scrollbackDumpVersion = 1

type scrollbackDump struct {
	Version  int               `json:"v"`
	NextSeq  uint64            `json:"next_seq"`
	BaseCols int               `json:"base_cols"`
	BaseRows int               `json:"base_rows"`
	Entries  []scrollDumpEntry `json:"entries"`
	// History is the snapshot emulator's scrollback as it was rendered (SGR
	// kept), at most scrollbackLines, and HistoryCols the width it was
	// rendered at. Entries are only the raw output ring (scrollbackBytes):
	// replayed alone, a successor's history is whatever the last 2 MiB of
	// output scrolled — on a busy agent's repaints, about a screen — so every
	// hot restart and reboot restore threw the rest away. A dump from an older
	// agent has none, and is replayed as before.
	History     []string `json:"history,omitempty"`
	HistoryCols int      `json:"history_cols,omitempty"`
}

type scrollDumpEntry struct {
	Seq  uint64 `json:"seq"`
	Data []byte `json:"data,omitempty"`
	Bar  bool   `json:"bar,omitempty"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

func scrollbackDumpPath(sessionID string) (string, error) {
	dir, err := reminalDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, "scrollback-"+sessionID+".json"), nil
}

// writeScrollbackDump writes the live buffer, sealed under a fresh one-time
// key, to a 0600 file the successor will load, and returns the key as hex.
// Empty history yields ("", "", nil) — no file.
func (a *Agent) writeScrollbackDump() (string, string, error) {
	if a == nil || a.buf == nil || a.box == nil || a.sessionID == "" {
		return "", "", nil
	}
	path, err := scrollbackDumpPath(a.sessionID)
	if err != nil {
		return "", "", err
	}
	if len(a.buf.From(0)) == 0 {
		return "", "", nil
	}
	key, err := atrest.NewKey()
	if err != nil {
		return "", "", err
	}
	id := a.sessionID
	err = a.writeScrollbackDumpTo(path, func(b []byte) ([]byte, error) {
		return atrest.SealWith(key, kindHandoff, id, b)
	})
	return path, hex.EncodeToString(key), err
}

// writeScrollbackDumpTo writes the live buffer, decrypted from the session
// key and sealed by seal, to path (0600). An empty buffer writes nothing.
func (a *Agent) writeScrollbackDumpTo(path string, seal func([]byte) ([]byte, error)) error {
	if a == nil || a.buf == nil || a.box == nil {
		return nil
	}
	raw := a.buf.From(0)
	if len(raw) == 0 {
		return nil
	}
	dump := scrollbackDump{
		Version: scrollbackDumpVersion,
		NextSeq: a.buf.LatestSeq(),
	}
	dump.BaseCols, dump.BaseRows = a.buf.Base()
	a.screenMu.Lock()
	if a.screen != nil {
		// The screen's rows too: what an app left above a frame it repaints
		// in place never scrolls into scrollback, and its ring holds only the
		// repaints. Should the replay draw them again, the copy is the
		// history's tail matching the frame, which the snapshot drops
		// (dedupAgainstFrame).
		// A copy: the cache is not ours to grow. A full-screen app's screen
		// (the alternate one) is not history.
		dump.History = append([]string(nil), a.renderedHistory()...)
		if !a.screen.IsAltScreen() {
			dump.History = append(dump.History, renderedScreenRows(a.screen)...)
		}
		if n := len(dump.History) - a.scrollbackLines; a.scrollbackLines > 0 && n > 0 {
			dump.History = dump.History[n:]
		}
		dump.HistoryCols = a.screen.Width()
	}
	a.screenMu.Unlock()
	dump.Entries = make([]scrollDumpEntry, 0, len(raw))
	for _, e := range raw {
		de := scrollDumpEntry{Seq: e.Seq, Bar: e.Bar, Cols: e.Cols, Rows: e.Rows}
		if e.Data != "" {
			pt, err := a.box.Decrypt(e.Data)
			if err != nil {
				return fmt.Errorf("decrypt scrollback seq %d: %w", e.Seq, err)
			}
			de.Data = pt
		}
		dump.Entries = append(dump.Entries, de)
	}
	body, err := json.Marshal(dump)
	if err != nil {
		return err
	}
	body, err = seal(body)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// Unique temp name, replacing rename, flushed: no window where the old
	// file is gone and the new one not there, and no two writers removing
	// each other's file.
	return atomicfile.Write(path, body, 0o600)
}

// dumpScrollbackForRestart writes the dump if there is history and returns
// the env entries that hand it to the successor. A write error must not
// block restart — we notify and continue without a file.
func (a *Agent) dumpScrollbackForRestart() (path string, env []string) {
	path, key, err := a.writeScrollbackDump()
	if err != nil {
		agentNotify("  reminal: could not save scrollback for restart — history will reset: %v\n", err)
		removeScrollbackDump(path)
		return "", nil
	}
	if path == "" {
		return "", nil
	}
	return path, []string{envResumeScrollback + "=" + path, envResumeScrollbackKey + "=" + key}
}

// removeScrollbackDump deletes a dump and its .tmp sibling. Safe on "" / missing.
func removeScrollbackDump(path string) {
	if path == "" {
		return
	}
	_ = os.Remove(path)
	_ = os.Remove(path + ".tmp")
}

// takeScrollbackDump loads and deletes a dump written by the predecessor,
// opened with the one-time key it handed over (hex). A predecessor from
// before sealing passes no key and a plain dump. Always deletes path and
// path+".tmp" so nothing lingers after a corrupt / wrong-version file.
// Missing or unreadable files return nil so restart still comes up (just
// without history).
func takeScrollbackDump(path, keyHex, sessionID string) *scrollbackDump {
	if path == "" {
		return nil
	}
	d := readScrollbackDump(path, func(b []byte) ([]byte, error) {
		if !atrest.IsSealed(b) {
			return b, nil // an older predecessor's plain dump
		}
		key, err := hex.DecodeString(keyHex)
		if err != nil {
			return nil, err
		}
		return atrest.OpenWith(key, kindHandoff, sessionID, b)
	})
	removeScrollbackDump(path)
	return d
}

// readScrollbackDump loads a dump, opened by open, and leaves it in place —
// a restore that fails can be tried again. Nil when missing, unreadable or
// empty.
func readScrollbackDump(path string, open func([]byte) ([]byte, error)) *scrollbackDump {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	if body, err = open(body); err != nil {
		return nil
	}
	var dump scrollbackDump
	if json.Unmarshal(body, &dump) != nil || dump.Version != scrollbackDumpVersion {
		return nil
	}
	if len(dump.Entries) == 0 {
		return nil
	}
	return &dump
}

// restoreResumedScrollback re-encrypts a predecessor dump under this agent's
// new session key and replays it into the snapshot emulator. Must run after
// initScreen and before the PTY pump, or live output interleaves with history.
func (a *Agent) restoreResumedScrollback() {
	a.screenMu.Lock()
	defer a.screenMu.Unlock()
	a.restoreResumedScrollbackLocked()
}

// restoreResumedScrollbackLocked is restoreResumedScrollback with the
// screen's lock held, from before its ring is restored to after its replay:
// output recorded meanwhile (record, under the same lock) comes after both
// (Run takes the lock before the pump starts and runs this behind it).
func (a *Agent) restoreResumedScrollbackLocked() {
	d := a.resumeDump
	a.resumeDump = nil
	if d == nil || a.buf == nil || a.box == nil {
		return
	}
	entries := make([]scrollEntry, 0, len(d.Entries))
	for _, e := range d.Entries {
		se := scrollEntry{Seq: e.Seq, Bar: e.Bar, Cols: e.Cols, Rows: e.Rows}
		if len(e.Data) > 0 {
			enc, err := a.box.Encrypt(e.Data)
			if err != nil {
				agentNotify("  reminal: could not restore scrollback: %v\n", err)
				return
			}
			se.Data = enc
		}
		entries = append(entries, se)
	}
	a.buf.restore(entries, d.NextSeq, d.BaseCols, d.BaseRows)

	if a.screen == nil {
		return
	}
	if len(d.History) > 0 {
		// Only what the ring will not draw again: replayed into a scratch
		// emulator first (once, at a restart), what it reproduces is matched
		// against the history's tail, and the history goes in up to there.
		scratch, err := newSessionScreen(max(d.BaseCols, 1), max(d.BaseRows, 1), len(d.History)+1000)
		if err != nil {
			agentNotify("  reminal: could not restore scrollback: %v\n", err)
			return
		}
		again := func() []string {
			defer scratch.Close()
			replayDump(scratch, d)
			return append(append([]string(nil), scratch.History()...), renderedScreenRows(scratch)...)
		}()
		restoreHistory(a.screen, d.History[:restoreCut(d, again)], d.HistoryCols)
		a.noteScrollbackLen()
	}
	replayDump(a.screen, d, a.noteScrollbackLen)
	a.freshLineAfterRestoreLocked()
}

// restoreCut is how much of d's history goes back before its ring is
// replayed: what the ring does not draw again (again: what it draws). A ring
// that holds the session's first output (ringFromStart) draws all of the
// history but what an older restart put back above it, so the history goes
// back up to where the ring's own first lines are in it (ringStartIn): a
// line the replay draws otherwise is then the replay's, not a reason to put
// all of it back (a young session's history came back twice). Any other
// ring, up to where it starts drawing the history's tail (historySeam).
func restoreCut(d *scrollbackDump, again []string) int {
	if ringFromStart(d) {
		return ringStartIn(d.History, again)
	}
	return historySeam(d.History, again)
}

// ringStartIn is where in history the ring's replay (again) begins: the first
// of its lines with text, and the two after it, found there together, at a
// place from which every row of the history after it is drawn again
// (drawnAgain), or nothing after it would be lost with it. Of several such
// places, the one nearest where the two would end together; none, all of
// history (a line twice rather than one lost).
func ringStartIn(history, again []string) int {
	plain := func(l string) string { return strings.TrimRight(stripANSI(l), " ") }
	at := map[string][]int{}
	for i, l := range history {
		if p := plain(l); p != "" {
			at[p] = append(at[p], i)
		}
	}
	drawn := drawnAgain(again)
	for j := 0; j < len(again) && j < 64; j++ {
		first := plain(again[j])
		if first == "" {
			continue
		}
		n := min(3, len(again)-j)
		var places []int
		for _, p := range at[first] {
			same := p+n <= len(history)
			for k := 1; k < n && same; k++ {
				same = plain(history[p+k]) == plain(again[j+k])
			}
			if same {
				places = append(places, p)
			}
		}
		want := len(history) - len(again) + j
		slices.SortStableFunc(places, func(a, b int) int { return absInt(a-want) - absInt(b-want) })
		for _, p := range places {
			if !slices.ContainsFunc(history[p:], func(l string) bool { return !drawn(l) }) {
				return p
			}
		}
	}
	return len(history)
}

// drawnAgain says of a row whether rows (what a replay draws) show its text:
// its tokens, split at spaces, an unbroken run of theirs read as one text. Or
// a redraw's splice of two: the start of one line, cut mid-word, and the rest
// of another, each shown (the first's last token the start of one of theirs).
// A row with no text is drawn.
func drawnAgain(rows []string) func(string) bool {
	var stream []string
	at := map[string][]int{}
	for _, r := range rows {
		for _, w := range strings.Fields(stripANSI(r)) {
			at[w] = append(at[w], len(stream))
			stream = append(stream, w)
		}
	}
	// run says whether t is a run of stream; cut: its last token may be the
	// start of the one there.
	run := func(t []string, cut bool) bool {
		for _, s := range at[t[0]] {
			if s+len(t) > len(stream) {
				continue
			}
			ok := true
			for k := 1; k < len(t) && ok; k++ {
				ok = stream[s+k] == t[k] || cut && k == len(t)-1 && strings.HasPrefix(stream[s+k], t[k])
			}
			if ok {
				return true
			}
		}
		return false
	}
	return func(row string) bool {
		t := strings.Fields(stripANSI(row))
		if len(t) == 0 || run(t, false) {
			return true
		}
		for k := 2; k <= len(t)-1; k++ {
			if run(t[:k], true) && run(t[k:], false) {
				return true
			}
		}
		return false
	}
}

// ringFromStart says whether a dump's output ring still holds the session's
// first output: its first entry is the first ever (seqs start at 1, nothing
// evicted, and a restart keeps them).
func ringFromStart(d *scrollbackDump) bool {
	return len(d.Entries) > 0 && d.Entries[0].Seq == 1
}

// freshLineAfterRestoreLocked starts what a new shell writes on a line of its
// own, when the screen restored ends with its cursor mid-line (after the old
// shell's prompt): its prompt was drawn on that same row before
// ("…/work$ …/work$"), and the line a person's keys were typed at never
// matched the one the session came back to. Only when a new shell starts
// (restoring: no PTY handed over); a hot restart's shell runs on where its
// cursor is. Into the screen and the ring alike, as record does. Caller
// holds screenMu.
func (a *Agent) freshLineAfterRestoreLocked() {
	if !a.restoring || a.screen == nil || a.screen.IsAltScreen() || a.screen.CursorPosition().X == 0 {
		return
	}
	nl := []byte("\r\n")
	_, _ = a.screen.Write(nl)
	a.noteScrollbackLen()
	if enc, err := a.box.Encrypt(nl); err == nil {
		a.buf.Append(enc)
	}
}

// restoreHistory puts a predecessor's rendered scrollback (scrollbackDump.
// History) into e's scrollback, whole, before its output ring is replayed:
// each line at the width it was rendered at, then a screen of blank lines to
// push the last of them up, then the screen cleared without pushing (ED 0
// from home) for the replay to draw on. What the ring itself scrolls again is
// a copy of the history's tail; the snapshot's dedup drops it (dedupBlocks).
func restoreHistory(e *sessionScreen, history []string, cols int) {
	if e == nil || len(history) == 0 {
		return
	}
	if cols > 0 && cols != e.Width() {
		e.Resize(cols, e.Height())
	}
	var b strings.Builder
	for _, l := range history {
		b.WriteString(l)
		b.WriteString("\x1b[m\r\n")
	}
	b.WriteString(strings.Repeat("\r\n", e.Height()))
	b.WriteString("\x1b[H\x1b[J")
	_, _ = e.Write([]byte(b.String()))
}

// renderedScreenRows is e's screen rendered, a line a row, without the blank rows
// below its last text.
func renderedScreenRows(e *sessionScreen) []string {
	rows := strings.Split(e.Render(), "\n")
	for i := range rows {
		rows[i] = strings.TrimRight(rows[i], " ")
	}
	for len(rows) > 0 && stripANSI(rows[len(rows)-1]) == "" {
		rows = rows[:len(rows)-1]
	}
	return rows
}

// replayDump replays a dump's output ring into e, as it was recorded: its
// sizes and its output, in order. after, if given, runs after each step.
func replayDump(e *sessionScreen, d *scrollbackDump, after ...func()) {
	step := func() {
		for _, f := range after {
			f()
		}
	}
	if d.BaseCols > 0 && d.BaseRows > 0 {
		e.Resize(d.BaseCols, d.BaseRows)
		step()
	}
	for _, en := range d.Entries {
		if en.Cols > 0 && en.Rows > 0 {
			e.Resize(en.Cols, en.Rows)
			step()
			continue
		}
		if len(en.Data) > 0 {
			_, _ = e.Write(en.Data)
			step()
		}
	}
}

// historySeam is how much of history to put back before the ring is
// replayed: all of it but the tail the ring draws again (again: what the
// replay alone renders). The replay ends where the history ends (both end
// with the screen), so the tail is again from one of its first non-blank
// lines on (a ring cut mid-frame begins with debris), matched to the
// history's end: first at the one place that lines both ends up, else at a
// later occurrence of that line. The history goes in up to the match — the
// debris before it in again is drawn as extra lines, never in place of
// history. With no match of three lines or more, the ring draws nothing of
// it and all of it goes in. Bounded: repetitive history (one line ten
// thousand times) costs what the direct alignment does.
func historySeam(history, again []string) int {
	plain := func(ls []string) []string {
		out := make([]string, len(ls))
		for i, l := range ls {
			out[i] = strings.TrimRight(stripANSI(l), " ")
		}
		return out
	}
	h, r := plain(history), plain(again)
	run := func(p, j int) int {
		n := 0
		for p+n < len(h) && j+n < len(r) && h[p+n] == r[j+n] {
			n++
		}
		return n
	}
	var at map[string][]int
	best, bestRun, budget := len(h), 0, 4*len(h)+4*len(r)+1024
	for j := 0; j < len(r) && j < 64; j++ {
		if r[j] == "" {
			continue
		}
		direct := len(h) - len(r) + j
		if direct >= 0 && direct < len(h) && h[direct] == r[j] {
			if n := run(direct, j); direct+n == len(h) {
				if j+n == len(r) {
					return direct // both ends line up: the tail is the replay, exactly
				}
				if n > bestRun {
					best, bestRun = direct, n
				}
			}
		}
		if at == nil {
			at = make(map[string][]int, len(h))
			for i, l := range h {
				if l != "" {
					at[l] = append(at[l], i)
				}
			}
		}
		// Later occurrences only: a match must run to the history's end, and
		// one starting before the direct alignment would outrun the replay.
		occ := at[r[j]]
		for k := len(occ) - 1; k >= 0 && occ[k] > direct && budget > 0; k-- {
			p := occ[k]
			n := run(p, j)
			budget -= n + 1
			if p+n == len(h) && n > bestRun {
				best, bestRun = p, n
			}
		}
	}
	if bestRun < 3 {
		return len(h)
	}
	return best
}

// renderedHistory is renderScrollback of the live emulator, kept from one dump
// to the next (saveRestore dumps every restoreSaveEvery while output flows)
// while the scrollback is unchanged. What tells a change: its length, its
// first and last lines, and its newest non-blank line by identity — the vt
// scrollback clones every non-blank line it is pushed, so a push makes a new
// one — with how far from the end that line is (pushes of blank lines move
// it). Length and end lines alone collide once the scrollback is at its cap
// and repeated lines sit at both ends: one in, one out, the same three
// values, and the history before the push handed out. Caller holds
// screenMu; the slice returned is not to be changed.
func (a *Agent) renderedHistory() []string {
	key := a.screen.HistoryKey()
	if a.histCache != nil && a.histKey == key {
		return a.histCache
	}
	h := a.screen.History()
	if a.scrollbackLines > 0 && len(h) > a.scrollbackLines {
		h = h[len(h)-a.scrollbackLines:]
	}
	a.histCache, a.histKey = h, key
	return a.histCache
}

// historyKey names a scrollback's content for renderedHistory.
func historyKey(lines []uv.Line) string {
	n := len(lines)
	if n == 0 {
		return "0"
	}
	newest, at := uintptr(0), -1
	for i := n - 1; i >= 0; i-- {
		if len(lines[i]) > 0 {
			newest, at = uintptr(unsafe.Pointer(unsafe.SliceData(lines[i]))), n-1-i
			break
		}
	}
	return fmt.Sprintf("%d\x00%s\x00%s\x00%x@%d", n, lines[0].Render(), lines[n-1].Render(), newest, at)
}
