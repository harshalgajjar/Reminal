// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Terminal geometry has one owner: the agent.
//
// Each viewer reports the wrap it can show (cols × rows). This book stores
// those reports, and the PTY becomes min(widths) × min(heights) so every
// attached screen can display the whole terminal. Viewers then render
// min(their wrap, the PTY the agent broadcasts). They never decide the
// PTY themselves.
//
// The host terminal is a mirror of that PTY, not a second wrap. Live
// GetSize of stdout is not mixed into the min: a PTY SIGWINCH can change
// that metric (scrollbar, echo) and feed back into another SIGWINCH.
// Extra rows already scroll on the host; extra columns wrap the same way.
//
// Garbage measurements (a 5-row wrap mid-keyboard-animation) are ignored.
// A burst of reports is one resize, applied when the viewport goes quiet.
// Keyboard open is a real shrink of the visible window: the PTY follows.

const (
	minTermCols = 20
	minTermRows = 8
)

// resizeSettle is how long a burst of wrap reports may run before the PTY
// moves. Long enough that a soft-keyboard slide is one resize.
var resizeSettle = 200 * time.Millisecond

// resizeGrowStable is how long a 1–2 row same-width grow must hold still.
// That is the mobile address bar, not a keyboard, and must not SIGWINCH
// the app on every scroll.
var resizeGrowStable = 2 * time.Second

type termSize struct {
	cols, rows uint16
}

func (s termSize) valid() bool {
	return s.cols >= minTermCols && s.rows >= minTermRows
}

func (s termSize) zero() bool {
	return s.cols == 0 || s.rows == 0
}

// viewerSizeBook is the agent's per-session wrap table.
type viewerSizeBook struct {
	mu      sync.Mutex
	wraps   map[string]termSize // latest valid report per viewer id
	settled termSize            // min of wraps, adopted when quiet (or on first)
	applied termSize            // last size actually sent to the PTY
	timer   *time.Timer
}

func minTermSizes(wraps map[string]termSize) termSize {
	var out termSize
	for _, s := range wraps {
		if out.cols == 0 || s.cols < out.cols {
			out.cols = s.cols
		}
		if out.rows == 0 || s.rows < out.rows {
			out.rows = s.rows
		}
	}
	return out
}

// effectivePTYSize is the size the PTY should be. A settled viewer wrap
// owns geometry outright — host GetSize is not a cap, because that
// number moves when the PTY itself resizes. With no viewer wrap, the
// PTY matches the host terminal.
func effectivePTYSize(viewer, host termSize) termSize {
	if !viewer.zero() {
		return viewer
	}
	return host
}

// report stores one viewer's wrap. Invalid sizes are ignored. Returns
// true when this is the first settled size (apply the PTY immediately).
func (b *viewerSizeBook) report(id string, cols, rows uint16) (first bool) {
	s := termSize{cols, rows}
	if !s.valid() {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.wraps == nil {
		b.wraps = map[string]termSize{}
	}
	b.wraps[id] = s
	if b.settled.zero() {
		b.settled = minTermSizes(b.wraps)
		return true
	}
	return false
}

// schedule applies the current min once reports stop arriving.
func (b *viewerSizeBook) schedule(apply func()) {
	b.mu.Lock()
	next := minTermSizes(b.wraps)
	cur := b.applied
	wait := resizeSettle
	// 1–2 extra rows at the same width is the mobile address bar, not a
	// keyboard. Wait longer so a scroll does not SIGWINCH the app.
	// A real keyboard open/close is tens of rows and uses resizeSettle:
	// one SIGWINCH after the animation, not a SIGWINCH per frame.
	if !cur.zero() && next.cols == cur.cols && next.rows > cur.rows && next.rows-cur.rows <= 2 {
		wait = resizeGrowStable
	}
	if b.timer != nil {
		b.timer.Stop()
	}
	b.timer = time.AfterFunc(wait, func() {
		b.mu.Lock()
		b.timer = nil
		b.settled = minTermSizes(b.wraps)
		b.mu.Unlock()
		apply()
	})
	b.mu.Unlock()
}

func (b *viewerSizeBook) settledSize() termSize {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.settled
}

func (b *viewerSizeBook) lastApplied() termSize {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.applied
}

// dump is the size-pipeline trace: every viewer's last wrap, the min
// those wraps imply, and what the PTY actually is.
func (b *viewerSizeBook) dump() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	ids := make([]string, 0, len(b.wraps))
	for id := range b.wraps {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		s := b.wraps[id]
		parts = append(parts, fmt.Sprintf("%s=%dx%d", id, s.cols, s.rows))
	}
	shown := strings.Join(parts, " ")
	if shown == "" {
		shown = "-"
	}
	return fmt.Sprintf("n=%d [%s] settled=%dx%d applied=%dx%d",
		len(b.wraps), shown, b.settled.cols, b.settled.rows, b.applied.cols, b.applied.rows)
}

// setApplied records the size we just put on the PTY. Returns false if
// it is unchanged, so the caller can skip a redundant SIGWINCH.
func (b *viewerSizeBook) setApplied(cols, rows uint16) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.applied.cols == cols && b.applied.rows == rows {
		return false
	}
	b.applied = termSize{cols, rows}
	return true
}

func (b *viewerSizeBook) clear() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
	b.wraps = nil
	b.settled = termSize{}
	b.applied = termSize{}
}

// forgetWraps drops every report but keeps the last applied PTY size.
// The relay only tells us a viewer left, not which one, so remaining
// viewers must re-report before we can grow.
func (b *viewerSizeBook) forgetWraps() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
	b.wraps = nil
	b.settled = termSize{}
}

func (b *viewerSizeBook) seed(wraps map[string]termSize, applied termSize) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.wraps = wraps
	b.settled = minTermSizes(wraps)
	b.applied = applied
}
