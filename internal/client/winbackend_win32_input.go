// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build windows

package client

// Input injection for the Win32 backend — everything goes through SendInput,
// with mouse positions normalized to the 0..65535 virtual-desktop space
// (MOUSEEVENTF_ABSOLUTE|MOUSEEVENTF_VIRTUALDESK), so multi-monitor layouts and
// negative coordinates just work. SendInput has no thread-affinity
// requirements, so plain goroutine scheduling is fine here (unlike the
// AttachThreadInput bracket in focus(), which pins its OS thread).

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf16"
	"unsafe"
)

const (
	w32InputMouse    = 0
	w32InputKeyboard = 1

	w32MouseEventFMove        = 0x0001
	w32MouseEventFLeftDown    = 0x0002
	w32MouseEventFLeftUp      = 0x0004
	w32MouseEventFRightDown   = 0x0008
	w32MouseEventFRightUp     = 0x0010
	w32MouseEventFWheel       = 0x0800
	w32MouseEventFHWheel      = 0x1000
	w32MouseEventFVirtualDesk = 0x4000
	w32MouseEventFAbsolute    = 0x8000

	w32KeyEventFKeyUp   = 0x0002
	w32KeyEventFUnicode = 0x0004

	w32VKShift   = 0x10
	w32VKControl = 0x11
	w32VKMenu    = 0x12 // ALT
	w32VKLWin    = 0x5B

	w32WheelDelta = 120 // one wheel notch
)

// w32MouseInput is MOUSEINPUT — also the largest member of the C INPUT union.
type w32MouseInput struct {
	Dx, Dy    int32
	MouseData uint32 // wheel delta (signed, stored as uint32) or XBUTTON id
	Flags     uint32
	Time      uint32
	ExtraInfo uintptr
}

// w32KeybdInput is KEYBDINPUT. Smaller than MOUSEINPUT; keyboard events are
// written into a w32Input by overlaying this onto the Mi union space.
type w32KeybdInput struct {
	Vk        uint16
	Scan      uint16
	Flags     uint32
	Time      uint32
	ExtraInfo uintptr
}

// w32Input is INPUT. In C the union follows the DWORD type field at
// pointer-alignment; Go reproduces that layout naturally, because Mi contains
// a uintptr and is therefore word-aligned on every GOARCH (offset 8 on 64-bit,
// 4 on 386 — exactly matching the C struct, so sizeof matches cbSize).
type w32Input struct {
	Type uint32
	Mi   w32MouseInput // the union; keyboard events overlay w32KeybdInput here
}

// w32KeyInput builds a keyboard INPUT by overlaying KEYBDINPUT onto the union.
func w32KeyInput(vk, scan uint16, flags uint32) w32Input {
	in := w32Input{Type: w32InputKeyboard}
	*(*w32KeybdInput)(unsafe.Pointer(&in.Mi)) = w32KeybdInput{Vk: vk, Scan: scan, Flags: flags}
	return in
}

// w32SendInputs injects a batch of events atomically (one SendInput call keeps
// them ordered and uninterleaved with real user input).
func w32SendInputs(ins []w32Input) error {
	if len(ins) == 0 {
		return nil
	}
	n, _, callErr := w32ProcSendInput.Call(uintptr(len(ins)),
		uintptr(unsafe.Pointer(&ins[0])), unsafe.Sizeof(ins[0]))
	if int(n) != len(ins) {
		return fmt.Errorf("SendInput injected %d/%d events: %v", n, len(ins), callErr)
	}
	return nil
}

func w32Metric(index int) int {
	r, _, _ := w32ProcGetSystemMetrics.Call(uintptr(index))
	return int(int32(r))
}

// w32MouseMoveInput builds an absolute move to a virtual-screen point,
// normalized to the 0..65535 grid MOUSEEVENTF_VIRTUALDESK expects.
func w32MouseMoveInput(x, y int) w32Input {
	vx, vy := w32Metric(w32SMXVirtualScreen), w32Metric(w32SMYVirtualScreen)
	vw, vh := w32Metric(w32SMCXVirtualScreen), w32Metric(w32SMCYVirtualScreen)
	if vw < 2 {
		vw = 2
	}
	if vh < 2 {
		vh = 2
	}
	nx := (x - vx) * 65535 / (vw - 1)
	ny := (y - vy) * 65535 / (vh - 1)
	clamp := func(v int) int32 {
		if v < 0 {
			return 0
		}
		if v > 65535 {
			return 65535
		}
		return int32(v)
	}
	return w32Input{Type: w32InputMouse, Mi: w32MouseInput{
		Dx: clamp(nx), Dy: clamp(ny),
		Flags: w32MouseEventFMove | w32MouseEventFAbsolute | w32MouseEventFVirtualDesk,
	}}
}

// w32MouseButtonInput builds a button/wheel event at the current cursor
// position (data carries the signed wheel delta for WHEEL/HWHEEL).
func w32MouseButtonInput(flags uint32, data int32) w32Input {
	return w32Input{Type: w32InputMouse, Mi: w32MouseInput{MouseData: uint32(data), Flags: flags}}
}

// w32Point maps a (fx, fy) 0..1 fraction from a window's top-left to absolute
// virtual-screen coordinates. Works for display pseudo-windows too — their
// rect is the monitor's.
func w32Point(w winInfo, fx, fy float64) (int, int) {
	return w.X + int(fx*float64(w.W)), w.Y + int(fy*float64(w.H))
}

func (c win32Windows) clickN(w winInfo, fx, fy float64, count int, right bool) error {
	if count < 1 {
		count = 1
	}
	_ = c.focus(w) // no-op for displays and already-foreground windows
	x, y := w32Point(w, fx, fy)
	down, up := uint32(w32MouseEventFLeftDown), uint32(w32MouseEventFLeftUp)
	if right {
		down, up = w32MouseEventFRightDown, w32MouseEventFRightUp
	}
	if err := w32SendInputs([]w32Input{w32MouseMoveInput(x, y)}); err != nil {
		return err
	}
	// count× down+up at the same spot. The pairs sit well inside the system
	// double-click time (default 500ms), so Windows itself coalesces them into
	// native double/triple-clicks; the ~40ms gap between pairs (and the short
	// press) keeps apps from reading the burst as one long press.
	for i := 0; i < count; i++ {
		if i > 0 {
			time.Sleep(40 * time.Millisecond)
		}
		if err := w32SendInputs([]w32Input{w32MouseButtonInput(down, 0)}); err != nil {
			return err
		}
		time.Sleep(20 * time.Millisecond)
		if err := w32SendInputs([]w32Input{w32MouseButtonInput(up, 0)}); err != nil {
			return err
		}
	}
	return nil
}

func (c win32Windows) drag(w winInfo, pts [][2]float64) error {
	if len(pts) == 0 {
		return nil
	}
	_ = c.focus(w)
	x, y := w32Point(w, pts[0][0], pts[0][1])
	if err := w32SendInputs([]w32Input{w32MouseMoveInput(x, y)}); err != nil {
		return err
	}
	time.Sleep(20 * time.Millisecond)
	if err := w32SendInputs([]w32Input{w32MouseButtonInput(w32MouseEventFLeftDown, 0)}); err != nil {
		return err
	}
	// Hold briefly, then move through the path with small gaps so apps track a
	// real press-and-move (text selection, sliders, drag-and-drop) instead of
	// collapsing an instantaneous press+release into a click.
	time.Sleep(40 * time.Millisecond)
	for _, p := range pts[1:] {
		x, y = w32Point(w, p[0], p[1])
		if err := w32SendInputs([]w32Input{w32MouseMoveInput(x, y)}); err != nil {
			return err
		}
		time.Sleep(15 * time.Millisecond)
	}
	time.Sleep(40 * time.Millisecond)
	return w32SendInputs([]w32Input{w32MouseButtonInput(w32MouseEventFLeftUp, 0)})
}

// w32WheelAmount converts a viewer pixel-ish delta into a signed wheel value:
// ~50px ≈ one 120-unit notch, fractional notches allowed (high-resolution
// wheel events are legal and smooth-scrolling apps honor them), a minimum
// nudge so tiny deltas still move, and a cap so one flick can't fire a page's
// worth of notches.
func w32WheelAmount(d float64) int32 {
	if d == 0 {
		return 0
	}
	v := int32(d * w32WheelDelta / 50)
	if v == 0 {
		if d > 0 {
			v = w32WheelDelta / 4
		} else {
			v = -w32WheelDelta / 4
		}
	}
	if v > 10*w32WheelDelta {
		v = 10 * w32WheelDelta
	}
	if v < -10*w32WheelDelta {
		v = -10 * w32WheelDelta
	}
	return v
}

func (win32Windows) scroll(w winInfo, fx, fy, dx, dy float64) error {
	// Wheel events land on the window under the cursor, so move there first.
	x, y := w32Point(w, fx, fy)
	ins := []w32Input{w32MouseMoveInput(x, y)}
	// Vertical: positive viewer dy means "content scrolls down", which a real
	// wheel reports as a NEGATIVE delta (wheel rolled toward the user) — so
	// negate. Horizontal HWHEEL is positive-right, matching the viewer's dx.
	if v := w32WheelAmount(-dy); v != 0 {
		ins = append(ins, w32MouseButtonInput(w32MouseEventFWheel, v))
	}
	if h := w32WheelAmount(dx); h != 0 {
		ins = append(ins, w32MouseButtonInput(w32MouseEventFHWheel, h))
	}
	if len(ins) == 1 {
		return nil // nothing to scroll
	}
	return w32SendInputs(ins)
}

func (c win32Windows) typeText(w winInfo, text string) error {
	_ = c.focus(w)
	// KEYEVENTF_UNICODE injects literal UTF-16 code units, independent of the
	// keyboard layout — one down+up pair per unit, so astral characters
	// (emoji) arrive as their surrogate pair and reassemble correctly.
	units := utf16.Encode([]rune(text))
	ins := make([]w32Input, 0, len(units)*2)
	for _, u := range units {
		ins = append(ins,
			w32KeyInput(0, u, w32KeyEventFUnicode),
			w32KeyInput(0, u, w32KeyEventFUnicode|w32KeyEventFKeyUp))
	}
	return w32SendInputs(ins)
}

// w32KeyCodes maps the viewer's neutral special-key names (the same set the
// darwin/linux backends accept) to Windows virtual-key codes. "delete" is the
// viewer's backspace, matching the other backends.
var w32KeyCodes = map[string]uint16{
	"return": 0x0D, "enter": 0x0D, "tab": 0x09, "space": 0x20,
	"delete": 0x08, "forwarddelete": 0x2E, "escape": 0x1B,
	"left": 0x25, "right": 0x27, "down": 0x28, "up": 0x26,
	"home": 0x24, "end": 0x23, "pageup": 0x21, "pagedown": 0x22,
}

func (c win32Windows) key(w winInfo, name string) error {
	_ = c.focus(w)
	name = strings.ToLower(name)
	// "ctrl-x" chords (the keybar's ^C/^D/… in window-keyboard mode), same
	// form the darwin/linux backends handle.
	if ch, ok := strings.CutPrefix(name, "ctrl-"); ok && len(ch) == 1 && ch[0] >= 'a' && ch[0] <= 'z' {
		vk := uint16(0x41 + ch[0] - 'a') // VK_A..VK_Z
		return w32SendInputs([]w32Input{
			w32KeyInput(w32VKControl, 0, 0),
			w32KeyInput(vk, 0, 0),
			w32KeyInput(vk, 0, w32KeyEventFKeyUp),
			w32KeyInput(w32VKControl, 0, w32KeyEventFKeyUp),
		})
	}
	vk, ok := w32KeyCodes[name]
	if !ok {
		return fmt.Errorf("unknown key %q", name)
	}
	return w32SendInputs([]w32Input{
		w32KeyInput(vk, 0, 0),
		w32KeyInput(vk, 0, w32KeyEventFKeyUp),
	})
}

func (win32Windows) releaseInput() error {
	// Release both mouse buttons (at the current cursor position) and the
	// modifiers our own injection can hold, so an interrupted click/drag or
	// ctrl-chord never leaves the host's desktop stuck in a grab. Releasing an
	// already-up button/key is a harmless no-op.
	return w32SendInputs([]w32Input{
		w32MouseButtonInput(w32MouseEventFLeftUp, 0),
		w32MouseButtonInput(w32MouseEventFRightUp, 0),
		w32KeyInput(w32VKShift, 0, w32KeyEventFKeyUp),
		w32KeyInput(w32VKControl, 0, w32KeyEventFKeyUp),
		w32KeyInput(w32VKMenu, 0, w32KeyEventFKeyUp),
		w32KeyInput(w32VKLWin, 0, w32KeyEventFKeyUp),
	})
}
