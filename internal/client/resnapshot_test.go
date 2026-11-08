// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"strings"
	"testing"
	"time"

	"reminal/internal/protocol"
)

// attach is a paced viewer's resume from the buffer's end: nothing owed.
func (r *pacedRig) attach(viewer string) {
	cursor := r.a.buf.LatestSeq() + 1
	r.a.noteSnapshotJoiner(viewer, snapCapParts, true, cursor)
	r.cursorCh <- cursor
}

// none is the rig with no message for a while.
func (r *pacedRig) none(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case m := <-r.msgs:
		t.Fatalf("got %s (seq %d), want nothing", m.Type, m.Seq)
	case <-time.After(d):
	}
}

// A width change that has settled re-snapshots the attached viewer as a
// joiner is: the screen part first, to that viewer. Output within the quiet
// pushes it: the live output goes first, and the snapshot comes after.
func TestAWidthChangeResnapshotsTheAttachedViewerOnceQuiet(t *testing.T) {
	a := colouredSession(t, 5, 0)
	r := startPacedRig(t, a)
	r.attach("v-test")
	r.none(t, 100*time.Millisecond)

	a.armResnapshot()
	a.record([]byte("still printing\r\n"))
	if m := <-r.msgs; m.Type != protocol.TypeData {
		t.Fatalf("got %s, want the live output before the snapshot", m.Type)
	}
	h, _ := r.part(t, <-r.msgs)
	if !h.Screen || h.To != "v-test" || !h.Resnap {
		t.Fatalf("%+v, want the screen for v-test, marked a repaint (not to be painted)", h)
	}
	// One per width change: nothing more follows.
	r.none(t, 2*resnapQuiet)
}

// A viewer that does not take parts, or none attached any more, gets none.
func TestAWidthChangeResnapshotsOnlyPacedViewersStillAttached(t *testing.T) {
	a := colouredSession(t, 5, 0)
	r := startPacedRig(t, a)
	cursor := a.buf.LatestSeq() + 1
	a.noteSnapshotJoiner("v-plain", "", false, cursor)
	r.cursorCh <- cursor
	a.armResnapshot()
	r.none(t, 3*resnapQuiet)

	r.attach("v-test")
	a.forgetPaced()
	a.armResnapshot()
	r.none(t, 3*resnapQuiet)
}

// The record is bounded, newest kept, and one viewer is in it once.
func TestPacedViewersRememberedAreBounded(t *testing.T) {
	a := &Agent{}
	for i := 0; i < 2*snapPacedMax; i++ {
		a.rememberPaced(string(rune('a' + i)))
	}
	a.rememberPaced("a")
	if n := len(a.snapPaced); n != snapPacedMax {
		t.Fatalf("%d remembered, want %d", n, snapPacedMax)
	}
	if a.snapPaced[snapPacedMax-1] != "a" || a.snapPaced[0] != string(rune('a'+snapPacedMax+1)) {
		t.Fatalf("order %q, want the oldest dropped and the re-resumed one newest", a.snapPaced)
	}
}

// Width changes in quick succession (a fold flapped) are one repaint at the
// end, not one each: the next comes resnapEvery after the last at the soonest.
func TestRepaintsInQuickSuccessionAreOne(t *testing.T) {
	a := colouredSession(t, 5, 0)
	r := startPacedRig(t, a)
	r.attach("v-test")
	a.armResnapshot()
	if h, _ := r.part(t, <-r.msgs); !h.Resnap {
		t.Fatalf("%+v, want the first repaint", h)
	}
	a.armResnapshot()
	r.none(t, resnapEvery-resnapQuiet-200*time.Millisecond)
	select {
	case m := <-r.msgs:
		if h, _ := r.part(t, m); !h.Resnap {
			t.Fatalf("%+v, want the second repaint", h)
		}
	case <-time.After(resnapEvery):
		t.Fatal("the second repaint never came")
	}
}

// A viewer still holding a snapshot under way to it (never acknowledged to
// the end: a page closed) is forgotten at the next repaint, not sent another
// full one; and an id no page makes is not remembered at all.
func TestRepaintsForgetAViewerThatNeverFinished(t *testing.T) {
	a := &Agent{}
	a.rememberPaced("v-gone")
	a.rememberPaced("v-here")
	a.rememberPaced(strings.Repeat("v", snapViewerMax+1))
	a.snapXfers = []*snapXfer{{viewer: "v-gone", id: "x", n: 9}}
	a.resnapWanted = true
	if got := a.takeResnapshot(); len(got) != 1 || got[0] != "v-here" {
		t.Fatalf("repainted %q, want v-here alone", got)
	}
	if len(a.snapPaced) != 1 || a.snapPaced[0] != "v-here" {
		t.Fatalf("remembered %q, want v-here alone", a.snapPaced)
	}
}
