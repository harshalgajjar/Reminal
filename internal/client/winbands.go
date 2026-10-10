// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"bytes"
	"errors"
	"image"
	"image/jpeg"
	"time"
)

// Changed bands. On a backend that hands over a window's pixels (an Xvfb's
// framebuffer file: xvfbfb.go), a frame that differs from the one before it
// only in part is sent as that part: a JPEG of the smallest block-aligned
// rectangle holding every changed pixel, which the viewer draws over the
// picture it already has. A keystroke in a terminal changes a dozen rows of
// several hundred, so a band is a fraction of the bytes and of the encoding.
//
// Only to viewers that said they can draw one (bands:1 in window_ctl start).
// While anyone else watches the window, a viewer too old to say who it is
// included, every frame goes whole. A band names the frame it was cut against
// (base); a viewer whose picture is not that frame draws nothing and asks for
// a whole one — the request an H.264 viewer makes on a gap.

const (
	// bandAlign is a JPEG's 4:2:0 block: a band starting and ending on the
	// frame's own block grid joins the picture under it without a seam.
	bandAlign = 16
	// bandMaxShare: a band over this share of the frame goes whole — it saves
	// too little to be worth the viewer drawing it over something.
	bandMaxShare = 0.6
	// bandFullEvery is how often a whole frame goes out while bands do: only
	// if any band went out since the last whole one.
	bandFullEvery = 10 * time.Second
	// bandQuality is the quality capture's own JPEGs are made at.
	bandQuality = 55
)

// errNoRawFrames: this backend, here and now, has no pixels to hand over.
var errNoRawFrames = errors.New("no raw frames on this display")

// pixelFramer is a backend that can capture a window as pixels. nil pixels with
// no error: the same picture as the one whose sum was last.
type pixelFramer interface {
	capturePixels(w winInfo, last uint64, haveLast bool) (*image.RGBA, uint64, error)
}

// winBands is a stream's state while it sends bands. Each picture is a
// window's worth of pixels (up to ~4 MB): the stream drops it all the moment
// bands stop, and when the stream ends.
type winBands struct {
	cur    *image.RGBA // the newest picture captured
	curSum uint64
	// sent is the picture held by every sink of the last frame that was not
	// only a probe, sentSeq that frame's seq, sinks its sinks.
	sent    *image.RGBA
	sentSum uint64
	sentSeq uint64
	sinks   winSinks
	// full is cur as a whole JPEG, once one was made; fullSum says of which.
	full    []byte
	fullSum uint64
	// fullAt is when the last whole frame went out, sinceFull whether a band
	// has gone out after it, wantFull that the next frame must be whole.
	fullAt    time.Time
	sinceFull bool
	wantFull  bool
}

// captureBand captures the window as pixels when bands are on for it; ok
// false, and capture goes on as it would without them, otherwise.
func (s *winStream) captureBand() (f winFrame, ok bool) {
	rf, can := s.b.(pixelFramer)
	if !can || !s.a.windowBandsOK(s.w.ID) {
		s.bands = nil
		return winFrame{}, false
	}
	bs := s.bands
	if bs == nil {
		bs = &winBands{}
	}
	img, sum, err := rf.capturePixels(s.w, bs.curSum, bs.cur != nil)
	if err != nil {
		s.bands = nil
		return winFrame{}, false
	}
	if img != nil {
		bs.cur, bs.curSum = img, sum
	}
	s.bands, s.lastImg = bs, nil
	return winFrame{Band: true}, true
}

// changed reports whether the newest picture is not what was last sent.
func (bs *winBands) changed() bool { return bs.sent == nil || bs.curSum != bs.sentSum }

// refreshDue reports whether a whole frame is owed: bands have gone out and
// none whole for bandFullEvery.
func (bs *winBands) refreshDue() bool { return bs.sinceFull && time.Since(bs.fullAt) >= bandFullEvery }

// payload is the JPEG to send to sinks: a band, with its rectangle, when one
// will do, else the whole picture.
func (bs *winBands) payload(sinks winSinks, force bool) ([]byte, *image.Rectangle, error) {
	whole := force || bs.wantFull || bs.sent == nil || len(sinks.probe) > 0 ||
		!sameSinks(sinks, bs.sinks) || bs.sent.Rect != bs.cur.Rect
	if !whole {
		r := changedBand(bs.sent, bs.cur)
		if !r.Empty() && float64(r.Dx()*r.Dy()) <= bandMaxShare*float64(bs.cur.Rect.Dx()*bs.cur.Rect.Dy()) {
			out, err := encodeBand(bs.cur.SubImage(r))
			return out, &r, err
		}
	}
	if bs.full == nil || bs.fullSum != bs.curSum {
		out, err := encodeBand(bs.cur)
		if err != nil {
			return nil, nil, err
		}
		bs.full, bs.fullSum = out, bs.curSum
	}
	return bs.full, nil, nil
}

// commit records that the newest picture went out as seq to sinks. A frame
// only probing a channel is no one's base: whoever else watches does not hold
// it.
func (bs *winBands) commit(sinks winSinks, seq uint64, whole bool) {
	if whole {
		bs.fullAt, bs.sinceFull = time.Now(), false
	} else {
		bs.sinceFull = true
	}
	if !sinks.expectsAck() {
		return
	}
	bs.sent, bs.sentSum, bs.sentSeq = bs.cur, bs.curSum, seq
	bs.sinks = winSinks{confirmed: append([]*rtcPeer(nil), sinks.confirmed...), ws: sinks.ws}
}

// sameSinks reports whether a and b send to the same viewers (probes aside).
func sameSinks(a, b winSinks) bool {
	if a.ws != b.ws || len(a.confirmed) != len(b.confirmed) {
		return false
	}
	for _, p := range a.confirmed {
		found := false
		for _, q := range b.confirmed {
			if p == q {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func encodeBand(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: bandQuality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// changedBand is the smallest rectangle, on the bandAlign grid, holding every
// pixel where a and b (the same size, at the origin) differ; empty when none
// do.
func changedBand(a, b *image.RGBA) image.Rectangle {
	w, h := b.Rect.Dx(), b.Rect.Dy()
	rowA := func(y int) []byte { return a.Pix[y*a.Stride : y*a.Stride+w*4] }
	rowB := func(y int) []byte { return b.Pix[y*b.Stride : y*b.Stride+w*4] }
	y0 := 0
	for y0 < h && bytes.Equal(rowA(y0), rowB(y0)) {
		y0++
	}
	if y0 == h {
		return image.Rectangle{}
	}
	y1 := h
	for y1 > y0 && bytes.Equal(rowA(y1-1), rowB(y1-1)) {
		y1--
	}
	x0, x1 := w, 0
	for y := y0; y < y1; y++ {
		ra, rb := rowA(y), rowB(y)
		if bytes.Equal(ra, rb) {
			continue
		}
		i := 0
		for i < x0*4 && ra[i] == rb[i] {
			i++
		}
		x0 = min(x0, i/4)
		j := len(ra) - 1
		for j >= x1*4 && ra[j] == rb[j] {
			j--
		}
		x1 = max(x1, j/4+1)
	}
	return image.Rect(
		x0/bandAlign*bandAlign, y0/bandAlign*bandAlign,
		min(w, (x1+bandAlign-1)/bandAlign*bandAlign), min(h, (y1+bandAlign-1)/bandAlign*bandAlign),
	)
}

// windowBandsOK reports whether everyone watching a window can draw a band.
func (a *Agent) windowBandsOK(id string) bool {
	a.winMu.Lock()
	defer a.winMu.Unlock()
	return len(a.winSubs[id]) > 0 && len(a.winNoBands[id]) == 0
}
