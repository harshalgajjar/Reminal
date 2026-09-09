// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build windows

package client

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	w32Shell32            = windows.NewLazySystemDLL("shell32.dll")
	w32Ole32              = windows.NewLazySystemDLL("ole32.dll")
	w32ProcSHGetFileInfoW = w32Shell32.NewProc("SHGetFileInfoW")
	w32ProcDestroyIcon    = w32User32.NewProc("DestroyIcon")
	w32ProcDrawIconEx     = w32User32.NewProc("DrawIconEx")
	w32ProcCoInitializeEx = w32Ole32.NewProc("CoInitializeEx")
)

const (
	w32SHGFI_ICON               = 0x000000100
	w32SHGFI_LARGEICON          = 0x000000000 // 32x32
	w32DI_NORMAL                = 0x0003
	w32COINIT_APARTMENTTHREADED = 0x2
)

// w32ShFileInfoW is SHFILEINFOW; only HIcon is read.
type w32ShFileInfoW struct {
	HIcon       windows.Handle
	IIcon       int32
	Attributes  uint32
	DisplayName [260]uint16
	TypeName    [80]uint16
}

// The shell's icon lookup (SHGetFileInfo with SHGFI_ICON, which resolves a .lnk
// to its target's icon) must run on a thread with COM initialized as an STA, or
// it blocks. So all extraction happens on ONE dedicated apartment-threaded
// worker: it holds its OS thread and COM apartment for the life of the process,
// and callers hand it one path at a time. One place does the Win32 dance; the
// rest of the code just asks for a PNG.
var (
	iconReqCh      = make(chan iconReq)
	iconWorkerOnce sync.Once
)

type iconReq struct {
	path  string
	reply chan string
}

func iconWorker() {
	runtime.LockOSThread() // COM apartment is per-thread; never let the runtime move us
	w32ProcCoInitializeEx.Call(0, w32COINIT_APARTMENTTHREADED)
	for req := range iconReqCh {
		ic, _ := win32IconPNG(req.path)
		req.reply <- ic
	}
}

// win32IconExtract returns the icon for one path via the apartment-threaded
// worker, or ("", false) if the worker doesn't answer within budget — a slow or
// wedged shell lookup for one app never holds up the whole list.
func win32IconExtract(path string, budget time.Duration) (string, bool) {
	iconWorkerOnce.Do(func() { go iconWorker() })
	reply := make(chan string, 1)
	select {
	case iconReqCh <- iconReq{path: path, reply: reply}:
	case <-time.After(budget):
		return "", false
	}
	select {
	case ic := <-reply:
		return ic, true
	case <-time.After(budget):
		return "", false
	}
}

// win32IconPNG extracts a shortcut's associated icon (via SHGetFileInfo, so a
// .lnk resolves to its target's icon) and returns it as a 32×32 PNG data URL.
// MUST be called on the apartment-threaded icon worker (see iconWorker).
func win32IconPNG(path string) (string, bool) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return "", false
	}
	var shfi w32ShFileInfoW
	r, _, _ := w32ProcSHGetFileInfoW.Call(
		uintptr(unsafe.Pointer(p)), 0,
		uintptr(unsafe.Pointer(&shfi)), unsafe.Sizeof(shfi),
		w32SHGFI_ICON|w32SHGFI_LARGEICON)
	if r == 0 || shfi.HIcon == 0 {
		return "", false
	}
	defer w32ProcDestroyIcon.Call(uintptr(shfi.HIcon))

	const sz = 32
	hdc, _, _ := w32ProcCreateCompatibleDC.Call(0)
	if hdc == 0 {
		return "", false
	}
	defer w32ProcDeleteDC.Call(hdc)
	bmp, bits, err := w32NewDIB(hdc, sz, sz)
	if err != nil {
		return "", false
	}
	defer w32ProcDeleteObject.Call(bmp)
	old, _, _ := w32ProcSelectObject.Call(hdc, bmp)
	defer w32ProcSelectObject.Call(hdc, old)

	if ok, _, _ := w32ProcDrawIconEx.Call(hdc, 0, 0, uintptr(shfi.HIcon), sz, sz, 0, 0, w32DI_NORMAL); ok == 0 {
		return "", false
	}

	// bits is top-down BGRA. Modern icons carry per-pixel alpha; legacy ones
	// leave it all zero — treat those as opaque so the icon shows. DrawIconEx
	// writes premultiplied colour, so un-premultiply for a straight-alpha PNG.
	anyAlpha := false
	for i := 3; i < len(bits); i += 4 {
		if bits[i] != 0 {
			anyAlpha = true
			break
		}
	}
	img := image.NewNRGBA(image.Rect(0, 0, sz, sz))
	for i := 0; i+3 < len(bits); i += 4 {
		b, g, r, a := bits[i], bits[i+1], bits[i+2], bits[i+3]
		if !anyAlpha {
			a = 0xFF
		} else if a != 0 && a != 0xFF {
			r, g, b = unpremul(r, a), unpremul(g, a), unpremul(b, a)
		}
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = r, g, b, a
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return "", false
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()), true
}

func unpremul(c, a uint8) uint8 {
	if v := int(c) * 255 / int(a); v < 255 {
		return uint8(v)
	}
	return 255
}
