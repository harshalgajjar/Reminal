// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"bytes"
	"hash/fnv"
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
	if !s.t.AltScreen() && entersAltScreen(p) {
		// What scrolled off up to now, kept for a join while the program
		// is on the alternate screen.
		s.historyLocked()
	}
	s.gen++
	return s.t.Write(p)
}

// entersAltScreen says p switches to the alternate screen (DECSET 1049,
// 1047 or 47, as full-screen programs do).
func entersAltScreen(p []byte) bool {
	for _, seq := range []string{"\x1b[?1049h", "\x1b[?1047h", "\x1b[?47h"} {
		if bytes.Contains(p, []byte(seq)) {
			return true
		}
	}
	return false
}

// Resize makes it cols by rows, re-wrapping its scrollback.
func (s *sessionScreen) Resize(cols, rows int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gen++
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
