// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
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
	if !h.Screen || h.To != "v-test" {
		t.Fatalf("%+v, want the screen for v-test", h)
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
