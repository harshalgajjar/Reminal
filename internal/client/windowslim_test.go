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

// A frame that spends long on the relay's link, beyond the link's round
// trip, makes the next pictures lighter, a step at a time and no further
// than the last step; a run of quick ones brings them back up a step; one in
// between changes nothing.
func TestAFrameLongOnTheLinkMakesThePicturesLighter(t *testing.T) {
	level, run := 0, 0
	for i := 0; i < 10; i++ {
		level, run = nextSlim(level, run, 600*time.Millisecond)
	}
	last := len(relayQualities) - 1
	if level != last {
		t.Fatalf("after slow frames the step is %d, want the last (%d)", level, last)
	}
	level, run = nextSlim(level, run, 80*time.Millisecond)
	if level != last || run != 0 {
		t.Fatalf("a frame neither slow nor quick moved the step to %d (run %d)", level, run)
	}
	for i := 0; i < relayFastRun-1; i++ {
		level, run = nextSlim(level, run, 10*time.Millisecond)
	}
	if level != last {
		t.Fatalf("the step rose before %d quick frames in a row", relayFastRun)
	}
	level, run = nextSlim(level, run, 10*time.Millisecond)
	if level != last-1 || run != 0 {
		t.Fatalf("after %d quick frames the step is %d (run %d), want one up", relayFastRun, level, run)
	}
	if l, r := nextSlim(1, relayFastRun-1, 300*time.Millisecond); l != 2 || r != 0 {
		t.Fatalf("a slow frame in a quick run gave step %d, run %d", l, r)
	}
	if l, _ := nextSlim(0, relayFastRun, 0); l != 0 {
		t.Fatal("the first step went below itself")
	}
}

// What is counted is the frame's time on the link, not its ack's: the round
// trip is taken off, as the quickest small frame lately shows it, or a guess
// until one has. On a link with a long round trip and room to spare the
// pictures stay as they are; on an ordinary round trip a frame that took a
// quarter of a second is made lighter (the whole ack was counted, against
// 350 ms, and it never was).
func TestTheLinksRoundTripIsNotTheFramesTime(t *testing.T) {
	t0 := time.Unix(1795000000, 0)
	big := 200 << 10
	step := func(r *relaySlim, seq uint64, bytes int, ack time.Duration) {
		r.sent(seq, t0, bytes)
		r.acked(seq, t0.Add(ack))
	}
	// No small frame seen: a round trip of relayTripGuess is assumed.
	var mac relaySlim
	step(&mac, 1, big, 250*time.Millisecond)
	if mac.level != 1 {
		t.Fatalf("a 250 ms ack with no round trip known left the step at %d", mac.level)
	}
	// A long round trip, shown by a small frame: big frames only a little
	// slower than it are not made lighter.
	var far relaySlim
	step(&far, 1, 2<<10, 400*time.Millisecond)
	if far.base != 400*time.Millisecond || far.level != 0 {
		t.Fatalf("a small frame's ack: base %s, step %d", far.base, far.level)
	}
	for seq := uint64(2); seq < 8; seq++ {
		step(&far, seq, big, 460*time.Millisecond)
	}
	if far.level != 0 {
		t.Fatalf("on a 400 ms round trip, frames 60 ms slower went to step %d", far.level)
	}
	step(&far, 9, big, 700*time.Millisecond)
	if far.level != 1 {
		t.Fatalf("on a 400 ms round trip, a frame 300 ms slower left the step at %d", far.level)
	}
	// The quickest small frame stands; a slower one does not raise it, until
	// it is old.
	step(&far, 10, 2<<10, 900*time.Millisecond)
	if far.base != 400*time.Millisecond {
		t.Fatalf("a slower small frame moved the round trip to %s", far.base)
	}
	far.sent(11, t0.Add(2*relayBaseKept), 2<<10)
	far.acked(11, t0.Add(2*relayBaseKept+90*time.Millisecond))
	if far.base != 90*time.Millisecond {
		t.Fatalf("an old round trip was kept: %s", far.base)
	}
	// An ack for an earlier frame, or with nothing on its way, changes nothing.
	var r relaySlim
	r.sent(7, t0, big)
	r.acked(6, t0.Add(time.Second))
	if r.level != 0 || r.sentAt.IsZero() {
		t.Fatal("an earlier frame's ack was taken for this one's")
	}
	r.acked(7, t0.Add(600*time.Millisecond))
	r.acked(7, t0.Add(5*time.Second))
	if r.level != 1 {
		t.Fatalf("the step is %d after one slow frame and a stray ack", r.level)
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
