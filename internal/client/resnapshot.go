// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"slices"
	"time"
)

// A width change on a viewer that is attached. An inline TUI (Claude Code)
// reprints its frame at the new width; the rows it had printed at the old
// one stay above, in the viewer's scrollback, and the person reads the
// passage twice, once each width. A viewer that joins afterwards does not:
// its history comes from the agent, deduped (dedupRepaints). So once the
// program's redraw has settled, the attached viewers that take a snapshot
// in parts are sent one as a joiner is (paced: the screen, then the history
// behind the live output, then one repaint), which replaces their
// scrollback with the deduped history. One repaint per real width change;
// the rows change on a phone's keyboard, not the columns, so the keyboard
// never causes one.

// screenRewraps says the session's copy of its screen re-wraps its history
// on a resize, as the viewer's own terminal does. Only then is a repaint
// from it at least as faithful as what an attached viewer holds: a copy
// that keeps each row at the width it was printed at (charmbracelet/x/vt)
// repainted a viewer with less than its own re-wrapped scrollback, a whole
// paragraph gone. Off with that copy; the engine that re-wraps turns it on.
const screenRewraps = false

// resnapQuiet is how long the program must have been quiet after the width
// change before the re-snapshot: its redraw follows SIGWINCH within a few
// hundred milliseconds, and a stream still going pushes it until a gap.
const resnapQuiet = 300 * time.Millisecond

// resnapEvery is the least time between two re-snapshots: a fold opened and
// closed five times in a few seconds is one repaint, at the end, not five
// full snapshots to every attached viewer.
const resnapEvery = 2 * time.Second

// armResnapshot starts the wait after a width change (applyEffectiveSize).
func (a *Agent) armResnapshot() {
	a.snapMu.Lock()
	defer a.snapMu.Unlock()
	if a.resnapTimer != nil {
		a.resnapTimer.Stop()
	}
	a.resnapTimer = time.AfterFunc(resnapQuiet, a.resnapshotWhenQuiet)
}

// resnapshotWhenQuiet fires at the end of the wait: with output since,
// the wait goes on from that output; quiet, the sender is told.
func (a *Agent) resnapshotWhenQuiet() {
	since := time.Since(time.Unix(0, a.lastOutputAt.Load()))
	a.snapMu.Lock()
	wait := resnapQuiet - since
	if last := time.Since(a.resnapAt); !a.resnapAt.IsZero() && resnapEvery-last > wait {
		wait = resnapEvery - last
	}
	if wait > 0 {
		a.resnapTimer = time.AfterFunc(wait, a.resnapshotWhenQuiet)
		a.snapMu.Unlock()
		return
	}
	a.resnapAt = time.Now()
	a.resnapWanted = len(a.snapPaced) > 0
	wake := a.snapWake
	a.snapMu.Unlock()
	if wake != nil {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
}

// takeResnapshot is who the sender owes a re-snapshot now (the paced
// viewers), and clears the owing; none until a width change has settled.
// A viewer still holding a snapshot under way to it (one it never
// acknowledged to the end: a page closed, an id from an earlier load) is
// forgotten instead: each one costs a full sealed snapshot per repaint. It
// is remembered again when it resumes.
func (a *Agent) takeResnapshot() []string {
	a.snapMu.Lock()
	defer a.snapMu.Unlock()
	if !a.resnapWanted {
		return nil
	}
	a.resnapWanted = false
	a.snapPaced = slices.DeleteFunc(a.snapPaced, func(v string) bool {
		return slices.ContainsFunc(a.snapXfers, func(x *snapXfer) bool { return x.viewer == v })
	})
	return slices.Clone(a.snapPaced)
}

// snapPacedMax bounds the viewers remembered: a page takes a new id on
// every load, so a long session would otherwise collect every load it has
// had, and each one costs a sealed copy of the snapshot per width change.
const snapPacedMax = 8

// snapViewerMax is the longest viewer id remembered: the page's own are
// "v-" and 12 characters.
const snapViewerMax = 64

// rememberPaced records a viewer that takes parts, paced, newest last.
func (a *Agent) rememberPaced(viewer string) {
	if len(viewer) > snapViewerMax {
		return
	}
	a.snapPaced = slices.DeleteFunc(a.snapPaced, func(v string) bool { return v == viewer })
	a.snapPaced = append(a.snapPaced, viewer)
	if over := len(a.snapPaced) - snapPacedMax; over > 0 {
		a.snapPaced = slices.Delete(a.snapPaced, 0, over)
	}
}

// forgetPaced drops the record: no viewer is attached (the relay said so,
// or the agent's own connection is new and they will resume).
func (a *Agent) forgetPaced() {
	a.snapMu.Lock()
	a.snapPaced, a.resnapWanted = nil, false
	if a.resnapTimer != nil {
		a.resnapTimer.Stop()
		a.resnapTimer = nil
	}
	a.snapMu.Unlock()
}
