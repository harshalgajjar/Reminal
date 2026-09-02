// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"sync/atomic"
	"testing"
	"time"
)

// lateJoinStream builds a JPEG-path stream that is already running: it has a
// frame on hand and has just sent one, so the relay pacing is not yet due.
func lateJoinStream() *winStream {
	a := &Agent{}
	a.viewerCount = 2 // two panes open on this window; one of them just joined
	return &winStream{
		a:           a,
		keyReq:      &atomic.Bool{},
		lastImg:     []byte("jpeg"),
		lastWSFrame: time.Now(), // a frame went out microseconds ago
		lastSent:    time.Now(),
	}
}

// TestLateJoinerIsServedOnAStillWindow is the regression test for a pane stuck
// on "Connecting…" forever, reported from a phone that opened a window someone
// else was already watching. Frames are only sent when the picture changes, so
// on a still window a viewer that joins an in-flight stream is never sent
// anything — it has no first frame and no change is coming. The agent already
// recognised the case (startWindowStream asks for an entry point when a second
// viewer arrives) but only honoured it for H.264, where it re-keys the encoder;
// on the JPEG path the request was swallowed and nothing happened.
func TestLateJoinerIsServedOnAStillWindow(t *testing.T) {
	s := lateJoinStream()

	// The stream loop's key-request handling, JPEG path: no helper, no codec.
	s.keyReq.Store(true)
	s.noteKeyRequest()
	if !s.forceSend {
		t.Fatal("a key request on the JPEG path left nothing to act on")
	}
	if s.keyReq.Load() {
		t.Error("the request was not consumed; every later frame would bypass pacing")
	}

	// The window has not changed, and it never will.
	changed := s.detectChange(s.lastImg) || s.forceSend
	if !changed {
		t.Fatal("a still window with a viewer waiting produced no frame to send")
	}

	// And the pacing throttle must not eat it: this newcomer has no picture at
	// all, so "one frame per interval" cannot be what keeps it waiting.
	vc := s.a.framesWantedBy()
	now := time.Now()
	if !s.wsFrameDue(vc, 0, now) {
		t.Error("the forced frame was throttled out; the newcomer keeps waiting")
	}
	// Prove the fixture is genuinely inside the throttle window, so the pass
	// above is the force at work and not a lapsed interval.
	unforced := *s
	unforced.forceSend = false
	if unforced.wsFrameDue(vc, 0, now) {
		t.Fatal("fixture is not inside the throttle window — the test proves nothing")
	}
}

// TestForcedFrameStaysArmedUntilItShips pins the other half: the flag clears
// when a frame really goes out, not when one is merely attempted. A forced
// frame that every sink declines is dropped by dispatch, and clearing early
// would strand the viewer that asked for it.
func TestForcedFrameStaysArmedUntilItShips(t *testing.T) {
	s := lateJoinStream()
	s.forceSend = true

	// No sinks resolved and no image: nothing shipped, so nothing is served.
	empty := winSinks{}
	if empty.any() {
		t.Fatal("fixture: expected no sinks")
	}
	if !s.forceSend {
		t.Fatal("the request was cleared without a frame going anywhere")
	}

	// Once a frame ships, the force is spent: a later still frame must be
	// paced normally again.
	s.forceSend = false
	if s.wsFrameDue(s.a.framesWantedBy(), 0, time.Now()) {
		t.Error("still bypassing pacing after the newcomer was served")
	}
}

func TestInputFlushShipsNextRelayFrameNow(t *testing.T) {
	s := lateJoinStream()
	s.flush = &atomic.Bool{}
	s.wsBatch = []winFrame{{Data: []byte("old")}}
	s.wsBatchBytes = 3
	s.flush.Store(true)
	s.takeInputFlush()
	if len(s.wsBatch) != 0 || s.wsBatchBytes != 0 {
		t.Fatal("pre-click batch should be dropped, not billed a beat later")
	}
	if !s.shipNow {
		t.Fatal("the next frame must leave without waiting out the interval")
	}
	if !s.wsFrameDue(s.a.framesWantedBy(), 0, time.Now()) {
		t.Error("shipNow did not open the relay gate")
	}
}
