// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

// A window's pictures made lighter for a slow relay link.
//
// Over the relay a window's frames share the connection with the session's
// terminal, in order, one frame at a time (ackGate): what is typed waits for
// the frame on its way, so on a slow link a frame's size is the delay. The
// time a relay frame takes to be acked says how the link is doing. When it
// is long, the next pictures are encoded again at a lower JPEG quality
// before they go (same size in pixels: a band still fits the picture it is
// cut against, and the viewer needs nothing new); when it has been short
// for a while, they go back up a step. Only pictures that go on the relay
// alone: a direct channel carries no terminal, and video has its own budget.

import (
	"bytes"
	"image/jpeg"
	"time"
)

// What tells a slow link is how long a frame spent ON it, not how long its
// ack took: the ack's time is the link's round trip plus the frame's own
// time through it, and the round trip is the same for a frame of any size.
// A small frame's ack is nearly all round trip, so the quickest small frame
// lately is taken for it (relaySlim.base); until one has been seen, a round
// trip of relayTripGuess is assumed. With 350 ms on the whole ack, a frame
// that held the terminal back a quarter of a second on an ordinary round
// trip was never made lighter.
const (
	// relayOverSlow is a time over the round trip that makes the pictures
	// lighter by a step; relayOverFast, held for relayFastRun frames in a
	// row, heavier by one.
	relayOverSlow = 120 * time.Millisecond
	relayOverFast = 50 * time.Millisecond
	relayFastRun  = 12
	// relaySmallFrame is a frame small enough for its ack to be the round
	// trip; relayBaseKept is how long the quickest one stands.
	relaySmallFrame = 16 << 10
	relayBaseKept   = time.Minute
	relayTripGuess  = 100 * time.Millisecond
)

// relayQualities are the JPEG qualities of the steps: 0 is the picture as it
// was captured. No step is below 22: a pane is mostly text, and lower than
// that small text is hard to read.
var relayQualities = []int{0, 30, 22}

// relaySlim is a stream's step, and what it has seen of the link.
type relaySlim struct {
	level     int
	fastRun   int
	sentAt    time.Time // when the relay frame now on its way was sent
	sentSeq   uint64
	sentBytes int
	base      time.Duration // the link's round trip: the quickest small frame's ack lately
	baseAt    time.Time
}

// nextSlim is the step after a relay frame spent over on the link beyond
// its round trip.
func nextSlim(level, fastRun int, over time.Duration) (int, int) {
	switch {
	case over > relayOverSlow:
		if level < len(relayQualities)-1 {
			level++
		}
		return level, 0
	case over < relayOverFast:
		fastRun++
		if fastRun >= relayFastRun && level > 0 {
			return level - 1, 0
		}
		return level, fastRun
	}
	return level, 0
}

// sent notes a frame of bytes going on the relay; acked takes the viewer's
// ack of seq. A small frame's ack is the round trip (the quickest lately is
// kept); a larger one's, less the round trip, moves the step.
func (r *relaySlim) sent(seq uint64, now time.Time, bytes int) {
	r.sentAt, r.sentSeq, r.sentBytes = now, seq, bytes
}

func (r *relaySlim) acked(seq uint64, now time.Time) {
	if r.sentAt.IsZero() || seq < r.sentSeq {
		return
	}
	ack := now.Sub(r.sentAt)
	r.sentAt = time.Time{}
	if r.sentBytes < relaySmallFrame {
		if r.base == 0 || ack < r.base || now.Sub(r.baseAt) > relayBaseKept {
			r.base, r.baseAt = ack, now
		}
		return
	}
	trip := relayTripGuess
	if r.base > 0 && now.Sub(r.baseAt) <= relayBaseKept {
		trip = r.base
	}
	r.level, r.fastRun = nextSlim(r.level, r.fastRun, ack-trip)
}

// slimJPEG is img encoded again at the step's quality, or img itself when
// the step is 0, it does not decode, or the result is no smaller.
func slimJPEG(img []byte, level int) []byte {
	if level <= 0 || level >= len(relayQualities) || len(img) == 0 {
		return img
	}
	src, err := jpeg.Decode(bytes.NewReader(img))
	if err != nil {
		return img
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, src, &jpeg.Options{Quality: relayQualities[level]}); err != nil || out.Len() >= len(img) {
		return img
	}
	return out.Bytes()
}
