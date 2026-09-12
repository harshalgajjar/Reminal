// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"bytes"
	"encoding/binary"
	"image"
	"io"
	"sync"
	"testing"
	"time"
)

// nal builds one NAL unit: a header byte carrying the type in its low 5 bits,
// then arbitrary payload.
func nal(typ byte, payload ...byte) []byte {
	return append([]byte{typ & 0x1f}, payload...)
}

// annexB concatenates NALs into an Annex-B elementary stream (4-byte start
// codes), the shape ffmpeg writes on stdout.
func annexB(nals ...[]byte) []byte {
	var b []byte
	for _, n := range nals {
		b = append(b, 0, 0, 0, 1)
		b = append(b, n...)
	}
	return b
}

// framedAU is one message parsed back out of splitAnnexBToFramed's output.
type framedAU struct {
	key bool
	au  []byte
}

func parseFramed(t *testing.T, b []byte) []framedAU {
	t.Helper()
	var out []framedAU
	for len(b) > 0 {
		if len(b) < 5 {
			t.Fatalf("trailing %d bytes, not a full frame header", len(b))
		}
		n := binary.BigEndian.Uint32(b[:4])
		if int(n)+4 > len(b) {
			t.Fatalf("frame length %d overruns %d remaining", n, len(b)-4)
		}
		flag := b[4]
		au := b[5 : 4+int(n)]
		out = append(out, framedAU{key: flag == flagH264Key, au: append([]byte(nil), au...)})
		b = b[4+int(n):]
	}
	return out
}

// The splitter groups parameter sets and SEI with the picture that follows and
// flushes one message per access unit, marking an IDR or SPS as a keyframe. This
// is the contract the viewer's decoder and the winHelper queue depend on.
func TestSplitAnnexBGroupsAccessUnits(t *testing.T) {
	sps := nal(7, 0x42, 0x00, 0x28)
	pps := nal(8, 0xCE)
	idr := nal(5, 0xAA, 0xBB)
	p1 := nal(1, 0x11)
	p2 := nal(1, 0x22)

	stream := annexB(sps, pps, idr, p1, p2, sps, pps, idr)
	var out bytes.Buffer
	if err := splitAnnexBToFramed(bytes.NewReader(stream), &out); err != nil {
		t.Fatalf("split: %v", err)
	}
	aus := parseFramed(t, out.Bytes())

	if len(aus) != 4 {
		t.Fatalf("got %d access units, want 4 (keyframe, P, P, keyframe)", len(aus))
	}
	if !aus[0].key || aus[1].key || aus[2].key || !aus[3].key {
		t.Fatalf("keyframe flags = %v %v %v %v, want true false false true",
			aus[0].key, aus[1].key, aus[2].key, aus[3].key)
	}
	// The first AU must carry SPS+PPS+IDR so a joining decoder has an entry
	// point, each NAL prefixed with a start code.
	if !bytes.Equal(aus[0].au, annexB(sps, pps, idr)) {
		t.Fatalf("keyframe AU = % x, want SPS+PPS+IDR with start codes", aus[0].au)
	}
	if !bytes.Equal(aus[1].au, annexB(p1)) {
		t.Fatalf("delta AU = % x, want a single P slice", aus[1].au)
	}
}

// ffmpeg output arrives in arbitrary chunks, so the splitter must reassemble
// NALs across read boundaries and never lose or merge one.
func TestSplitAnnexBReassemblesAcrossChunks(t *testing.T) {
	idr := nal(5, 0x01, 0x02, 0x03, 0x04, 0x05)
	p1 := nal(1, 0x06, 0x07)
	stream := annexB(nal(7, 0x42, 0x00, 0x28), nal(8, 0xCE), idr, p1)

	// A reader that yields one byte at a time is the worst case for a start-code
	// scanner that assumes whole start codes land in one read.
	var out bytes.Buffer
	if err := splitAnnexBToFramed(&iotest1{stream}, &out); err != nil {
		t.Fatalf("split: %v", err)
	}
	aus := parseFramed(t, out.Bytes())
	if len(aus) != 2 {
		t.Fatalf("got %d access units across byte-at-a-time reads, want 2", len(aus))
	}
	if !aus[0].key || aus[1].key {
		t.Fatalf("keyframe flags = %v %v, want true false", aus[0].key, aus[1].key)
	}
}

// iotest1 is an io.Reader that returns at most one byte per Read.
type iotest1 struct{ b []byte }

func (r *iotest1) Read(p []byte) (int, error) {
	if len(r.b) == 0 {
		return 0, io.EOF
	}
	p[0] = r.b[0]
	r.b = r.b[1:]
	return 1, nil
}

func TestFindStartCode(t *testing.T) {
	// 4-byte start code preceded by content.
	b := []byte{0xFF, 0x00, 0x00, 0x00, 0x01, 0x67}
	if idx, l := findStartCode(b, 1); idx != 1 || l != 4 {
		t.Fatalf("4-byte start code at %d len %d, want 1/4", idx, l)
	}
	// 3-byte start code at the head.
	b = []byte{0x00, 0x00, 0x01, 0x67}
	if idx, l := findStartCode(b, 0); idx != 0 || l != 3 {
		t.Fatalf("3-byte start code at %d len %d, want 0/3", idx, l)
	}
	if idx, _ := findStartCode([]byte{1, 2, 3, 4}, 0); idx != -1 {
		t.Fatal("found a start code where there is none")
	}
}

// The encoder rejects odd dimensions (4:2:0) and the mirror should not upscale;
// the target must fit inside maxWidth, keep aspect, and be even.
func TestFFmpegTargetDims(t *testing.T) {
	tw, th := ffmpegTargetDims(winInfo{W: 1600, H: 900}, 1100)
	if tw > 1100 || tw%2 != 0 || th%2 != 0 {
		t.Fatalf("dims %dx%d: want <=1100 wide and both even", tw, th)
	}
	// Aspect preserved within rounding.
	if r := float64(tw) / float64(th); r < 1.7 || r > 1.8 {
		t.Fatalf("aspect %.3f drifted from 16:9", r)
	}
	// A window already small enough is not upscaled.
	tw, th = ffmpegTargetDims(winInfo{W: 640, H: 480}, 1100)
	if tw != 640 || th != 480 {
		t.Fatalf("dims %dx%d, want the source size unchanged", tw, th)
	}
}

// fakeRawBackend is a windowBackend that only knows how to hand back raw frames
// — enough to drive the ffmpeg helper without a real window. Each frame varies
// so the encoder produces genuine deltas, not an all-skip stream.
type fakeRawBackend struct {
	windowBackend
	mu    sync.Mutex
	count int
}

func (f *fakeRawBackend) captureRaw(w winInfo, tw, th int) ([]byte, error) {
	f.mu.Lock()
	f.count++
	n := f.count
	f.mu.Unlock()
	buf := make([]byte, tw*th*4)
	for i := 0; i < len(buf); i += 4 {
		buf[i] = byte((i/4 + n*3) % 256) // a band that moves each frame
		buf[i+1] = byte(n * 7 % 256)
		buf[i+2] = 0x40
		buf[i+3] = 0xFF
	}
	return buf, nil
}

// The whole ffmpeg pipeline, for real, where ffmpeg is installed (the Linux
// VM): capture → rawvideo stdin → encoder → Annex-B stdout → AU framing →
// winHelper queue. Proves ffmpeg is found and drivable, the encoder produces a
// stream that starts with a keyframe, and every access unit the consumer sees is
// a well-formed Annex-B unit tagged H.264 — without a GUI window or TCC.
func TestFFmpegHelperEncodesEndToEnd(t *testing.T) {
	if !ffmpegAvailable() {
		t.Skip("ffmpeg not installed — skipping the live encode test")
	}
	b := &fakeRawBackend{}
	h, err := startFFmpegHelper(b, winInfo{ID: "x", W: 320, H: 240}, 320, 50, 15)
	if err != nil {
		t.Fatalf("start ffmpeg helper: %v", err)
	}
	defer h.stop()

	gotKey := false
	frames := 0
	deadline := time.Now().Add(10 * time.Second)
	for frames < 5 && time.Now().Before(deadline) {
		f, ok := h.next(nil, 2*time.Second)
		if !ok || len(f.Data) == 0 {
			continue
		}
		if !f.H264 {
			t.Fatal("frame not tagged H.264")
		}
		d := f.Data
		startCode := len(d) >= 4 && d[0] == 0 && d[1] == 0 &&
			(d[2] == 1 || (d[2] == 0 && d[3] == 1))
		if !startCode {
			n := len(d)
			if n > 8 {
				n = 8
			}
			t.Fatalf("access unit %d does not start with an Annex-B start code: % x", frames, d[:n])
		}
		if f.Key {
			gotKey = true
		}
		frames++
	}
	if frames < 5 {
		t.Fatalf("only %d access units within the deadline, want >= 5 (encoder or capture stalled): %s",
			frames, h.errorText())
	}
	if !gotKey {
		t.Fatal("no keyframe among the first access units — a joining decoder would never sync")
	}
}

// scaleExactRGBA (the Wayland raw-capture path's resampler) must always produce
// exactly dw*dh*4 bytes — ffmpeg's rawvideo input rejects any other size — and
// preserve a solid color, with an opaque alpha.
func TestScaleExactRGBA(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 100, 60))
	for i := 0; i < len(src.Pix); i += 4 {
		src.Pix[i], src.Pix[i+1], src.Pix[i+2], src.Pix[i+3] = 0x20, 0xC0, 0x40, 0xFF
	}
	for _, d := range []struct{ w, h int }{{32, 24}, {160, 90}, {17, 13}} {
		out := scaleExactRGBA(src, d.w, d.h)
		if len(out) != d.w*d.h*4 {
			t.Fatalf("scaleExactRGBA to %dx%d = %d bytes, want %d", d.w, d.h, len(out), d.w*d.h*4)
		}
		// Middle pixel keeps the source color and an opaque alpha.
		o := ((d.h/2)*d.w + d.w/2) * 4
		if out[o] != 0x20 || out[o+1] != 0xC0 || out[o+2] != 0x40 || out[o+3] != 0xFF {
			t.Fatalf("center pixel = %x %x %x %x, want 20 C0 40 FF", out[o], out[o+1], out[o+2], out[o+3])
		}
	}
}

func TestFFmpegBitrateClamps(t *testing.T) {
	if br := ffmpegBitrate(64, 64, 30); br != 600_000 {
		t.Fatalf("tiny window bitrate %d, want the 600k floor", br)
	}
	if br := ffmpegBitrate(3840, 2160, 60); br != 12_000_000 {
		t.Fatalf("huge window bitrate %d, want the 12M ceiling", br)
	}
	// A 1100x700 @30 window lands near the macOS helper's measured ~1.7 Mbps.
	if br := ffmpegBitrate(1100, 700, 30); br < 1_400_000 || br > 2_000_000 {
		t.Fatalf("1100x700@30 bitrate %d, want ~1.7 Mbps", br)
	}
}
