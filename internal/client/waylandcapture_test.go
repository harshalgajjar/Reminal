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

// TestIsWaylandSession pins the detection that decides X11-vs-Wayland capture.
// The bug this fixes: the old check only saw Wayland when DISPLAY was empty, so
// a Wayland session with Xwayland (DISPLAY set — the common GNOME/Cinnamon case)
// slipped through to the X11 path and captured a black root.
func TestIsWaylandSession(t *testing.T) {
	cases := []struct {
		name, sessionType, waylandDisplay, display string
		want                                       bool
	}{
		{"x11 only", "x11", "", ":0", false},
		{"headless", "", "", "", false},
		{"wayland via session type (Xwayland also set)", "wayland", "wayland-0", ":0", true},
		{"wayland via WAYLAND_DISPLAY only", "", "wayland-0", "", true},
		{"session type wins even with DISPLAY set", "wayland", "", ":0", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("XDG_SESSION_TYPE", c.sessionType)
			t.Setenv("WAYLAND_DISPLAY", c.waylandDisplay)
			t.Setenv("DISPLAY", c.display)
			if got := isWaylandSession(); got != c.want {
				t.Errorf("isWaylandSession()=%v want %v", got, c.want)
			}
		})
	}
}

// TestDownscaleJPEG covers the shared decode→shrink→JPEG step the Wayland path
// uses in place of ImageMagick: it must only shrink (never enlarge), preserve
// aspect, produce a decodable JPEG, and keep the image's colour (not go black).
func TestDownscaleJPEG(t *testing.T) {
	// 800x400 solid mid-blue, offset origin to catch bounds-handling bugs.
	src := image.NewRGBA(image.Rect(10, 20, 810, 420))
	blue := color.RGBA{0x22, 0x88, 0xff, 0xff}
	for y := src.Rect.Min.Y; y < src.Rect.Max.Y; y++ {
		for x := src.Rect.Min.X; x < src.Rect.Max.X; x++ {
			src.SetRGBA(x, y, blue)
		}
	}

	// Downscale to fit 200 wide → expect 200x100.
	jpg, err := downscaleJPEG(src, 200, 55)
	if err != nil {
		t.Fatalf("downscaleJPEG: %v", err)
	}
	img, err := jpeg.Decode(bytes.NewReader(jpg))
	if err != nil {
		t.Fatalf("output not valid JPEG: %v", err)
	}
	if got := img.Bounds().Dx(); got != 200 {
		t.Errorf("width=%d want 200", got)
	}
	if got := img.Bounds().Dy(); got != 100 {
		t.Errorf("height=%d want 100 (aspect preserved)", got)
	}
	// Centre pixel should be ~blue, not black.
	r, g, b, _ := img.At(100, 50).RGBA()
	if r>>8 > 0x60 || g>>8 < 0x50 || b>>8 < 0xB0 {
		t.Errorf("centre colour drifted: r=%d g=%d b=%d (want ~ 0x22,0x88,0xff)", r>>8, g>>8, b>>8)
	}

	// Smaller-than-max must NOT be enlarged.
	jpg2, err := downscaleJPEG(src, 4000, 55)
	if err != nil {
		t.Fatalf("downscaleJPEG (no shrink): %v", err)
	}
	img2, _ := jpeg.Decode(bytes.NewReader(jpg2))
	if img2.Bounds().Dx() != 800 {
		t.Errorf("width=%d want 800 (should not enlarge)", img2.Bounds().Dx())
	}
}
