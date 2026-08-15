// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build windows

package client

// GDI capture for the Win32 backend: PrintWindow (works occluded) with a
// screen-BitBlt fallback, 32bpp DIB → image.RGBA → box-averaged downscale →
// JPEG. No cgo, no image deps beyond the stdlib.

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"unsafe"
)

const (
	w32SRCCOPY    = 0x00CC0020
	w32CAPTUREBLT = 0x40000000 // include layered (alpha-blended) windows in a screen blt

	// PW_RENDERFULLCONTENT (undocumented but long-stable): ask the window to
	// render its DWM-composited content, which captures DirectX/hardware-drawn
	// surfaces (browsers, Electron) that plain PrintWindow returns black for.
	w32PWRenderFullContent = 2

	w32DIBRGBColors = 0

	// Downscale ceiling and JPEG quality for outgoing frames (quality matches
	// the macOS screencapture fallback's sips settings).
	w32MaxCaptureWidth = 1440
	w32JPEGQuality     = 45
)

// w32BitmapInfoHeader is BITMAPINFOHEADER.
type w32BitmapInfoHeader struct {
	Size          uint32
	Width, Height int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

// w32NewDIB creates a w×h 32bpp DIB section and returns the bitmap handle plus
// its pixel memory as a byte slice (BGRA, valid until DeleteObject). Height is
// passed NEGATIVE: a positive biHeight means a bottom-up bitmap (last row
// first — the GDI default), while negative asks for top-down rows, so the
// buffer reads like a normal image and needs no flip.
func w32NewDIB(dc uintptr, w, h int) (bmp uintptr, bits []byte, err error) {
	bi := w32BitmapInfoHeader{
		Size: uint32(unsafe.Sizeof(w32BitmapInfoHeader{})), Width: int32(w), Height: -int32(h),
		Planes: 1, BitCount: 32,
	}
	var pbits unsafe.Pointer
	bmp, _, _ = w32ProcCreateDIBSection.Call(dc, uintptr(unsafe.Pointer(&bi)),
		w32DIBRGBColors, uintptr(unsafe.Pointer(&pbits)), 0, 0)
	if bmp == 0 || pbits == nil {
		return 0, nil, fmt.Errorf("CreateDIBSection %dx%d failed", w, h)
	}
	return bmp, unsafe.Slice((*byte)(pbits), w*h*4), nil
}

// w32BGRAToRGBA converts (a crop of) a top-down 32bpp BGRA buffer to an
// image.RGBA. GDI stores pixels little-endian 0x00RRGGBB, i.e. B,G,R,X in
// memory — so red and blue swap, and the unused X byte becomes opaque alpha.
// Must be called before the DIB is deleted (bits aliases its memory).
func w32BGRAToRGBA(bits []byte, srcW int, crop image.Rectangle) *image.RGBA {
	out := image.NewRGBA(image.Rect(0, 0, crop.Dx(), crop.Dy()))
	stride := srcW * 4
	for y := 0; y < crop.Dy(); y++ {
		srow := bits[(crop.Min.Y+y)*stride+crop.Min.X*4:]
		drow := out.Pix[y*out.Stride:]
		for x := 0; x < crop.Dx(); x++ {
			drow[x*4+0] = srow[x*4+2] // R ← B position
			drow[x*4+1] = srow[x*4+1] // G
			drow[x*4+2] = srow[x*4+0] // B ← R position
			drow[x*4+3] = 0xFF
		}
	}
	return out
}

// w32AllBlack reports whether every pixel is pure black — the signature of a
// PrintWindow call that "succeeded" but drew nothing (some GPU-rendered and
// UWP windows do this). Exits on the first lit pixel, so real content returns
// almost immediately; a genuinely all-black window falls back to BitBlt, which
// just produces the same black, so the false positive is harmless.
func w32AllBlack(bits []byte) bool {
	for i := 0; i+2 < len(bits); i += 4 {
		if bits[i] != 0 || bits[i+1] != 0 || bits[i+2] != 0 {
			return false
		}
	}
	return true
}

// w32Downscale box-averages src down to at most maxW wide, keeping aspect.
// Box averaging (each destination pixel = mean of its source box) is a proper
// minification filter — nearest-neighbour would alias text badly at the 2-3×
// reductions a 4K capture needs. Never upscales.
func w32Downscale(src *image.RGBA, maxW int) *image.RGBA {
	sw, sh := src.Rect.Dx(), src.Rect.Dy()
	if sw <= maxW || sw == 0 || sh == 0 {
		return src
	}
	dw := maxW
	dh := sh * maxW / sw
	if dh < 1 {
		dh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for dy := 0; dy < dh; dy++ {
		y0, y1 := dy*sh/dh, (dy+1)*sh/dh
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for dx := 0; dx < dw; dx++ {
			x0, x1 := dx*sw/dw, (dx+1)*sw/dw
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var r, g, b, n uint32
			for y := y0; y < y1; y++ {
				row := src.Pix[y*src.Stride:]
				for x := x0; x < x1; x++ {
					r += uint32(row[x*4])
					g += uint32(row[x*4+1])
					b += uint32(row[x*4+2])
					n++
				}
			}
			o := dy*dst.Stride + dx*4
			dst.Pix[o] = byte(r / n)
			dst.Pix[o+1] = byte(g / n)
			dst.Pix[o+2] = byte(b / n)
			dst.Pix[o+3] = 0xFF
		}
	}
	return dst
}

// w32EncodeFrame downscales and JPEG-encodes a captured frame.
func w32EncodeFrame(img *image.RGBA) ([]byte, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, w32Downscale(img, w32MaxCaptureWidth),
		&jpeg.Options{Quality: w32JPEGQuality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// w32BitBltScreen grabs a virtual-screen rectangle from the screen DC.
// GetDC(NULL) spans the whole virtual desktop, so negative coordinates (a
// monitor left of or above the primary) work directly. CAPTUREBLT includes
// layered windows (tooltips, some menus) that a plain SRCCOPY would skip.
func w32BitBltScreen(x, y, w, h int) (*image.RGBA, error) {
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("empty capture rect %dx%d", w, h)
	}
	sdc, _, _ := w32ProcGetDC.Call(0)
	if sdc == 0 {
		return nil, fmt.Errorf("GetDC(screen) failed")
	}
	defer w32ProcReleaseDC.Call(0, sdc)
	mdc, _, _ := w32ProcCreateCompatibleDC.Call(sdc)
	if mdc == 0 {
		return nil, fmt.Errorf("CreateCompatibleDC failed")
	}
	defer w32ProcDeleteDC.Call(mdc)
	bmp, bits, err := w32NewDIB(sdc, w, h)
	if err != nil {
		return nil, err
	}
	defer w32ProcDeleteObject.Call(bmp)
	old, _, _ := w32ProcSelectObject.Call(mdc, bmp)
	defer w32ProcSelectObject.Call(mdc, old)
	if r, _, _ := w32ProcBitBlt.Call(mdc, 0, 0, uintptr(w), uintptr(h),
		sdc, uintptr(x), uintptr(y), w32SRCCOPY|w32CAPTUREBLT); r == 0 {
		return nil, fmt.Errorf("BitBlt failed")
	}
	return w32BGRAToRGBA(bits, w, image.Rect(0, 0, w, h)), nil
}

// w32PrintWindowCapture asks the window to render itself into a DIB via
// PrintWindow — which, unlike a screen blt, captures the window even when
// occluded or on another part of the desktop. The DIB is sized to the FULL
// GetWindowRect (PrintWindow paints the invisible resize-border shadow too),
// then cropped to the DWM frame bounds so pixels line up 1:1 with the rect we
// enumerate and map clicks against. ok=false → caller falls back to BitBlt.
func w32PrintWindowCapture(hwnd uintptr, frame w32Rect) ([]byte, bool) {
	wr, ok := w32WindowRect(hwnd)
	if !ok {
		return nil, false
	}
	fullW, fullH := int(wr.Right-wr.Left), int(wr.Bottom-wr.Top)
	if fullW <= 0 || fullH <= 0 {
		return nil, false
	}
	sdc, _, _ := w32ProcGetDC.Call(0)
	if sdc == 0 {
		return nil, false
	}
	defer w32ProcReleaseDC.Call(0, sdc)
	mdc, _, _ := w32ProcCreateCompatibleDC.Call(sdc)
	if mdc == 0 {
		return nil, false
	}
	defer w32ProcDeleteDC.Call(mdc)
	bmp, bits, err := w32NewDIB(sdc, fullW, fullH)
	if err != nil {
		return nil, false
	}
	defer w32ProcDeleteObject.Call(bmp)
	old, _, _ := w32ProcSelectObject.Call(mdc, bmp)
	defer w32ProcSelectObject.Call(mdc, old)
	if r, _, _ := w32ProcPrintWindow.Call(hwnd, mdc, w32PWRenderFullContent); r == 0 {
		return nil, false
	}
	if w32AllBlack(bits) {
		return nil, false // rendered nothing — let BitBlt try the screen pixels
	}
	crop := image.Rect(int(frame.Left-wr.Left), int(frame.Top-wr.Top),
		int(frame.Right-wr.Left), int(frame.Bottom-wr.Top)).
		Intersect(image.Rect(0, 0, fullW, fullH))
	if crop.Empty() {
		crop = image.Rect(0, 0, fullW, fullH)
	}
	img, err := w32EncodeFrame(w32BGRAToRGBA(bits, fullW, crop))
	return img, err == nil
}

func (win32Windows) capture(w winInfo) ([]byte, error) {
	if isDisplayID(w.ID) {
		// Whole desktop: blt the monitor rect straight off the screen DC.
		rgba, err := w32BitBltScreen(w.X, w.Y, w.W, w.H)
		if err != nil {
			return nil, err
		}
		return w32EncodeFrame(rgba)
	}
	hwnd, err := w32ParseHWND(w.ID)
	if err != nil {
		return nil, err
	}
	frame, ok := w32FrameBounds(hwnd)
	if !ok {
		frame, _ = w32WindowRect(hwnd)
	}
	if img, ok := w32PrintWindowCapture(hwnd, frame); ok {
		return img, nil
	}
	// PrintWindow failed or drew all-black — grab the window's screen rect
	// instead (loses occluded content, but always shows what the user sees).
	rgba, err := w32BitBltScreen(w.X, w.Y, w.W, w.H)
	if err != nil {
		return nil, err
	}
	return w32EncodeFrame(rgba)
}

func (win32Windows) captureRegion(x, y, w, h int) ([]byte, error) {
	// Raw screen rect (context menus overlapping the window, etc.) — same
	// convert/scale/encode pipeline as window capture.
	rgba, err := w32BitBltScreen(x, y, w, h)
	if err != nil {
		return nil, err
	}
	return w32EncodeFrame(rgba)
}
