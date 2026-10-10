// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"bytes"
	"hash/maphash"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
	"time"
)

// pixelWindows is a backend whose window is a picture the test paints.
type pixelWindows struct {
	linuxWindows
	img *image.RGBA
}

func (p *pixelWindows) capturePixels(_ winInfo, last uint64, haveLast bool) (*image.RGBA, uint64, error) {
	sum := maphash.Bytes(xvfbLast.seed, p.img.Pix)
	if haveLast && sum == last {
		return nil, sum, nil
	}
	cp := image.NewRGBA(p.img.Rect)
	copy(cp.Pix, p.img.Pix)
	return cp, sum, nil
}

func greyPicture(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = 0x80
	}
	return img
}

func TestChangedBand(t *testing.T) {
	a, b := greyPicture(200, 100), greyPicture(200, 100)
	if r := changedBand(a, b); !r.Empty() {
		t.Fatalf("the same picture has a band: %v", r)
	}
	b.Set(37, 50, color.RGBA{255, 0, 0, 255})
	if r, want := changedBand(a, b), image.Rect(32, 48, 48, 64); r != want {
		t.Errorf("one pixel at 37,50: band %v, want %v", r, want)
	}
	b.Set(199, 99, color.RGBA{0, 255, 0, 255}) // the corner: the band stops at the edge
	if r, want := changedBand(a, b), image.Rect(32, 48, 200, 100); r != want {
		t.Errorf("and the far corner: band %v, want %v", r, want)
	}
	c := greyPicture(200, 100)
	c.Set(0, 0, color.RGBA{1, 2, 3, 255})
	if r, want := changedBand(a, c), image.Rect(0, 0, 16, 16); r != want {
		t.Errorf("the near corner: band %v, want %v", r, want)
	}
}

// bandStream is a JPEG stream on a backend with pixels, watched by viewers
// that can draw bands.
func bandStream(t *testing.T) (*winStream, *pixelWindows) {
	t.Helper()
	a := &Agent{}
	a.addWindowSub("w1", "viewerA", true)
	pw := &pixelWindows{img: greyPicture(320, 240)}
	return &winStream{a: a, b: pw, w: winInfo{ID: "w1", W: 320, H: 240}}, pw
}

// next captures the picture and sends it to sinks as sendFrame would, giving
// back the JPEG and its band (nil: a whole frame).
func (s *winStream) nextBandFrame(t *testing.T, sinks winSinks, force bool) ([]byte, *image.Rectangle) {
	t.Helper()
	if _, ok := s.captureBand(); !ok {
		t.Fatal("bands were not on")
	}
	img, band, err := s.bands.payload(sinks, force)
	if err != nil {
		t.Fatal(err)
	}
	s.seq++
	s.bands.commit(sinks, s.seq, band == nil)
	return img, band
}

func TestBandsOnlyWhatChanged(t *testing.T) {
	s, pw := bandStream(t)
	ws := winSinks{ws: true}

	if _, band := s.nextBandFrame(t, ws, false); band != nil {
		t.Fatal("the first frame was a band")
	}
	for x := 40; x < 60; x++ {
		pw.img.Set(x, 100, color.RGBA{255, 255, 255, 255})
	}
	img, band := s.nextBandFrame(t, ws, false)
	if band == nil {
		t.Fatal("a one-line change went whole")
	}
	if want := image.Rect(32, 96, 64, 112); *band != want {
		t.Errorf("band %v, want %v", *band, want)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(img))
	if err != nil || cfg.Width != band.Dx() || cfg.Height != band.Dy() {
		t.Errorf("the band's JPEG is %dx%d (%v), want %dx%d", cfg.Width, cfg.Height, err, band.Dx(), band.Dy())
	}
	if s.bands.sentSeq != 2 {
		t.Errorf("base after two frames: %d", s.bands.sentSeq)
	}
}

func TestBandsGoWholeWhenTheyMust(t *testing.T) {
	ws := winSinks{ws: true}
	scribble := func(pw *pixelWindows) {
		pw.img.Set(10, 10, color.RGBA{uint8(time.Now().UnixNano()), 1, 1, 255})
	}
	for _, tc := range []struct {
		name string
		do   func(s *winStream, pw *pixelWindows) (winSinks, bool)
	}{
		{"asked for one (a viewer joined, or lost its place)", func(s *winStream, pw *pixelWindows) (winSinks, bool) {
			scribble(pw)
			return ws, true
		}},
		{"most of the picture changed", func(s *winStream, pw *pixelWindows) (winSinks, bool) {
			for y := 0; y < 200; y++ {
				for x := 0; x < 320; x++ {
					pw.img.Set(x, y, color.RGBA{0, 0, 0, 255})
				}
			}
			return ws, false
		}},
		{"the window changed size", func(s *winStream, pw *pixelWindows) (winSinks, bool) {
			pw.img = greyPicture(300, 240)
			return ws, false
		}},
		{"somebody else is now sent the frame", func(s *winStream, pw *pixelWindows) (winSinks, bool) {
			scribble(pw)
			return winSinks{ws: true, confirmed: []*rtcPeer{{}}}, false
		}},
		{"a channel is being probed", func(s *winStream, pw *pixelWindows) (winSinks, bool) {
			scribble(pw)
			return winSinks{ws: true, probe: []*rtcPeer{{}}}, false
		}},
		{"a whole frame is due", func(s *winStream, pw *pixelWindows) (winSinks, bool) {
			scribble(pw)
			s.bands.wantFull = true
			return ws, false
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, pw := bandStream(t)
			s.nextBandFrame(t, ws, false)
			sinks, force := tc.do(s, pw)
			if _, band := s.nextBandFrame(t, sinks, force); band != nil {
				t.Errorf("sent as a band %v", *band)
			}
		})
	}
}

// A frame that only probes a channel is held by the probed viewer alone: the
// next band is cut against what everyone else holds.
func TestProbeIsNobodysBase(t *testing.T) {
	s, pw := bandStream(t)
	ws := winSinks{ws: true}
	s.nextBandFrame(t, ws, false)
	pw.img.Set(5, 5, color.RGBA{9, 9, 9, 255})
	s.nextBandFrame(t, winSinks{probe: []*rtcPeer{{}}}, false)
	if s.bands.sentSeq != 1 {
		t.Fatalf("a probe moved the base to %d", s.bands.sentSeq)
	}
	_, band := s.nextBandFrame(t, ws, false)
	if band == nil || !image.Pt(5, 5).In(*band) {
		t.Fatalf("the change the probe carried is not in the next band: %v", band)
	}
}

// Every so often a whole frame goes out, but only after bands did: a window
// that sits still sends nothing.
func TestBandsRefreshOnlyAfterChange(t *testing.T) {
	s, pw := bandStream(t)
	ws := winSinks{ws: true}
	s.nextBandFrame(t, ws, false)
	s.bands.fullAt = time.Now().Add(-2 * bandFullEvery)
	if s.bands.refreshDue() {
		t.Fatal("a whole frame is due on a picture that never changed")
	}
	s.bands.fullAt = time.Now()
	pw.img.Set(5, 5, color.RGBA{9, 9, 9, 255})
	if _, band := s.nextBandFrame(t, ws, false); band == nil {
		t.Fatal("fixture: expected a band")
	}
	if s.bands.refreshDue() {
		t.Fatal("a whole frame is due straight after a band")
	}
	s.bands.fullAt = time.Now().Add(-2 * bandFullEvery)
	if !s.bands.refreshDue() {
		t.Fatal("no whole frame due long after a band")
	}
}

// Bands only while everyone watching can draw them, an anonymous (old) viewer
// included; and a stream that stops sending them lets its pictures go.
func TestBandsNeedEveryViewer(t *testing.T) {
	s, _ := bandStream(t)
	a := s.a
	if !a.windowBandsOK("w1") {
		t.Fatal("one viewer that can: no bands")
	}
	a.addWindowSub("w1", "viewerB", false)
	if a.windowBandsOK("w1") {
		t.Fatal("bands with a viewer that cannot draw them")
	}
	if _, ok := s.captureBand(); ok || s.bands != nil {
		t.Fatal("captured for bands, or kept the pictures, with a viewer that cannot draw them")
	}
	a.dropWindowSub("w1", "viewerB")
	if !a.windowBandsOK("w1") {
		t.Fatal("still no bands after that viewer left")
	}
	a.addWindowSub("w1", "", false)
	if a.windowBandsOK("w1") {
		t.Fatal("bands with an anonymous viewer watching")
	}
	a.forgetWindowSubs("w1")
	a.addWindowSub("w1", "viewerA", true)
	if !a.windowBandsOK("w1") {
		t.Fatal("an ended stream remembered its anonymous viewer")
	}
	if _, ok := s.captureBand(); !ok || s.bands == nil {
		t.Fatal("fixture: expected bands on")
	}
	s.cleanup()
	if s.bands != nil {
		t.Fatal("an ended stream kept its pictures")
	}
}
