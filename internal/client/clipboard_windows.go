// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build windows

package client

import (
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Win32 clipboard, bound lazily (x/sys/windows wraps none of it). The global-
// memory trio is needed because SetClipboardData doesn't take a plain buffer:
// it takes ownership of a GMEM_MOVEABLE handle.
var (
	clipUser32           = windows.NewLazySystemDLL("user32.dll")
	procOpenClipboard    = clipUser32.NewProc("OpenClipboard")
	procCloseClipboard   = clipUser32.NewProc("CloseClipboard")
	procEmptyClipboard   = clipUser32.NewProc("EmptyClipboard")
	procSetClipboardData = clipUser32.NewProc("SetClipboardData")

	clipKernel32      = windows.NewLazySystemDLL("kernel32.dll")
	procGlobalAlloc   = clipKernel32.NewProc("GlobalAlloc")
	procGlobalFree    = clipKernel32.NewProc("GlobalFree")
	procGlobalLock    = clipKernel32.NewProc("GlobalLock")
	procGlobalUnlock  = clipKernel32.NewProc("GlobalUnlock")
	procRtlMoveMemory = clipKernel32.NewProc("RtlMoveMemory")
)

const (
	cfUnicodeText = 13     // CF_UNICODETEXT: UTF-16, NUL-terminated
	gmemMoveable  = 0x0002 // GMEM_MOVEABLE: required for clipboard handles
)

// writeClipboardNative puts text on the Windows clipboard as CF_UNICODETEXT.
// Native counterpart of the pbcopy/xclip subprocesses agent.go uses elsewhere.
// Best-effort: false on any failure, and the caller falls back to OSC 52.
//
// Two Win32 contracts matter here. First, the clipboard is a single mutex-like
// resource: OpenClipboard fails whenever any other app momentarily holds it
// (clipboard managers poll constantly), so a couple of short retries turn
// "flaky" into "reliable". Second, ownership of the memory transfers on
// success: once SetClipboardData accepts the handle the SYSTEM frees it, and a
// GlobalFree of our own would be a double-free — we only free on the failure
// paths.
func writeClipboardNative(text string) bool {
	// UTF16FromString appends the terminating NUL CF_UNICODETEXT requires, but
	// rejects interior NULs — strip them (clipboard text truncates there anyway).
	u16, err := syscall.UTF16FromString(strings.ReplaceAll(text, "\x00", ""))
	if err != nil {
		return false
	}
	byteLen := uintptr(len(u16)) * 2

	if !openClipboardRetry() {
		return false
	}
	defer procCloseClipboard.Call()

	if r, _, _ := procEmptyClipboard.Call(); r == 0 {
		return false
	}

	hMem, _, _ := procGlobalAlloc.Call(gmemMoveable, byteLen)
	if hMem == 0 {
		return false
	}
	p, _, _ := procGlobalLock.Call(hMem)
	if p == 0 {
		procGlobalFree.Call(hMem)
		return false
	}
	// Copy with RtlMoveMemory rather than converting p back to a Go pointer:
	// p addresses non-Go memory, and keeping it as a uintptr the whole way is
	// the pattern vet's unsafeptr check sanctions.
	procRtlMoveMemory.Call(p, uintptr(unsafe.Pointer(&u16[0])), byteLen)
	procGlobalUnlock.Call(hMem)

	if r, _, _ := procSetClipboardData.Call(cfUnicodeText, hMem); r == 0 {
		procGlobalFree.Call(hMem) // still ours — SetClipboardData refused it
		return false
	}
	// Success: the system owns hMem now. Do NOT free it.
	return true
}

// openClipboardRetry opens the clipboard with a few short-backoff attempts,
// unowned (hwnd 0). See writeClipboardNative for why one attempt isn't enough.
func openClipboardRetry() bool {
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			time.Sleep(50 * time.Millisecond)
		}
		if r, _, _ := procOpenClipboard.Call(0); r != 0 {
			return true
		}
	}
	return false
}
