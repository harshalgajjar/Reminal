// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/maphash"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// An Xvfb started with -fbdir keeps its screen in a file (XWD: a header, a
// colormap, then the pixels, row by row) that it draws into as it goes. On
// such a display a frame is read from that file — no process spawned, no grab
// of the X server, no round trip through ImageMagick — and encoded here; a
// window whose pixels did not change since its last frame costs a read and a
// hash. Any other display (a desktop's own X server, Wayland) captures as
// before (import, a compositor's screenshot).

// xvfbFB is one Xvfb screen's framebuffer file, as its header describes it.
type xvfbFB struct {
	path     string
	off      int64 // where its pixels start
	w, h     int
	stride   int  // bytes a row
	msbFirst bool // pixels XRGB in memory, not BGRX
}

var xvfbFBState struct {
	mu      sync.Mutex
	display string
	at      time.Time
	fb      *xvfbFB
}

// xvfbFBRecheck is how long an answer to "is this display an Xvfb with a
// framebuffer file?" stands: an X server restarted with or without one is
// seen within it.
const xvfbFBRecheck = 10 * time.Second

// currentXvfbFB is this process's DISPLAY's framebuffer file, or nil.
func currentXvfbFB() *xvfbFB {
	disp := os.Getenv("DISPLAY")
	s := &xvfbFBState
	s.mu.Lock()
	defer s.mu.Unlock()
	if disp == s.display && time.Since(s.at) < xvfbFBRecheck {
		return s.fb
	}
	s.display, s.at, s.fb = disp, time.Now(), nil
	if path := xvfbFBPath("/proc", disp); path != "" {
		if fb, err := openXvfbFB(path); err == nil {
			s.fb = fb
		}
	}
	return s.fb
}

// xvfbFBPath finds, under proc, an Xvfb serving display (":99", ":99.0",
// "localhost:99" is not one: a local Xvfb only) with a framebuffer directory
// (-fbdir DIR), and returns its screen's file: DIR/Xvfb_screenN.
func xvfbFBPath(proc, display string) string {
	if !strings.HasPrefix(display, ":") {
		return ""
	}
	num, screen, _ := strings.Cut(display[1:], ".")
	if _, err := strconv.Atoi(num); err != nil {
		return ""
	}
	if screen == "" {
		screen = "0"
	}
	if _, err := strconv.Atoi(screen); err != nil {
		return ""
	}
	ents, err := os.ReadDir(proc)
	if err != nil {
		return ""
	}
	for _, e := range ents {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(proc, e.Name(), "cmdline"))
		if err != nil || len(raw) == 0 {
			continue
		}
		args := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
		if filepath.Base(args[0]) != "Xvfb" {
			continue
		}
		dir, mine := "", false
		for i, a := range args[1:] {
			switch {
			case a == ":"+num:
				mine = true
			case a == "-fbdir" && i+2 < len(args):
				dir = args[i+2]
			}
		}
		if mine && dir != "" {
			return filepath.Join(dir, "Xvfb_screen"+screen)
		}
	}
	return ""
}

// openXvfbFB reads a framebuffer file's header: 32-bit TrueColor pixels in a
// ZPixmap only (what Xvfb keeps at depth 24); anything else is not read.
func openXvfbFB(path string) (*xvfbFB, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var h [25]uint32 // XWDFileHeader, big-endian
	if err := binary.Read(f, binary.BigEndian, &h); err != nil {
		return nil, err
	}
	headerSize, version, format, width, height := h[0], h[1], h[2], h[4], h[5]
	byteOrder, bpp, stride, visual := h[7], h[11], h[12], h[13]
	red, green, blue, ncolors := h[14], h[15], h[16], h[19]
	switch {
	case version != 7 || format != 2:
		return nil, fmt.Errorf("%s: not an XWD ZPixmap (version %d, format %d)", path, version, format)
	case bpp != 32 || visual != 4 || red != 0xff0000 || green != 0xff00 || blue != 0xff:
		return nil, fmt.Errorf("%s: not 32-bit TrueColor (%d bpp, visual %d)", path, bpp, visual)
	case width == 0 || height == 0 || width > 1<<15 || height > 1<<15 || stride < width*4:
		return nil, fmt.Errorf("%s: a %dx%d screen of %d bytes a row", path, width, height, stride)
	}
	fb := &xvfbFB{path: path, off: int64(headerSize) + int64(ncolors)*12, w: int(width), h: int(height), stride: int(stride), msbFirst: byteOrder == 1}
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if fi.Size() < fb.off+int64(fb.stride)*int64(fb.h) {
		return nil, fmt.Errorf("%s: %d bytes, shorter than its screen", path, fi.Size())
	}
	return fb, nil
}

// read is r of the screen (clipped to it) as RGBA.
func (fb *xvfbFB) read(r image.Rectangle) (*image.RGBA, error) {
	r = r.Intersect(image.Rect(0, 0, fb.w, fb.h))
	if r.Empty() {
		return nil, errors.New("the window is not on the screen")
	}
	f, err := os.Open(fb.path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	// Its rows in one read: from its first pixel to its last, the screen's
	// own stride between them.
	n := (r.Dy()-1)*fb.stride + r.Dx()*4
	bp := xvfbReadBuf.Get().(*[]byte)
	defer xvfbReadBuf.Put(bp)
	if cap(*bp) < n {
		*bp = make([]byte, n)
	}
	block := (*bp)[:n]
	if _, err := f.ReadAt(block, fb.off+int64(r.Min.Y)*int64(fb.stride)+int64(r.Min.X)*4); err != nil {
		return nil, err
	}
	img := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	for y := 0; y < r.Dy(); y++ {
		row := block[y*fb.stride : y*fb.stride+r.Dx()*4]
		dst := img.Pix[y*img.Stride : y*img.Stride+len(row)]
		for i := 0; i < len(row); i += 4 {
			if fb.msbFirst { // X R G B
				dst[i], dst[i+1], dst[i+2] = row[i+1], row[i+2], row[i+3]
			} else { // B G R X
				dst[i], dst[i+1], dst[i+2] = row[i+2], row[i+1], row[i]
			}
			dst[i+3] = 0xff
		}
	}
	return img, nil
}

// xvfbReadBuf holds a frame's raw rows between reads (a screen is MBs: not
// made anew every frame).
var xvfbReadBuf = sync.Pool{New: func() any { b := []byte(nil); return &b }}

// shrinkTo is src scaled to exactly w×h by averaging the pixels each one
// covers (a box filter: text stays legible where nearest-neighbour would
// drop strokes). Not scaled when it is that size already.
func shrinkTo(src *image.RGBA, w, h int) *image.RGBA {
	sw, sh := src.Rect.Dx(), src.Rect.Dy()
	if sw == w && sh == h {
		return src
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		y0, y1 := y*sh/h, (y+1)*sh/h
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for x := 0; x < w; x++ {
			x0, x1 := x*sw/w, (x+1)*sw/w
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var r, g, b, n uint32
			for sy := y0; sy < y1 && sy < sh; sy++ {
				p := src.Pix[sy*src.Stride+x0*4:]
				for sx := x0; sx < x1 && sx < sw; sx++ {
					r += uint32(p[0])
					g += uint32(p[1])
					b += uint32(p[2])
					p = p[4:]
					n++
				}
			}
			d := dst.Pix[y*dst.Stride+x*4:]
			d[0], d[1], d[2], d[3] = uint8(r/n), uint8(g/n), uint8(b/n), 0xff
		}
	}
	return dst
}

// fitBox is w×h shrunk (never grown) to fit a box×box square, aspect kept.
func fitBox(w, h, box int) (int, int) {
	if w <= box && h <= box {
		return w, h
	}
	if w >= h {
		return box, max(1, h*box/w)
	}
	return max(1, w*box/h), box
}

// xvfbLast is the last frame of each thing captured (a window, a region),
// by what was asked: unchanged pixels give back the same bytes, unencoded.
var xvfbLast struct {
	mu   sync.Mutex
	seed maphash.Seed
	m    map[string]xvfbFrame
}

type xvfbFrame struct {
	sum uint64
	out []byte
}

func init() { xvfbLast.seed = maphash.MakeSeed() }

// xvfbFrameOf is r of the screen, encoded by enc — or, when its pixels are
// what they were at the last frame asked for as key, those bytes again.
func xvfbFrameOf(fb *xvfbFB, key string, r image.Rectangle, enc func(*image.RGBA) ([]byte, error)) ([]byte, error) {
	img, err := fb.read(r)
	if err != nil {
		return nil, err
	}
	sum := maphash.Bytes(xvfbLast.seed, img.Pix)
	xvfbLast.mu.Lock()
	last, ok := xvfbLast.m[key]
	xvfbLast.mu.Unlock()
	if ok && last.sum == sum {
		return last.out, nil
	}
	out, err := enc(img)
	if err != nil {
		return nil, err
	}
	xvfbLast.mu.Lock()
	if xvfbLast.m == nil || len(xvfbLast.m) > 64 {
		xvfbLast.m = map[string]xvfbFrame{} // a handful are captured at once; never grows without end
	}
	xvfbLast.m[key] = xvfbFrame{sum: sum, out: out}
	xvfbLast.mu.Unlock()
	return out, nil
}

// xvfbJPEG is r of the screen as a JPEG fitting a maxW×maxW box, at quality
// (as import's -resize "NxN>" -quality makes it).
func xvfbJPEG(fb *xvfbFB, key string, r image.Rectangle, maxW, quality int) ([]byte, error) {
	return xvfbFrameOf(fb, fmt.Sprintf("jpeg %s %v %d %d", key, r, maxW, quality), r, func(img *image.RGBA) ([]byte, error) {
		w, h := fitBox(img.Rect.Dx(), img.Rect.Dy(), maxW)
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, shrinkTo(img, w, h), &jpeg.Options{Quality: quality}); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	})
}

// xvfbRGBA is r of the screen as tightly packed RGBA at exactly tw×th.
func xvfbRGBA(fb *xvfbFB, key string, r image.Rectangle, tw, th int) ([]byte, error) {
	return xvfbFrameOf(fb, fmt.Sprintf("rgba %s %v %dx%d", key, r, tw, th), r, func(img *image.RGBA) ([]byte, error) {
		return shrinkTo(img, tw, th).Pix, nil
	})
}
