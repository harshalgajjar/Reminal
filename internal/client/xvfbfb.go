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
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// An Xvfb started with -fbdir keeps its screen in a file (XWD: a header, a
// colormap, then the pixels, row by row) that it draws into as it goes. On
// such a display a frame is read from that file — no process spawned, no grab
// of the X server, no round trip through ImageMagick — and encoded here; a
// window whose pixels did not change since its last frame costs a read and a
// hash. Any other display (a desktop's own X server, Wayland) captures as
// before (import, a compositor's screenshot).

// xvfbFB is an Xvfb's framebuffer file, and the uid it must be owned by (its
// X server's). What it holds is read from its header at every frame.
type xvfbFB struct {
	path  string
	owner uint32
}

// xvfbScreen is a framebuffer file's screen, as its header says.
type xvfbScreen struct {
	off      int64 // where its pixels start
	w, h     int
	stride   int  // bytes a row: w*4, nothing else is read
	msbFirst bool // pixels XRGB in memory, not BGRX
}

// xvfbMaxSide bounds a screen's width and height: a header that says more is
// not a screen this reads (its rows would be read into memory).
const xvfbMaxSide = 8192

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

// currentXvfbFB is this process's DISPLAY's framebuffer file, or nil. Its
// lock is never held over the file system: a /proc walk that is slow holds
// nobody else up.
func currentXvfbFB() *xvfbFB {
	disp := os.Getenv("DISPLAY")
	s := &xvfbFBState
	s.mu.Lock()
	if disp == s.display && !s.at.IsZero() && time.Since(s.at) < xvfbFBRecheck {
		fb := s.fb
		s.mu.Unlock()
		return fb
	}
	s.mu.Unlock()
	fb := findXvfbFB("/proc", disp)
	s.mu.Lock()
	s.display, s.at, s.fb = disp, time.Now(), fb
	s.mu.Unlock()
	return fb
}

// findXvfbFB finds, under proc, an Xvfb serving display (":99", ":99.0";
// "localhost:99" is not one: a local Xvfb only) with a framebuffer directory
// (-fbdir DIR) — run by root or by this process's own user, never another's
// (trustedUID) — and returns its screen's file, DIR/Xvfb_screenN. Processes
// are looked at in pid order.
func findXvfbFB(proc, display string) *xvfbFB {
	if !strings.HasPrefix(display, ":") {
		return nil
	}
	num, screen, _ := strings.Cut(display[1:], ".")
	if _, err := strconv.Atoi(num); err != nil {
		return nil
	}
	if screen == "" {
		screen = "0"
	}
	if _, err := strconv.Atoi(screen); err != nil {
		return nil
	}
	ents, err := os.ReadDir(proc)
	if err != nil {
		return nil
	}
	var pids []int
	for _, e := range ents {
		if pid, err := strconv.Atoi(e.Name()); err == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	sort.Ints(pids)
	for _, pid := range pids {
		dirPath := filepath.Join(proc, strconv.Itoa(pid))
		fi, err := os.Stat(dirPath)
		if err != nil {
			continue
		}
		uid, ok := ownerOf(fi)
		if !ok || !trustedUID(uid) {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dirPath, "cmdline"))
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
		if mine && dir != "" && filepath.IsAbs(dir) {
			return &xvfbFB{path: filepath.Join(dir, "Xvfb_screen"+screen), owner: uid}
		}
	}
	return nil
}

// screenOf reads and checks a framebuffer file's header: 32-bit TrueColor
// pixels in a ZPixmap, rows of exactly w*4 bytes, a screen no larger than
// xvfbMaxSide a side, in a file of the size that makes — anything else is not
// read (a header can say anything; its rows are read into memory).
func screenOf(f *os.File, size int64) (xvfbScreen, error) {
	var h [25]uint32 // XWDFileHeader, big-endian
	if err := binary.Read(io.NewSectionReader(f, 0, 100), binary.BigEndian, &h); err != nil {
		return xvfbScreen{}, err
	}
	headerSize, version, format, width, height := h[0], h[1], h[2], h[4], h[5]
	byteOrder, bpp, stride, visual := h[7], h[11], h[12], h[13]
	red, green, blue, ncolors := h[14], h[15], h[16], h[19]
	switch {
	case version != 7 || format != 2:
		return xvfbScreen{}, fmt.Errorf("not an XWD ZPixmap (version %d, format %d)", version, format)
	case bpp != 32 || visual != 4 || red != 0xff0000 || green != 0xff00 || blue != 0xff:
		return xvfbScreen{}, fmt.Errorf("not 32-bit TrueColor (%d bpp, visual %d)", bpp, visual)
	case width == 0 || height == 0 || width > xvfbMaxSide || height > xvfbMaxSide || stride != width*4:
		return xvfbScreen{}, fmt.Errorf("a %dx%d screen of %d bytes a row", width, height, stride)
	case headerSize < 100 || headerSize > 4096 || ncolors > 65536:
		return xvfbScreen{}, fmt.Errorf("a header of %d bytes and %d colors", headerSize, ncolors)
	}
	sc := xvfbScreen{off: int64(headerSize) + int64(ncolors)*12, w: int(width), h: int(height), stride: int(stride), msbFirst: byteOrder == 1}
	if want := sc.off + int64(sc.stride)*int64(sc.h); size < want || size > want+4096 {
		return xvfbScreen{}, fmt.Errorf("%d bytes, not the %d its screen takes", size, want)
	}
	return sc, nil
}

// read is r of the screen as RGBA, r's own size whatever of it is off the
// screen (black there): a frame's pixels line up with the window's rect,
// which clicks are mapped against.
func (fb *xvfbFB) read(r image.Rectangle) (*image.RGBA, error) {
	if r.Empty() || r.Dx() > xvfbMaxSide || r.Dy() > xvfbMaxSide {
		return nil, fmt.Errorf("a %v window", r)
	}
	f, fi, err := openFramebuffer(fb.path, fb.owner)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc, err := screenOf(f, fi.Size())
	if err != nil {
		return nil, fmt.Errorf("%s: %w", fb.path, err)
	}
	on := r.Intersect(image.Rect(0, 0, sc.w, sc.h))
	if on.Empty() {
		return nil, errors.New("the window is not on the screen")
	}
	// Its rows in one read: from its first pixel on the screen to its last,
	// the screen's own stride between them.
	n := (on.Dy()-1)*sc.stride + on.Dx()*4
	bp := xvfbReadBuf.Get().(*[]byte)
	defer xvfbReadBuf.Put(bp)
	if cap(*bp) < n {
		*bp = make([]byte, n)
	}
	block := (*bp)[:n]
	if _, err := f.ReadAt(block, sc.off+int64(on.Min.Y)*int64(sc.stride)+int64(on.Min.X)*4); err != nil {
		return nil, err
	}
	img := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	if on != r { // partly off the screen: black, opaque, where it is not
		for i := 3; i < len(img.Pix); i += 4 {
			img.Pix[i] = 0xff
		}
	}
	dx, dy := on.Min.X-r.Min.X, on.Min.Y-r.Min.Y
	for y := 0; y < on.Dy(); y++ {
		row := block[y*sc.stride : y*sc.stride+on.Dx()*4]
		dst := img.Pix[(dy+y)*img.Stride+dx*4:]
		for i := 0; i < len(row); i += 4 {
			if sc.msbFirst { // X R G B
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

// xvfbExact says the last frame captured came from a framebuffer file (and
// so is the same bytes until its pixels change): what exactFrames answers.
var xvfbExact atomic.Bool

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
