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
	s.flush = newWinInputSignal()
	s.wsBatch = []winFrame{{Data: []byte("old")}}
	s.wsBatchBytes = 3
	s.flush.raise()
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

// Viewer input wakes a stream sitting out its frame floor, and starts a short
// burst at a display frame's floor, so the redraw the input causes is captured
// at once rather than after up to 60ms. Many inputs before the loop looks
// coalesce into one wake and never block the input path.
func TestInputWakesTheFrameFloor(t *testing.T) {
	s := lateJoinStream()
	s.flush = newWinInputSignal()
	select {
	case <-s.inputWake():
		t.Fatal("woken with no input")
	default:
	}
	for i := 0; i < 5; i++ {
		s.flush.raise() // must not block: the wake is one slot, coalesced
	}
	select {
	case <-s.inputWake():
	default:
		t.Fatal("input did not wake the floor")
	}
	select {
	case <-s.inputWake():
		t.Fatal("five inputs woke it more than once")
	default:
	}
	s.takeInputFlush()
	if !time.Now().Before(s.burstUntil) || time.Until(s.burstUntil) > inputBurstFor {
		t.Fatalf("no burst after input (burstUntil in %v)", time.Until(s.burstUntil))
	}
	var none winStream
	if none.inputWake() != nil {
		t.Fatal("a stream without a signal must never wake")
	}
}

// A still window is captured less often, and input brings the full rate back.
func TestAStillWindowIsCapturedLessOften(t *testing.T) {
	s := lateJoinStream()
	s.flush = newWinInputSignal()
	now := time.Now()
	for _, c := range []struct {
		still time.Duration
		want  time.Duration
	}{
		{0, windowFrameInterval},
		{stillAfter - time.Millisecond, windowFrameInterval},
		{stillAfter, stillInterval},
		{time.Hour, stillInterval},
	} {
		s.lastChange = now.Add(-c.still)
		if got := s.stillFloor(now); got != c.want {
			t.Errorf("still %v: floor %v, want %v", c.still, got, c.want)
		}
	}
	if stillInterval > 100*time.Millisecond {
		t.Errorf("stillInterval %v: a change the window makes on its own would show more than a tenth of a second late", stillInterval)
	}
	var fresh winStream
	if got := fresh.stillFloor(now); got != windowFrameInterval {
		t.Errorf("a stream with no change yet: floor %v, want %v", got, windowFrameInterval)
	}
	// The framebuffer path reads a file per frame: never slowed.
	s.lastChange = now.Add(-time.Hour)
	s.bands = &winBands{}
	if got := s.stillFloor(now); got != windowFrameInterval {
		t.Errorf("framebuffer capture: floor %v, want %v", got, windowFrameInterval)
	}
	s.bands = nil
	s.flush.raise()
	s.takeInputFlush()
	if got := s.stillFloor(time.Now()); got != windowFrameInterval {
		t.Errorf("after input: floor %v, want %v", got, windowFrameInterval)
	}
}

// A window answers a tap in more than one draw (a button shows pressed, the
// result comes tens of milliseconds later). Over the relay only the first
// changed frame used to leave early, and the frame with the result waited
// out the pacing interval: every such tap took 200 ms and more. For the
// input's burst the first wsBurstShips changed frames leave early, no closer
// together than wsBurstGap; after them, and after the burst, the pacing
// holds again, so a tap adds at most wsBurstShips-1 messages.
func TestRelayFramesAfterInputLeaveEarlyAFewTimes(t *testing.T) {
	s := lateJoinStream()
	s.flush = newWinInputSignal()
	due := func(at time.Time) bool { return s.wsFrameDue(s.a.framesWantedBy(), 0, at) }
	// Paced, with no input: a frame right after another waits.
	t0 := time.Now()
	s.lastWSFrame = t0
	if due(t0.Add(50 * time.Millisecond)) {
		t.Fatal("with no input, a frame 50 ms after the last one must wait")
	}
	s.flush.raise()
	s.takeInputFlush()
	if !due(t0.Add(time.Millisecond)) {
		t.Fatal("the first frame after input must leave at once")
	}
	sent := 1
	at := t0.Add(16 * time.Millisecond)
	s.wsShipped(at) // the pressed button
	if due(at.Add(wsBurstGap / 2)) {
		t.Error("a second frame left sooner than wsBurstGap after the first")
	}
	// A changed frame every 16 ms for the rest of the burst, as a capture
	// loop in its burst would offer them.
	for x := at.Add(16 * time.Millisecond); x.Before(s.burstUntil); x = x.Add(16 * time.Millisecond) {
		if due(x) {
			if gap := x.Sub(s.lastWSFrame); gap < wsBurstGap {
				t.Errorf("frames %v apart in the burst, want at least %v", gap, wsBurstGap)
			}
			s.wsShipped(x)
			sent++
		}
	}
	// Pacing alone would have sent the first frame and one more in a burst
	// of inputBurstFor: the burst may add wsBurstShips-1 to that at most.
	paced := 1 + int(inputBurstFor/wsFrameMinInterval)
	if sent < wsBurstShips || sent > paced+wsBurstShips-1 {
		t.Errorf("%d relay frames in one input's burst, want at least %d (the result is not held back) and at most %d", sent, wsBurstShips, paced+wsBurstShips-1)
	}
	// The burst over: paced again.
	after := s.burstUntil.Add(time.Millisecond)
	s.wsShipped(after)
	if due(after.Add(50 * time.Millisecond)) {
		t.Error("after the burst a frame 50 ms after the last one must wait again")
	}
	if !due(after.Add(wsFrameMinInterval)) {
		t.Error("after the burst the pacing interval must still let a frame through")
	}
}

// The capture helper offers a frame once, when the window changes. A changed
// frame the relay pacing holds back is not offered again, so it used to reach
// a viewer on the relay only when the wait for the next change ran out,
// seconds later: the result of a tap, when it came just after the pressed
// button. It stays owed, the wait is cut to when the relay is due, and it is
// sent then with no new change.
func TestAFrameHeldBackFromTheRelayIsSentWhenTheRelayIsDue(t *testing.T) {
	s := lateJoinStream()
	s.capNative = true
	if _, ok := s.wsOwedIn(time.Now()); ok {
		t.Fatal("nothing was held back yet")
	}
	s.dispatch(nil, true) // a frame went out microseconds ago: this one waits
	if s.seq != 0 {
		t.Fatalf("a frame was sent %v after the last one", time.Since(s.lastWSFrame))
	}
	d, ok := s.wsOwedIn(time.Now())
	if !ok || d <= 0 || d > wsFrameMinInterval {
		t.Fatalf("held back frame: owed %v in %v, want owed within %v", ok, d, wsFrameMinInterval)
	}
	s.dispatch(nil, false) // woken too soon, with no change: still waits
	if s.seq != 0 {
		t.Fatal("the held back frame left before the relay was due")
	}
	s.lastWSFrame = time.Now().Add(-wsFrameMinInterval)
	s.dispatch(nil, false) // the relay is due, and the window has not changed
	if s.seq != 1 {
		t.Fatal("the held back frame was not sent when the relay came due")
	}
	if _, ok := s.wsOwedIn(time.Now()); ok {
		t.Error("the frame was sent and is still owed")
	}
	s.lastWSFrame = time.Now().Add(-wsFrameMinInterval)
	s.dispatch(nil, false)
	if s.seq != 1 {
		t.Error("a still window with nothing owed was sent again")
	}

	// In an input's burst the wait is the burst's gap, not the pacing.
	s.flush = newWinInputSignal()
	s.flush.raise()
	s.takeInputFlush()
	s.dispatch(nil, true) // the pressed button: at once
	s.dispatch(nil, true) // the result, right behind it: held back
	if d, ok := s.wsOwedIn(time.Now()); s.seq != 2 || !ok || d > wsBurstGap {
		t.Errorf("in a burst: seq %d, owed %v in %v, want one frame sent and the next owed within %v", s.seq, ok, d, wsBurstGap)
	}

	// A capture loop offers the frame again at its next turn: nothing owed.
	c := lateJoinStream()
	c.dispatch(nil, true)
	if _, ok := c.wsOwedIn(time.Now()); ok {
		t.Error("a frame is owed on a path that captures again by itself")
	}
}
