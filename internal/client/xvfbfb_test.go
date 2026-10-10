// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !windows

package client

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// writeXWD writes a w×h framebuffer file as Xvfb keeps one (XWD, 32-bit
// TrueColor, 256 colormap entries), each pixel px(x, y) as R, G, B.
func writeXWD(t *testing.T, path string, w, h int, msb bool, px func(x, y int) (r, g, b byte)) {
	t.Helper()
	name := []byte("Xvfb main window\x00")
	hdr := make([]uint32, 25)
	hdr[0] = uint32(100 + len(name))
	hdr[1], hdr[2], hdr[3], hdr[4], hdr[5] = 7, 2, 24, uint32(w), uint32(h)
	if msb {
		hdr[7] = 1
	}
	hdr[8], hdr[10], hdr[11], hdr[12], hdr[13] = 32, 32, 32, uint32(w*4), 4
	hdr[14], hdr[15], hdr[16], hdr[17], hdr[18], hdr[19] = 0xff0000, 0xff00, 0xff, 8, 256, 256
	var buf bytes.Buffer
	_ = binary.Write(&buf, binary.BigEndian, hdr)
	buf.Write(name)
	buf.Write(make([]byte, 256*12))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, b := px(x, y)
			if msb {
				buf.Write([]byte{0, r, g, b})
			} else {
				buf.Write([]byte{b, g, r, 0})
			}
		}
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAnXvfbIsFoundByItsDisplayAndFramebufferDir(t *testing.T) {
	proc := t.TempDir()
	cmd := func(pid string, args ...string) string {
		dir := filepath.Join(proc, pid)
		_ = os.MkdirAll(dir, 0o755)
		var b bytes.Buffer
		for _, a := range args {
			b.WriteString(a + "\x00")
		}
		_ = os.WriteFile(filepath.Join(dir, "cmdline"), b.Bytes(), 0o644)
		return dir
	}
	cmd("10", "/usr/bin/Xvfb", ":5", "-screen", "0", "800x600x24") // no -fbdir
	cmd("11", "/usr/bin/Xvfb", ":99", "-screen", "0", "1280x800x24", "-fbdir", "/run/xvfb", "-nolisten", "tcp")
	cmd("12", "/usr/bin/bash", ":98", "-fbdir", "/elsewhere")   // not an Xvfb
	cmd("self", "/usr/bin/Xvfb", ":99", "-fbdir", "/nope")      // not a pid
	cmd("13", "/usr/bin/Xvfb", ":97", "-fbdir", "relative/dir") // not an absolute dir
	path := func(disp string) string {
		if fb := findXvfbFB(proc, disp); fb != nil {
			return fb.path
		}
		return ""
	}
	for disp, want := range map[string]string{
		":99": "/run/xvfb/Xvfb_screen0", ":99.0": "/run/xvfb/Xvfb_screen0", ":99.1": "/run/xvfb/Xvfb_screen1",
		":5": "", ":7": "", "": "", "localhost:99": "", ":x": "", ":98": "", ":97": "",
	} {
		if got := path(disp); got != want {
			t.Errorf("DISPLAY=%q: %q, want %q", disp, got, want)
		}
	}
	if fb := findXvfbFB(proc, ":99"); fb == nil || fb.owner != uint32(os.Geteuid()) {
		t.Errorf("its owner: %+v", fb)
	}
	// In pid order, not as names sort ("5917" before "7").
	ord := t.TempDir()
	proc = ord
	cmd("5917", "/usr/bin/Xvfb", ":50", "-fbdir", "/late")
	cmd("7", "/usr/bin/Xvfb", ":50", "-fbdir", "/early")
	if got := path(":50"); got != "/early/Xvfb_screen0" {
		t.Errorf("pid order: %q", got)
	}
	// Another user's process that calls itself Xvfb is not trusted.
	if os.Geteuid() != 0 {
		t.Log("not root: the other-user spoof is not checked here")
		return
	}
	spoof := t.TempDir()
	proc = spoof
	dir := cmd("8", "/tmp/Xvfb", ":60", "-fbdir", "/tmp/evil")
	if err := os.Chown(dir, 65534, 65534); err != nil {
		t.Fatal(err)
	}
	if got := path(":60"); got != "" {
		t.Errorf("another user's fake Xvfb was trusted: %q", got)
	}
}

// mine is a framebuffer file of this process's user, as found.
func mine(path string) *xvfbFB { return &xvfbFB{path: path, owner: uint32(os.Geteuid())} }

func TestAFramebufferIsReadAsItsScreenShows(t *testing.T) {
	for _, msb := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "Xvfb_screen0")
		writeXWD(t, path, 64, 48, msb, func(x, y int) (byte, byte, byte) { return byte(x * 4), byte(y * 5), 0x80 })
		fb := mine(path)
		img, err := fb.read(image.Rect(10, 20, 30, 28))
		if err != nil {
			t.Fatal(err)
		}
		if img.Rect.Dx() != 20 || img.Rect.Dy() != 8 {
			t.Fatalf("read %v", img.Rect)
		}
		for _, p := range [][2]int{{0, 0}, {19, 7}, {5, 3}} {
			c := img.RGBAAt(p[0], p[1])
			if want := [3]byte{byte((10 + p[0]) * 4), byte((20 + p[1]) * 5), 0x80}; [3]byte{c.R, c.G, c.B} != want || c.A != 0xff {
				t.Errorf("msb=%v: pixel %v is %v, want %v", msb, p, c, want)
			}
		}
		// Partly off its edge: the window's own size, black where it is off
		// the screen — pixels stay where clicks are mapped.
		if img, err := fb.read(image.Rect(50, 40, 90, 90)); err != nil || img.Rect.Dx() != 40 || img.Rect.Dy() != 50 {
			t.Errorf("a window partly off the screen: %v %v", img, err)
		} else {
			if c := img.RGBAAt(0, 0); [3]byte{c.R, c.G, c.B} != [3]byte{200, 200, 0x80} {
				t.Errorf("its on-screen corner: %v", c)
			}
			if c := img.RGBAAt(39, 49); c.R != 0 || c.G != 0 || c.B != 0 || c.A != 0xff {
				t.Errorf("off the screen: %v, want opaque black", c)
			}
		}
		if _, err := fb.read(image.Rect(100, 100, 120, 120)); err == nil {
			t.Error("a window off the screen read")
		}
	}
}

// What is not a screen this reads is refused, at once and without reading
// what a header claims: no header; rows not w*4 bytes; a header saying more
// than its (sparse) file holds; a FIFO in its place (never blocks); a link;
// a file others may write.
func TestAFramebufferThatIsNotOneIsRefused(t *testing.T) {
	dir := t.TempDir()
	r := image.Rect(0, 0, 10, 10)
	bad := filepath.Join(dir, "noheader")
	_ = os.WriteFile(bad, []byte("not an xwd file at all, no header"), 0o644)
	if _, err := mine(bad).read(r); err == nil {
		t.Error("a file with no XWD header was read")
	}
	hdr := func(w, h, stride uint32, size int64) string {
		p := filepath.Join(dir, fmt.Sprintf("h%dx%d-%d", w, h, stride))
		v := make([]uint32, 25)
		v[0], v[1], v[2], v[3], v[4], v[5] = 100, 7, 2, 24, w, h
		v[11], v[12], v[13], v[14], v[15], v[16], v[19] = 32, stride, 4, 0xff0000, 0xff00, 0xff, 0
		var b bytes.Buffer
		_ = binary.Write(&b, binary.BigEndian, v)
		_ = os.WriteFile(p, b.Bytes(), 0o644)
		if size > 0 {
			_ = os.Truncate(p, size) // sparse
		}
		return p
	}
	if _, err := mine(hdr(64, 48, 1<<30, 100+(1<<30)*48)).read(r); err == nil || !strings.Contains(err.Error(), "bytes a row") {
		t.Errorf("a stride of 1<<30: %v", err)
	}
	if _, err := mine(hdr(8000, 8000, 32000, 1000)).read(r); err == nil || !strings.Contains(err.Error(), "not the") {
		t.Errorf("a header saying more than its file holds: %v", err)
	}
	if _, err := mine(hdr(9000, 100, 36000, 100+36000*100)).read(r); err == nil {
		t.Error("a screen past xvfbMaxSide was read")
	}
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := mine(fifo).read(r); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("a FIFO was read as a screen")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a FIFO in the framebuffer's place blocked the read")
	}
	good := filepath.Join(dir, "good")
	writeXWD(t, good, 16, 16, false, func(x, y int) (byte, byte, byte) { return 1, 2, 3 })
	link := filepath.Join(dir, "link")
	_ = os.Symlink(good, link)
	if _, err := mine(link).read(r); err == nil {
		t.Error("a link was followed")
	}
	_ = os.Chmod(good, 0o666)
	if _, err := mine(good).read(r); err == nil || !strings.Contains(err.Error(), "writable") {
		t.Errorf("a world-writable file: %v", err)
	}
	_ = os.Chmod(good, 0o644)
	if _, err := (&xvfbFB{path: good, owner: 4242}).read(r); err == nil || !strings.Contains(err.Error(), "owned by") {
		t.Errorf("a file not its X server's: %v", err)
	}
	if _, err := mine(good).read(r); err != nil {
		t.Errorf("the good file, after: %v", err)
	}
}

// A framebuffer that cannot be read is no lost frame: the capture falls back
// (fromXvfb says not ok), and the stream no longer takes frames as exact.
func TestAnUnreadableFramebufferFallsBack(t *testing.T) {
	s := &xvfbFBState
	s.mu.Lock()
	oldDisp, oldAt, oldFB := s.display, s.at, s.fb
	s.display, s.at, s.fb = os.Getenv("DISPLAY"), time.Now(), mine(filepath.Join(t.TempDir(), "gone"))
	s.mu.Unlock()
	t.Cleanup(func() { s.mu.Lock(); s.display, s.at, s.fb = oldDisp, oldAt, oldFB; s.mu.Unlock() })
	xvfbExact.Store(true)
	if _, ok := fromXvfb(func(fb *xvfbFB) ([]byte, error) { return xvfbJPEG(fb, "w", image.Rect(0, 0, 10, 10), 1100, 55) }); ok {
		t.Fatal("a framebuffer file that is not there gave a frame")
	}
	if xvfbExact.Load() {
		t.Error("frames still taken as exact after falling back")
	}
}

func TestAFramebufferFrameIsEncodedOnlyWhenItChanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Xvfb_screen0")
	shade := byte(10)
	writeXWD(t, path, 1280, 800, false, func(x, y int) (byte, byte, byte) { return shade, byte(x), byte(y) })
	fb := mine(path)
	r := image.Rect(0, 0, 1280, 800)
	a, err := xvfbJPEG(fb, "0x1", r, 1100, 55)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(a))
	if err != nil || cfg.Width != 1100 || cfg.Height != 687 {
		t.Fatalf("a 1280×800 window as %dx%d (%v): want it fit to 1100", cfg.Width, cfg.Height, err)
	}
	b, _ := xvfbJPEG(fb, "0x1", r, 1100, 55)
	if &a[0] != &b[0] {
		t.Error("unchanged pixels were encoded again")
	}
	shade = 200
	writeXWD(t, path, 1280, 800, false, func(x, y int) (byte, byte, byte) { return shade, byte(x), byte(y) })
	c, _ := xvfbJPEG(fb, "0x1", r, 1100, 55)
	if bytes.Equal(a, c) {
		t.Error("changed pixels gave the old frame")
	}
	// The exact size the H.264 path asks for.
	raw, err := xvfbRGBA(fb, "0x1", r, 640, 400)
	if err != nil || len(raw) != 640*400*4 {
		t.Fatalf("RGBA %d bytes (%v), want %d", len(raw), err, 640*400*4)
	}
}

func TestShrinkingAveragesWhatEachPixelCovers(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 4, 2))
	for i := 0; i < len(src.Pix); i += 4 {
		v := byte(0)
		if (i/4)%2 == 1 {
			v = 200
		}
		src.Pix[i], src.Pix[i+1], src.Pix[i+2], src.Pix[i+3] = v, v, v, 0xff
	}
	d := shrinkTo(src, 2, 1)
	if c := d.RGBAAt(0, 0); c.R != 100 || c.A != 0xff {
		t.Errorf("a 2×2 of black and grey averaged to %v", c)
	}
	if w, h := fitBox(1280, 800, 1100); w != 1100 || h != 687 {
		t.Errorf("fit %dx%d", w, h)
	}
	if w, h := fitBox(500, 300, 1100); w != 500 || h != 300 {
		t.Errorf("grown: %dx%d", w, h)
	}
	if w, h := fitBox(300, 2000, 1100); w != 165 || h != 1100 {
		t.Errorf("tall: %dx%d", w, h)
	}
}

// The stream reads a frame whose bytes are the ones it last sent as no
// change — without decoding it — and anything else as before.
func TestTheSameBytesAsLastSentAreNoChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Xvfb_screen0")
	writeXWD(t, path, 64, 48, false, func(x, y int) (byte, byte, byte) { return byte(x), byte(y), 1 })
	fb := mine(path)
	frame, _ := xvfbJPEG(fb, "w", image.Rect(0, 0, 64, 48), 1100, 55)
	s := &winStream{}
	if !s.detectChange(frame) {
		t.Fatal("the first frame is no change")
	}
	// Not sent yet: the same bytes are still a change.
	if !s.detectChange(frame) {
		t.Fatal("a frame never sent read as no change")
	}
	s.lastSig, s.haveSig, s.sentImg = s.pendingSig, s.pendingSigOK, s.pendingImg // as dispatch commits it
	if s.detectChange(frame) {
		t.Error("the frame last sent, again, read as a change")
	}
	if s.pendingSig != s.lastSig {
		t.Error("an unchanged frame left a different pending signature")
	}
}

// exactBackend is a backend whose frames change exactly when their bytes do.
type exactBackend struct{ windowBackend }

func (exactBackend) exactFrames() bool { return true }

// From such a backend, a frame is a change when its bytes are not the ones
// last sent — no signature: these bytes are not even a JPEG.
func TestExactFramesAreChangedByTheirBytesAlone(t *testing.T) {
	s := &winStream{b: exactBackend{}}
	a, b := []byte("frame one, not a jpeg"), []byte("frame two, not a jpeg")
	if !s.detectChange(a) {
		t.Fatal("the first frame is no change")
	}
	s.lastSig, s.haveSig, s.sentImg = s.pendingSig, s.pendingSigOK, s.pendingImg
	if s.detectChange(a) {
		t.Error("the bytes last sent read as a change")
	}
	if !s.detectChange(b) {
		t.Error("new bytes read as no change")
	}
}
