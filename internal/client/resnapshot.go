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

// resnapQuiet is how long the program must have been quiet after the width
// change before the re-snapshot: its redraw follows SIGWINCH within a few
// hundred milliseconds, and a stream still going pushes it until a gap.
const resnapQuiet = 300 * time.Millisecond

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
	if since < resnapQuiet {
		a.resnapTimer = time.AfterFunc(resnapQuiet-since, a.resnapshotWhenQuiet)
		a.snapMu.Unlock()
		return
	}
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
func (a *Agent) takeResnapshot() []string {
	a.snapMu.Lock()
	defer a.snapMu.Unlock()
	if !a.resnapWanted {
		return nil
	}
	a.resnapWanted = false
	return slices.Clone(a.snapPaced)
}

// snapPacedMax bounds the viewers remembered: a page takes a new id on
// every load, so a long session would otherwise collect every load it has
// had, and each one costs a sealed copy of the snapshot per width change.
const snapPacedMax = 8

// rememberPaced records a viewer that takes parts, paced, newest last.
func (a *Agent) rememberPaced(viewer string) {
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
	a.snapMu.Unlock()
}
