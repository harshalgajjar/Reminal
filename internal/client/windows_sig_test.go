// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
)

// jpegOf renders a gray canvas with an optional filled rectangle and returns
// its JPEG bytes at the same quality streamWindow uses, so the test exercises
// the real decode + box-average path.
func jpegOf(w, h int, rect image.Rectangle, c color.Gray) []byte {
	img := image.NewGray(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetGray(x, y, color.Gray{Y: 200})
		}
	}
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			img.SetGray(x, y, c)
		}
	}
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 45})
	return buf.Bytes()
}

// jpegColorOf is jpegOf's colour twin: a background fill with an optional filled
// rectangle in an arbitrary RGBA colour, so a test can change hue independently
// of brightness.
func jpegColorOf(w, h int, rect image.Rectangle, bg, fg color.Color) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, bg)
		}
	}
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			img.Set(x, y, fg)
		}
	}
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 45})
	return buf.Bytes()
}

func sigOf(t *testing.T, b []byte) frameSig {
	t.Helper()
	sig, ok := frameSignature(b)
	if !ok {
		t.Fatal("frameSignature failed to decode")
	}
	return sig
}

func TestFrameSignatureIdenticalFramesDoNotDiffer(t *testing.T) {
	empty := image.Rect(0, 0, 0, 0)
	a := sigOf(t, jpegOf(640, 480, empty, color.Gray{}))
	b := sigOf(t, jpegOf(640, 480, empty, color.Gray{}))
	if sigDiffers(a, b) {
		t.Fatal("two identical frames were reported as changed")
	}
}

func TestFrameSignatureRealChangeDiffers(t *testing.T) {
	base := sigOf(t, jpegOf(640, 480, image.Rect(0, 0, 0, 0), color.Gray{}))
	// A black 60x60 block — a clearly visible change (e.g. a menu opening).
	changed := sigOf(t, jpegOf(640, 480, image.Rect(300, 200, 360, 260), color.Gray{Y: 0}))
	if !sigDiffers(base, changed) {
		t.Fatal("a visible content change was reported as unchanged")
	}
}

func TestFrameSignatureColourChangeDiffers(t *testing.T) {
	// A block that changes HUE while keeping brightness almost constant: a
	// neutral gray (128,128,128, luma 128) → (170,120,90, luma ~131). The luma
	// shift (~3) is under the threshold, so a luma-only signature would miss
	// this entirely and freeze the pane — the chroma channels (Cb/Cr move ~23+)
	// are what catch it. This is the "status dot green→red" case.
	gray := color.RGBA{128, 128, 128, 255}
	base := sigOf(t, jpegColorOf(640, 480, image.Rect(0, 0, 0, 0), gray, gray))
	recolored := sigOf(t, jpegColorOf(640, 480, image.Rect(280, 190, 360, 270), gray, color.RGBA{170, 120, 90, 255}))
	if !sigDiffers(base, recolored) {
		t.Fatal("a colour-only change (constant luma) was reported as unchanged")
	}
}

func TestFrameSignatureTinyChangeStaysUnderThreshold(t *testing.T) {
	// A 2x2 speck barely dents one box-averaged cell — must not trigger a
	// resend, or an idle window with sub-pixel noise would stream forever.
	base := sigOf(t, jpegOf(640, 480, image.Rect(0, 0, 0, 0), color.Gray{}))
	speck := sigOf(t, jpegOf(640, 480, image.Rect(10, 10, 12, 12), color.Gray{Y: 190}))
	if sigDiffers(base, speck) {
		t.Fatal("a negligible speck crossed the change threshold")
	}
}
