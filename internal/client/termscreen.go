// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"bytes"
	"fmt"
	"hash/fnv"
	"regexp"
	"strconv"
	"strings"
	"sync"

	uv "github.com/charmbracelet/ultraviolet"

	"reminal/internal/vtwasm"
)

// sessionScreen is the session's own copy of its terminal: what the program
// in it drew, kept the way a terminal keeps it, so a viewer that joins is
// shown it. A resize re-wraps the scrollback to the new width, bottom-anchored,
// as a terminal's own does (vtwasm): a narrower screen loses nothing, and a
// line wrapped for one width is re-wrapped for another. Safe for concurrent
// use: the agent reads it under screenMu, and the attention probe and tests
// read it as they do.
type sessionScreen struct {
	t *vtwasm.Terminal
	// mu guards everything below: what is kept of the terminal's answers
	// (vtwasm serializes the calls into it itself).
	mu sync.Mutex
	// maxLines bounds History: the terminal keeps whole pages of rows, so
	// its own bound is loose, and what it keeps past this is not shown.
	maxLines int
	// gen counts the changes to it (a write, a resize): History and Render
	// are kept for the gen they were made at, so a join to a session that
	// has not moved formats nothing. historyGen and renderGen are gen+1 of
	// what each was made at; zero, never made.
	gen                   uint64
	history               []string
	historyKey            string
	historyGen, renderGen uint64
	render                string
	// mainHistory is the history as of the main screen, held while a
	// program is on the alternate screen: the terminal formats only the
	// screen it is on, and the alternate one has no scrollback, so a join
	// to vim or a full-screen Claude Code would paint none. Nothing scrolls
	// off the main screen while the program is on the other, so it is as
	// it was at the switch (refreshed at the switch, Write).
	mainHistory    []string
	mainHistoryKey string
	// tail is the end of the last write (entersAltScreen).
	tail []byte
	// failed is the first write the terminal refused (memory it would not
	// give): from then on its copy is not what the program drew (Err).
	failed error
	// pruned says the terminal let the history's oldest lines go since
	// TakePruned was last asked.
	pruned bool
	// count is how many lines its history holds (uncapped) when it held
	// count.rows rows: kept so a resize need not format all of it
	// (HistoryLines).
	count lineCount
}

// newSessionScreen is a screen of cols by rows keeping at most maxLines of
// scrollback (none past the default when maxLines is not positive).
func newSessionScreen(cols, rows, maxLines int) (*sessionScreen, error) {
	if maxLines <= 0 {
		maxLines = defaultScreenLines
	}
	t, err := vtwasm.New(cols, rows, maxLines*historyRowsPerLine)
	if err != nil {
		return nil, err
	}
	return &sessionScreen{t: t, maxLines: maxLines}, nil
}

// defaultScreenLines bounds the scrollback when no limit was set.
const defaultScreenLines = 20000

// historyRowsPerLine is how many rows the terminal keeps for each line of
// the bound: its own cap counts rows, and a line re-wrapped for a narrower
// screen is more of them (180 columns to a phone's 60 is three), so a cap of
// maxLines rows lost two thirds of a long session's oldest lines as the
// phone joined. Four keeps every line through a 240-to-60 narrowing; what
// History hands out is still at most maxLines lines.
const historyRowsPerLine = 4

func (s *sessionScreen) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// The switch can be split across two writes: the end of the last one
	// is read with the start of this one too.
	seam := append(s.tail, p[:min(len(p), altTail)]...)
	sw := entersAltScreen(p) || entersAltScreen(seam)
	if len(p) >= altTail {
		s.tail = append(s.tail[:0], p[len(p)-altTail:]...)
	} else {
		s.tail = append(s.tail[:0], seam[max(0, len(seam)-altTail):]...)
	}
	if !s.t.AltScreen() && sw {
		// What scrolled off up to now, kept for a join while the program
		// is on the alternate screen.
		s.historyLocked()
	}
	s.gen++
	n, err := s.t.Write(p)
	if err != nil && s.failed == nil {
		s.failed = err
	}
	return n, err
}

// entersAltScreen says p switches to the alternate screen: DECSET 1049, 1047
// or 47, as full-screen programs do, alone or among other modes
// ("ESC[?1049;2004h").
func entersAltScreen(p []byte) bool {
	for _, m := range decset.FindAllSubmatch(p, -1) {
		for _, mode := range bytes.Split(m[1], []byte(";")) {
			switch string(mode) {
			case "1049", "1047", "47":
				return true
			}
		}
	}
	return false
}

// decset is a DEC private mode set, its modes captured.
var decset = regexp.MustCompile(`\x1b\[\?([0-9;]+)h`)

// altTail is how much of the last write is kept to find a switch split
// across two: longer than any DECSET that holds one.
const altTail = 32

// Resize makes it cols by rows, re-wrapping its scrollback. A height change
// is done as the viewer's xterm.js does it, since what the viewer shows is
// what the person sees and what the program redraws over: a shrink first
// drops the rows below the cursor, then scrolls the top into history, so
// the cursor ends on the bottom row; a grow with the cursor there pulls the
// rows back out of history (the terminal does that itself). The terminal on
// its own kept the rows below the cursor on a shrink, so after a phone
// keyboard closed Claude Code's cursor was not on the bottom row, its grow
// added blank rows, and its redraw left a stale copy of its frame in the
// history, shown twice by every join.
func (s *sessionScreen) Resize(cols, rows int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gen++
	// The count of the history's lines is carried across (HistoryLines):
	// its tail read before and after, and the lines it gained or lost found
	// between them (tailShift). Not on the alternate screen, whose resize
	// re-wraps the main screen out of sight: it is counted again later.
	alt := s.t.AltScreen()
	var before []string
	var firstBefore string
	lines, tracked := 0, false
	if !alt {
		lines = s.linesLocked()
		firstBefore = s.count.first
		_, h := s.t.Size()
		before, tracked = s.historyTail(max(h, rows))
		tracked = tracked && s.count.valid
	}
	defer func() {
		s.count.valid = false
		if !tracked {
			return
		}
		after, ok := s.historyTail(max(rows, len(before)/8))
		first := s.firstLineLocked()
		if first != firstBefore {
			// The re-wrap took the scrollback past its budget: its oldest
			// lines went.
			s.pruned = true
			return
		}
		if d, found := tailShift(before, after); ok && found {
			s.count = lineCount{valid: true, lines: lines + d, rows: s.t.ScrollbackRows(), first: first}
		}
	}()
	if _, h := s.t.Size(); rows < h && !alt {
		if x, y := s.t.Cursor(); y < h-1 {
			if drop := min(h-rows, h-1-y); drop > 0 {
				// Delete the rows under the cursor (DL at the row below
				// it, the cursor put back), which leaves blank rows at the
				// bottom: the terminal trims those before it scrolls.
				_, _ = s.t.Write([]byte(fmt.Sprintf("\x1b[%d;1H\x1b[%dM\x1b[%d;%dH", y+2, drop, y+1, x+1)))
			}
		}
	}
	_ = s.t.Resize(cols, rows)
}

// Gen is how many times it has changed: what a cache of it is kept by.
func (s *sessionScreen) Gen() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gen
}

func (s *sessionScreen) Width() int  { c, _ := s.t.Size(); return c }
func (s *sessionScreen) Height() int { _, r := s.t.Size(); return r }

// Render is the screen, a row a line joined by "\n", exactly Height rows,
// each row's colours and styles its own.
func (s *sessionScreen) Render() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.renderGen == s.gen+1 {
		return s.render
	}
	out, err := s.t.Screen()
	if err != nil {
		out = ""
	}
	rows := splitRows(out)
	h := s.Height()
	if len(rows) > h {
		rows = rows[len(rows)-h:]
	}
	for len(rows) < h {
		rows = append(rows, "")
	}
	s.render, s.renderGen = strings.Join(rows, "\n"), s.gen+1
	return s.render
}

// CursorPosition is the cursor's cell on the screen, 0-based.
func (s *sessionScreen) CursorPosition() uv.Position {
	x, y := s.t.Cursor()
	return uv.Pos(x, y)
}

func (s *sessionScreen) IsAltScreen() bool { return s.t.AltScreen() }

// History is what scrolled off the top, oldest first, the last maxLines of
// it: a line each as the program wrote it (the rows a long line wrapped
// onto joined again), with its styles. A viewer of any width wraps them anew.
func (s *sessionScreen) History() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.historyLocked()
}

// historyLocked is History under mu.
func (s *sessionScreen) historyLocked() []string {
	if s.t.AltScreen() {
		s.history, s.historyKey = s.mainHistory, s.mainHistoryKey
		return s.history
	}
	if s.historyGen == s.gen+1 {
		return s.history
	}
	out, err := s.t.History()
	if err != nil {
		out = ""
	}
	rows := splitRows(out)
	s.count = lineCount{valid: err == nil, lines: len(rows), rows: s.t.ScrollbackRows(), first: s.firstLineLocked()}
	if len(rows) > s.maxLines {
		rows = rows[len(rows)-s.maxLines:]
	}
	s.history, s.historyGen = rows, s.gen+1
	h := fnv.New64a()
	for _, ln := range s.history {
		h.Write([]byte(ln))
		h.Write([]byte{0})
	}
	s.historyKey = strconv.Itoa(len(s.history)) + ":" + strconv.FormatUint(h.Sum64(), 16)
	s.mainHistory, s.mainHistoryKey = s.history, s.historyKey
	return s.history
}

// HistoryKey names History's content: the same while what scrolled off is
// the same, however the screen below it changed.
func (s *sessionScreen) HistoryKey() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.historyLocked()
	return s.historyKey
}

// HistoryLen is how many rows have scrolled off: cheap, read on every write
// to notice a clear (ESC[3J), when it falls.
func (s *sessionScreen) HistoryLen() int { return s.t.ScrollbackRows() }

// SetMaxLines bounds its scrollback to n lines.
func (s *sessionScreen) SetMaxLines(n int) {
	if n <= 0 {
		n = defaultScreenLines
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gen++
	s.maxLines = n
	s.t.SetMaxLines(n * historyRowsPerLine)
}

// ClearScrollback forgets what scrolled off (ED 3, as a program clears it).
func (s *sessionScreen) ClearScrollback() { _, _ = s.Write([]byte("\x1b[3J")) }

// Err is why its terminal can no longer be used (a call into it trapped,
// ran past its memory or its time limit, or refused a write), or nil. Once it is not nil
// read of it is empty: its owner stops using it (Agent.dropFailedScreenLocked).
func (s *sessionScreen) Err() error {
	if err := s.t.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failed
}

// Close frees it.
func (s *sessionScreen) Close() { s.t.Close() }

// splitRows is the formatter's output a row (or line) each, without the
// resets it opens and closes each with: a row of plain text is just its
// text, and whoever paints the rows resets between them.
func splitRows(out string) []string {
	out = strings.TrimSuffix(strings.ReplaceAll(out, "\r\n", "\n"), "\n")
	if out == "" {
		return nil
	}
	rows := strings.Split(out, "\n")
	for i, r := range rows {
		for strings.HasPrefix(r, sgrReset) {
			r = r[len(sgrReset):]
		}
		for strings.HasSuffix(r, sgrReset) {
			r = r[:len(r)-len(sgrReset)]
		}
		rows[i] = r
	}
	return rows
}

// sgrReset turns every colour and style off.
const sgrReset = "\x1b[0m"

// dropFailedScreenLocked stops using the session's screen once its terminal
// has failed (sessionScreen.Err), and says so once: every read of it would
// be empty, and a join would be painted a blank screen with no history, the
// attention and login readers would read nothing. Without it, viewers are
// given the raw output instead, as with REMINAL_SNAPSHOT=0. Caller holds
// screenMu; true when it was dropped.
func (a *Agent) dropFailedScreenLocked() bool {
	if a.screen == nil {
		return false
	}
	err := a.screen.Err()
	if err == nil {
		return false
	}
	agentNotify("  reminal: the copy of this session's screen stopped working (%v); viewers that join are sent its output instead.\n", err)
	a.screen.Close()
	a.screen = nil
	a.resizeSegs = nil
	a.segsGen++
	return true
}

// haveScreen says the session has a copy of its screen to join from.
func (a *Agent) haveScreen() bool {
	a.screenMu.Lock()
	defer a.screenMu.Unlock()
	return a.screen != nil
}

// closeScreen frees the session's copy of its screen as the agent ends: its
// terminal's memory, reserved whole (vtwasm), is given back.
func (a *Agent) closeScreen() {
	a.screenMu.Lock()
	defer a.screenMu.Unlock()
	if a.screen != nil {
		a.screen.Close()
		a.screen = nil
	}
}

// lineCount is a count of the history's lines, as of a number of its rows
// and its first line (first): when the scrollback is at its budget the
// terminal lets its oldest pages go, and the first line changing says so.
type lineCount struct {
	valid       bool
	lines, rows int
	first       string
}

// firstLineLocked is the history's first line, as far as its first rows go.
func (s *sessionScreen) firstLineLocked() string {
	out, _ := s.t.HistoryRows(0, 7)
	if rows := splitRows(out); len(rows) > 0 {
		return rows[0]
	}
	return ""
}

// TakePruned says the terminal let the history's oldest lines go since it
// was last asked: every position counted from the top moved (the resize
// segments: resizeScreen).
func (s *sessionScreen) TakePruned() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.pruned
	s.pruned = false
	return p
}

// HistoryLines is len(History()) without formatting all of it: the count of
// its lines is kept from the rows that scrolled off since (only those are
// read), and across a resize from where the history's last lines went
// (Resize). A resize reads it on every step of a drag, under the lock the
// program's output waits on, and formatting a full history took 150 ms.
func (s *sessionScreen) HistoryLines() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.t.AltScreen() {
		return len(s.mainHistory)
	}
	if s.historyGen == s.gen+1 {
		return len(s.history)
	}
	return min(s.linesLocked(), s.maxLines)
}

// linesLocked is the count of the history's lines, uncapped, brought up to
// date: from the rows added since it was taken when there are more, from the
// whole history when there are fewer (a clear, the oldest pages let go) or no
// count is kept.
func (s *sessionScreen) linesLocked() int {
	r := s.t.ScrollbackRows()
	first := s.firstLineLocked()
	if s.count.valid && first != s.count.first {
		// Its oldest lines went: counted again, whole.
		s.pruned, s.count.valid = true, false
	}
	if s.count.valid && r == s.count.rows {
		return s.count.lines
	}
	if s.count.valid && r > s.count.rows {
		// From the last row counted: its line, or the end of it, comes first.
		if out, err := s.t.HistoryRows(s.count.rows-1, r-1); err == nil {
			n := len(splitRows(out))
			if s.count.rows > 0 {
				n--
			}
			s.count = lineCount{valid: true, lines: s.count.lines + n, rows: r, first: first}
			return s.count.lines
		}
	}
	out, err := s.t.History()
	n := len(splitRows(out))
	s.count = lineCount{valid: err == nil, lines: n, rows: r, first: first}
	return n
}

// historyTail is the last lines of the history, read from its last rows
// (enough for a screen's height h several times over): the first may be
// only the end of a line.
func (s *sessionScreen) historyTail(h int) ([]string, bool) {
	r := s.t.ScrollbackRows()
	out, err := s.t.HistoryRows(r-(8*h+16), r-1)
	return splitRows(out), err == nil
}

// tailShift is how many lines the history gained (or lost, negative) between
// two reads of its tail, before and after a resize: a shrink moves rows off
// the top of the screen into it, a grow pulls its last lines back down, and a
// re-wrap moves neither. Read off a pair of lines found once in each tail,
// away from both ends (a tail's first line can be the end of one, its last
// the start of one the screen goes on with); false when there is none.
func tailShift(old, now []string) (int, bool) {
	key := func(t []string, i int) string { return t[i-1] + "\x00" + t[i] }
	where := func(t []string) map[string]int {
		m := map[string]int{}
		for i := 2; i <= len(t)-2; i++ {
			k := key(t, i)
			if _, seen := m[k]; seen {
				m[k] = -1
			} else {
				m[k] = i
			}
		}
		return m
	}
	inOld, inNow := where(old), where(now)
	for i := len(old) - 2; i >= 2; i-- {
		if len(dedupWords(old[i]))+len(dedupWords(old[i-1])) == 0 {
			continue
		}
		k := key(old, i)
		if inOld[k] != i {
			continue
		}
		if j, ok := inNow[k]; ok && j > 0 {
			return (len(now) - 1 - j) - (len(old) - 1 - i), true
		}
	}
	return 0, false
}
