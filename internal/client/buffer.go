// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import "sync"

// scrollback holds encrypted terminal output keyed by a monotonically
// increasing sequence number. The newest entries are kept; old entries are
// dropped once total ciphertext bytes exceed maxBytes, so a reconnecting
// viewer can replay everything it missed (up to the buffer size).
type scrollback struct {
	mu       sync.Mutex
	entries  []scrollEntry
	nextSeq  uint64
	bytes    int
	maxBytes int
	notify   chan struct{}
	// baseCols/baseRows is the geometry in effect at the OLDEST retained
	// entry: seeded with the initial screen size and advanced whenever a
	// resize marker is evicted, so a history rebuild always knows the width
	// to start its replay at even after the marker itself is gone.
	baseCols, baseRows int
}

type scrollEntry struct {
	Seq  uint64
	Data string // base64 ciphertext, ready to send as-is
	// Bar marks status-bar chrome (row-addressed draws, scroll-region asserts).
	// Viewers stream these like any output, but the snapshot's history REBUILD
	// skips them: replayed outside their original geometry they'd stamp bar
	// text and scroll regions into the middle of reconstructed history.
	Bar bool
	// Cols/Rows > 0 marks a RESIZE MARKER (Data is empty): the PTY changed to
	// this geometry before the next entry's bytes were emitted. The snapshot's
	// history rebuild resizes its replay emulator in lockstep so each segment
	// re-renders at the width it was written for — replaying 120-col output at
	// 100 cols wraps every line into garbage. Never streamed to viewers (they
	// get real resize messages) and skipped by the catch-up sender.
	Cols, Rows int
}

func newScrollback(maxBytes int) *scrollback {
	return &scrollback{
		maxBytes: maxBytes,
		notify:   make(chan struct{}, 1),
	}
}

// Append records a new chunk and returns its assigned sequence number.
// Older entries are evicted until total bytes fit under maxBytes.
func (s *scrollback) Append(data string) uint64 { return s.append(data, false) }

// AppendBar records status-bar chrome: streamed to viewers like any output but
// skipped by the snapshot history rebuild (see scrollEntry.Bar).
func (s *scrollback) AppendBar(data string) uint64 { return s.append(data, true) }

func (s *scrollback) append(data string, bar bool) uint64 {
	s.mu.Lock()
	s.nextSeq++
	seq := s.nextSeq
	s.entries = append(s.entries, scrollEntry{Seq: seq, Data: data, Bar: bar})
	s.bytes += len(data)
	for s.bytes > s.maxBytes && len(s.entries) > 1 {
		if s.entries[0].Cols > 0 {
			s.baseCols, s.baseRows = s.entries[0].Cols, s.entries[0].Rows
		}
		s.bytes -= len(s.entries[0].Data)
		s.entries = s.entries[1:]
	}
	s.mu.Unlock()
	select {
	case s.notify <- struct{}{}:
	default:
	}
	return seq
}

// AppendResize records a PTY geometry change so the snapshot history rebuild
// can replay each output segment at the width it was emitted for. Consecutive
// markers coalesce (only the latest geometry before the next output matters),
// so a burst of resizes with no output in between costs one entry.
func (s *scrollback) AppendResize(cols, rows int) {
	if cols <= 0 || rows <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := len(s.entries); n > 0 && s.entries[n-1].Cols > 0 {
		s.entries[n-1].Cols, s.entries[n-1].Rows = cols, rows
		return
	}
	s.nextSeq++
	s.entries = append(s.entries, scrollEntry{Seq: s.nextSeq, Cols: cols, Rows: rows})
}

// SetBase seeds the geometry in effect before the first buffered entry (the
// initial screen size). See baseCols.
func (s *scrollback) SetBase(cols, rows int) {
	s.mu.Lock()
	s.baseCols, s.baseRows = cols, rows
	s.mu.Unlock()
}

// Base returns the geometry in effect at the oldest retained entry (0,0 if
// never seeded).
func (s *scrollback) Base() (cols, rows int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.baseCols, s.baseRows
}

// From returns a copy of entries with Seq >= fromSeq.
func (s *scrollback) From(fromSeq uint64) []scrollEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]scrollEntry, 0, len(s.entries))
	for _, e := range s.entries {
		if e.Seq >= fromSeq {
			out = append(out, e)
		}
	}
	return out
}

// OldestSeq returns the seq of the oldest entry still buffered, or 0 if empty.
// A resume cursor at or below this means the viewer wants history we've partly
// evicted — the caller sends a snapshot instead of an incomplete raw replay.
func (s *scrollback) OldestSeq() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.entries) == 0 {
		return 0
	}
	return s.entries[0].Seq
}

// LatestSeq returns the seq of the newest appended entry (0 if none). Used to
// tag a snapshot frame so viewers treat it as covering everything through it.
func (s *scrollback) LatestSeq() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nextSeq
}

// NextSeq returns the seq the next Append() will assign. Used to detect
// "viewer is asking us to resume from a seq we've never reached" — i.e.
// the viewer was talking to a previous agent incarnation and its
// lastSeq is far past anything we know about (most commonly after a
// hot-restart where the new agent's counter starts from zero).
func (s *scrollback) NextSeq() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nextSeq + 1
}

// Notify returns a channel that receives a struct{} whenever Append happens.
// It is buffered to depth 1 and edge-triggered: a sender that misses a tick
// just loops back and reads the buffer again, so no data is lost.
func (s *scrollback) Notify() <-chan struct{} {
	return s.notify
}
