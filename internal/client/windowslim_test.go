// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"math/rand"
	"testing"
	"time"
)

// A relay frame that takes long to be acked makes the next pictures lighter,
// a step at a time and no further than the last step; a run of quick acks
// brings them back up a step; an ack in between changes nothing.
func TestSlowRelayAcksMakeThePicturesLighter(t *testing.T) {
	level, run := 0, 0
	for i := 0; i < 10; i++ {
		level, run = nextSlim(level, run, 900*time.Millisecond)
	}
	if level != len(relayQualities)-1 {
		t.Fatalf("after slow acks the step is %d, want the last (%d)", level, len(relayQualities)-1)
	}
	level, run = nextSlim(level, run, 250*time.Millisecond)
	if level != len(relayQualities)-1 || run != 0 {
		t.Fatalf("an ack neither slow nor quick moved the step to %d (run %d)", level, run)
	}
	for i := 0; i < relayFastRun-1; i++ {
		level, run = nextSlim(level, run, 60*time.Millisecond)
	}
	if level != len(relayQualities)-1 {
		t.Fatalf("the step rose before %d quick acks in a row", relayFastRun)
	}
	level, run = nextSlim(level, run, 60*time.Millisecond)
	if level != len(relayQualities)-2 || run != 0 {
		t.Fatalf("after %d quick acks the step is %d (run %d), want one up", relayFastRun, level, run)
	}
	// One slow ack in a quick run starts the run again.
	level, run = nextSlim(1, relayFastRun-1, 500*time.Millisecond)
	if level != 2 || run != 0 {
		t.Fatalf("a slow ack in a quick run gave step %d, run %d", level, run)
	}
	if l, _ := nextSlim(0, relayFastRun, 10*time.Millisecond); l != 0 {
		t.Fatal("the first step went below itself")
	}

	// The ack's time is counted from the frame it is for, once.
	var r relaySlim
	t0 := time.Unix(1795000000, 0)
	r.sent(7, t0)
	r.acked(6, t0.Add(time.Second)) // an earlier frame's ack: not this one's
	if r.level != 0 || r.sentAt.IsZero() {
		t.Fatal("an earlier frame's ack was taken for this one's")
	}
	r.acked(7, t0.Add(600*time.Millisecond))
	if r.level != 1 {
		t.Fatalf("a 600 ms ack left the step at %d", r.level)
	}
	r.acked(7, t0.Add(5*time.Second)) // again: nothing is on its way
	if r.level != 1 {
		t.Fatal("an ack with nothing on its way moved the step")
	}
}

// A lighter picture is the same size in pixels and fewer bytes, a step
// lighter each time; what cannot be read, or would not shrink, is left as it
// is.
func TestALighterPictureKeepsItsSizeInPixels(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	src := image.NewRGBA(image.Rect(0, 0, 320, 200))
	for y := 0; y < 200; y++ {
		for x := 0; x < 320; x++ {
			v := uint8(rng.Intn(256))
			src.Set(x, y, color.RGBA{v, uint8(x), uint8(y), 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, src, &jpeg.Options{Quality: 55}); err != nil {
		t.Fatal(err)
	}
	orig := buf.Bytes()
	if got := slimJPEG(orig, 0); !bytes.Equal(got, orig) {
		t.Fatal("step 0 changed the picture")
	}
	prev := len(orig)
	for level := 1; level < len(relayQualities); level++ {
		got := slimJPEG(orig, level)
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(got))
		if err != nil || cfg.Width != 320 || cfg.Height != 200 {
			t.Fatalf("step %d: %v, %dx%d, want 320x200", level, err, cfg.Width, cfg.Height)
		}
		if len(got) >= prev {
			t.Fatalf("step %d is %d bytes, the step before %d", level, len(got), prev)
		}
		prev = len(got)
	}
	if prev*4 > len(orig)*3 {
		t.Errorf("the last step is %d of %d bytes: a quarter off at the least was expected", prev, len(orig))
	}
	if q := relayQualities[len(relayQualities)-1]; q < 22 {
		t.Errorf("the last step's quality is %d: below 22 small text is hard to read", q)
	}
	junk := []byte("not a picture")
	if got := slimJPEG(junk, 2); !bytes.Equal(got, junk) {
		t.Fatal("what does not decode was changed")
	}
	if got := slimJPEG(orig, 99); !bytes.Equal(got, orig) {
		t.Fatal("a step that does not exist changed the picture")
	}
}
