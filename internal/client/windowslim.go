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

const (
	// relayAckSlow is an ack time that makes the pictures lighter by a step;
	// relayAckFast, held for relayFastRun frames in a row, heavier by one.
	relayAckSlow = 350 * time.Millisecond
	relayAckFast = 150 * time.Millisecond
	relayFastRun = 12
)

// relayQualities are the JPEG qualities of the steps: 0 is the picture as it
// was captured. No step is below 22: a pane is mostly text, and lower than
// that small text is hard to read.
var relayQualities = []int{0, 30, 22}

// relaySlim is a stream's step, and what it has seen of the link.
type relaySlim struct {
	level   int
	fastRun int
	sentAt  time.Time // when the relay frame now on its way was sent
	sentSeq uint64
}

// nextSlim is the step after a relay frame took ack to be acked.
func nextSlim(level, fastRun int, ack time.Duration) (int, int) {
	switch {
	case ack > relayAckSlow:
		if level < len(relayQualities)-1 {
			level++
		}
		return level, 0
	case ack < relayAckFast:
		fastRun++
		if fastRun >= relayFastRun && level > 0 {
			return level - 1, 0
		}
		return level, fastRun
	}
	return level, 0
}

// sent notes a frame going on the relay; acked takes the viewer's ack of seq
// and moves the step.
func (r *relaySlim) sent(seq uint64, now time.Time) { r.sentAt, r.sentSeq = now, seq }

func (r *relaySlim) acked(seq uint64, now time.Time) {
	if r.sentAt.IsZero() || seq < r.sentSeq {
		return
	}
	r.level, r.fastRun = nextSlim(r.level, r.fastRun, now.Sub(r.sentAt))
	r.sentAt = time.Time{}
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
