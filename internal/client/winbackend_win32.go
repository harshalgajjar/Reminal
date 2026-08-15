// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build windows

package client

// Native Win32 window-mirroring backend. Pure Go — every OS call goes through
// golang.org/x/sys/windows (process/shell APIs) or lazily-loaded user32/gdi32/
// dwmapi procs, so there is no cgo and nothing extra to install: Win32 is
// always present, which is why unsupported() is "".
//
// Split across three files: this one (enumeration, focus, apps, plumbing),
// winbackend_win32_capture.go (GDI capture → JPEG), and
// winbackend_win32_input.go (SendInput injection).

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	w32User32 = windows.NewLazySystemDLL("user32.dll")
	w32Gdi32  = windows.NewLazySystemDLL("gdi32.dll")
	w32Dwmapi = windows.NewLazySystemDLL("dwmapi.dll")

	w32ProcSetProcessDpiAwarenessContext = w32User32.NewProc("SetProcessDpiAwarenessContext")
	w32ProcSetProcessDPIAware            = w32User32.NewProc("SetProcessDPIAware")
	w32ProcEnumWindows                   = w32User32.NewProc("EnumWindows")
	w32ProcEnumDisplayMonitors           = w32User32.NewProc("EnumDisplayMonitors")
	w32ProcGetMonitorInfoW               = w32User32.NewProc("GetMonitorInfoW")
	w32ProcIsWindow                      = w32User32.NewProc("IsWindow")
	w32ProcIsWindowVisible               = w32User32.NewProc("IsWindowVisible")
	w32ProcIsIconic                      = w32User32.NewProc("IsIconic")
	w32ProcShowWindow                    = w32User32.NewProc("ShowWindow")
	w32ProcSetForegroundWindow           = w32User32.NewProc("SetForegroundWindow")
	w32ProcGetForegroundWindow           = w32User32.NewProc("GetForegroundWindow")
	w32ProcBringWindowToTop              = w32User32.NewProc("BringWindowToTop")
	w32ProcAttachThreadInput             = w32User32.NewProc("AttachThreadInput")
	w32ProcGetWindowTextW                = w32User32.NewProc("GetWindowTextW")
	w32ProcGetWindowLongW                = w32User32.NewProc("GetWindowLongW")
	w32ProcGetWindowThreadProcessId      = w32User32.NewProc("GetWindowThreadProcessId")
	w32ProcGetWindowRect                 = w32User32.NewProc("GetWindowRect")
	w32ProcGetDC                         = w32User32.NewProc("GetDC")
	w32ProcReleaseDC                     = w32User32.NewProc("ReleaseDC")
	w32ProcPrintWindow                   = w32User32.NewProc("PrintWindow")
	w32ProcSendInput                     = w32User32.NewProc("SendInput")
	w32ProcGetSystemMetrics              = w32User32.NewProc("GetSystemMetrics")

	w32ProcCreateCompatibleDC = w32Gdi32.NewProc("CreateCompatibleDC")
	w32ProcCreateDIBSection   = w32Gdi32.NewProc("CreateDIBSection")
	w32ProcSelectObject       = w32Gdi32.NewProc("SelectObject")
	w32ProcDeleteObject       = w32Gdi32.NewProc("DeleteObject")
	w32ProcDeleteDC           = w32Gdi32.NewProc("DeleteDC")
	w32ProcBitBlt             = w32Gdi32.NewProc("BitBlt")

	w32ProcDwmGetWindowAttribute = w32Dwmapi.NewProc("DwmGetWindowAttribute")
)

const (
	// DwmGetWindowAttribute attributes.
	w32DWMWAExtendedFrameBounds = 9  // the visible frame rect, minus the invisible resize-border shadow
	w32DWMWACloaked             = 14 // nonzero for windows hidden by DWM (other virtual desktops, suspended UWP)

	w32WSExToolWindow = 0x00000080 // WS_EX_TOOLWINDOW — palettes/tooltips, not user-facing windows
	w32SWRestore      = 9          // ShowWindow: un-minimize

	// Virtual-screen metrics (GetSystemMetrics) — the bounding box of every
	// monitor, which is also the space MOUSEEVENTF_VIRTUALDESK normalizes over.
	w32SMXVirtualScreen  = 76
	w32SMYVirtualScreen  = 77
	w32SMCXVirtualScreen = 78
	w32SMCYVirtualScreen = 79
)

// w32GWLExstyle is GWL_EXSTYLE (-20) as the sign-extended uintptr Win64 expects.
var w32GWLExstyle = ^uintptr(19)

type w32Rect struct{ Left, Top, Right, Bottom int32 }

type w32MonitorInfo struct {
	Size          uint32
	Monitor, Work w32Rect
	Flags         uint32
}

// win32Windows is the Win32 windowBackend. Stateless — all calls are already
// serialized by the agent's winOps worker, and the enum-callback scratch state
// below carries its own mutex anyway (cheap insurance for list() callers off
// the worker).
type win32Windows struct{}

// newNativeWindowBackend returns the Win32 backend (the !windows counterpart
// in winbackend_other.go returns nil).
func newNativeWindowBackend() windowBackend {
	w32SetDPIAware()
	return win32Windows{}
}

// w32SetDPIAware opts this process into per-monitor DPI awareness, so window
// rects and GDI captures are in real physical pixels. Without it Windows
// virtualizes coordinates and bitmap sizes for "DPI-unaware" processes and
// captures come back scaled/blurry with rects that don't match the screen.
// PER_MONITOR_AWARE_V2 is a pseudo-handle with value -4; the API is Win10
// 1703+, so fall back to the ancient system-wide SetProcessDPIAware.
func w32SetDPIAware() {
	if w32ProcSetProcessDpiAwarenessContext.Find() == nil {
		const perMonitorAwareV2 = ^uintptr(3) // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 (-4)
		if r, _, _ := w32ProcSetProcessDpiAwarenessContext.Call(perMonitorAwareV2); r != 0 {
			return
		}
	}
	_, _, _ = w32ProcSetProcessDPIAware.Call()
}

func (win32Windows) unsupported() string    { return "" } // Win32 is always present
func (win32Windows) permissionHint() string { return "" } // no TCC-style capture permission on Windows

// ---- enumeration ------------------------------------------------------------

// Enum-callback scratch state. syscall.NewCallback allocations are permanent
// (and capped process-wide), so each callback is created exactly once and
// deposits into a package var; the mutex brackets one enumeration.
var (
	w32EnumMu  sync.Mutex
	w32EnumOut []uintptr
	w32EnumCB  = syscall.NewCallback(func(hwnd, _ uintptr) uintptr {
		w32EnumOut = append(w32EnumOut, hwnd)
		return 1 // keep enumerating
	})
	w32MonCB = syscall.NewCallback(func(hmon, _, _, _ uintptr) uintptr {
		w32EnumOut = append(w32EnumOut, hmon)
		return 1
	})
)

// w32TopLevelWindows returns every top-level HWND, in Z order.
func w32TopLevelWindows() []uintptr {
	w32EnumMu.Lock()
	defer w32EnumMu.Unlock()
	w32EnumOut = w32EnumOut[:0]
	_, _, _ = w32ProcEnumWindows.Call(w32EnumCB, 0)
	out := make([]uintptr, len(w32EnumOut))
	copy(out, w32EnumOut)
	return out
}

// w32Monitors returns each monitor's rect in virtual-screen coordinates.
func w32Monitors() []w32Rect {
	w32EnumMu.Lock()
	defer w32EnumMu.Unlock()
	w32EnumOut = w32EnumOut[:0]
	_, _, _ = w32ProcEnumDisplayMonitors.Call(0, 0, w32MonCB, 0)
	var rects []w32Rect
	for _, hmon := range w32EnumOut {
		mi := w32MonitorInfo{Size: uint32(unsafe.Sizeof(w32MonitorInfo{}))}
		if r, _, _ := w32ProcGetMonitorInfoW.Call(hmon, uintptr(unsafe.Pointer(&mi))); r != 0 {
			rects = append(rects, mi.Monitor)
		}
	}
	return rects
}

// w32ParseHWND recovers the numeric window handle from an "hwnd:<n>" id.
func w32ParseHWND(id string) (uintptr, error) {
	n, err := strconv.ParseUint(strings.TrimPrefix(id, "hwnd:"), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("bad window id %q", id)
	}
	return uintptr(n), nil
}

func w32WindowText(hwnd uintptr) string {
	var buf [512]uint16
	n, _, _ := w32ProcGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return windows.UTF16ToString(buf[:n])
}

func w32WindowRect(hwnd uintptr) (w32Rect, bool) {
	var r w32Rect
	ok, _, _ := w32ProcGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	return r, ok != 0
}

// w32FrameBounds returns the DWM extended frame bounds — the rect the user
// actually sees. GetWindowRect on modern Windows includes an invisible ~7px
// resize-border shadow on the left/right/bottom, which would offset every
// click we map from viewer fractions; the DWM rect excludes it.
func w32FrameBounds(hwnd uintptr) (w32Rect, bool) {
	var r w32Rect
	if w32ProcDwmGetWindowAttribute.Find() != nil {
		return r, false
	}
	hr, _, _ := w32ProcDwmGetWindowAttribute.Call(hwnd, w32DWMWAExtendedFrameBounds,
		uintptr(unsafe.Pointer(&r)), unsafe.Sizeof(r))
	return r, hr == 0
}

// w32Cloaked reports whether DWM has hidden the window: other virtual
// desktops, suspended UWP apps, and app-frame ghosts all sit "visible" in the
// classic sense but cloaked — listing them would offer windows that capture
// black. DWM unavailable → treat as not cloaked.
func w32Cloaked(hwnd uintptr) bool {
	if w32ProcDwmGetWindowAttribute.Find() != nil {
		return false
	}
	var v uint32
	hr, _, _ := w32ProcDwmGetWindowAttribute.Call(hwnd, w32DWMWACloaked,
		uintptr(unsafe.Pointer(&v)), unsafe.Sizeof(v))
	return hr == 0 && v != 0
}

// w32ProcessName returns the executable base name (without .exe) for a pid.
func w32ProcessName(pid uint32) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	var buf [windows.MAX_PATH]uint16
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		return ""
	}
	name := filepath.Base(windows.UTF16ToString(buf[:n]))
	if strings.EqualFold(filepath.Ext(name), ".exe") {
		name = name[:len(name)-4]
	}
	return name
}

func (win32Windows) list() ([]winInfo, error) {
	var wins []winInfo
	for _, hwnd := range w32TopLevelWindows() {
		if r, _, _ := w32ProcIsWindowVisible.Call(hwnd); r == 0 {
			continue
		}
		if r, _, _ := w32ProcIsIconic.Call(hwnd); r != 0 {
			continue // minimized windows park at (-32000,-32000) and capture blank
		}
		if w32Cloaked(hwnd) {
			continue
		}
		if ex, _, _ := w32ProcGetWindowLongW.Call(hwnd, w32GWLExstyle); uint32(ex)&w32WSExToolWindow != 0 {
			continue
		}
		title := w32WindowText(hwnd)
		if strings.TrimSpace(title) == "" {
			continue
		}
		rect, ok := w32FrameBounds(hwnd)
		if !ok {
			if rect, ok = w32WindowRect(hwnd); !ok {
				continue
			}
		}
		w, h := int(rect.Right-rect.Left), int(rect.Bottom-rect.Top)
		if w < 40 || h < 40 {
			continue
		}
		var pid uint32
		_, _, _ = w32ProcGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
		app := w32ProcessName(pid)
		if app == "" {
			app = "?"
		}
		wins = append(wins, winInfo{
			ID:  "hwnd:" + strconv.FormatUint(uint64(hwnd), 10),
			App: app, Title: title,
			X: int(rect.Left), Y: int(rect.Top), W: w, H: h,
			PID: int(pid),
			// CropL/CropT stay 0: the DWM frame bounds already exclude the
			// invisible border, and capture() crops to that same rect itself.
		})
	}
	// Whole desktops ride the same list as pseudo-windows ("display:<n>", App
	// "Desktop"), exactly like the macOS backend, so a viewer can mirror an
	// entire monitor. Rects are virtual-screen coordinates — the same space as
	// the window rects above, so input mapping works unchanged.
	mons := w32Monitors()
	for i, m := range mons {
		w, h := int(m.Right-m.Left), int(m.Bottom-m.Top)
		label := fmt.Sprintf("Entire screen — %d×%d", w, h)
		if len(mons) > 1 {
			label = fmt.Sprintf("Display %d — %d×%d", i+1, w, h)
		}
		wins = append(wins, winInfo{
			ID: "display:" + strconv.Itoa(i+1), App: "Desktop", Title: label,
			X: int(m.Left), Y: int(m.Top), W: w, H: h,
		})
	}
	return wins, nil
}

func (win32Windows) exists(id string) bool {
	if isDisplayID(id) {
		// Displays are always "open"; an unplugged monitor drops out of list(),
		// which the stream's geometry poll already turns into a closed pane.
		return true
	}
	hwnd, err := w32ParseHWND(id)
	if err != nil {
		return true // contract: never close a pane over a transient failure
	}
	r, _, _ := w32ProcIsWindow.Call(hwnd)
	return r != 0
}

// ---- focus ------------------------------------------------------------------

func (win32Windows) focus(w winInfo) error {
	if isDisplayID(w.ID) {
		return nil // a desktop has no window to raise; clicks land wherever aimed
	}
	hwnd, err := w32ParseHWND(w.ID)
	if err != nil {
		return err
	}
	if r, _, _ := w32ProcIsIconic.Call(hwnd); r != 0 {
		_, _, _ = w32ProcShowWindow.Call(hwnd, w32SWRestore)
	}
	if fg, _, _ := w32ProcGetForegroundWindow.Call(); fg == hwnd {
		return nil // already frontmost — keep per-keystroke focus calls cheap
	}
	// Windows refuses SetForegroundWindow from a background process (the
	// foreground-lock rules: only the thread that owns the current foreground
	// window, or one that recently received input, may steal focus). Two
	// standard escapes, both used here:
	//  1. AttachThreadInput to the target window's thread, which shares its
	//     input-state (and its right to the foreground) with ours.
	//  2. If that still didn't take, inject a no-op ALT press+release via
	//     SendInput — the injected input marks our process as "the user's
	//     last-input process", which satisfies the lock — then retry. The ALT
	//     tap happens while focus is elsewhere/moving, so it doesn't trigger
	//     the target's menu-bar activation.
	// AttachThreadInput must attach and detach from the SAME OS thread, and Go
	// migrates goroutines between threads at will — so pin the thread for the
	// duration of the bracket.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var pid uint32
	tid, _, _ := w32ProcGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	cur := uintptr(windows.GetCurrentThreadId())
	attached := false
	if tid != 0 && tid != cur {
		if r, _, _ := w32ProcAttachThreadInput.Call(tid, cur, 1); r != 0 {
			attached = true
		}
	}
	_, _, _ = w32ProcSetForegroundWindow.Call(hwnd)
	_, _, _ = w32ProcBringWindowToTop.Call(hwnd)
	if fg, _, _ := w32ProcGetForegroundWindow.Call(); fg != hwnd {
		_ = w32SendInputs([]w32Input{
			w32KeyInput(w32VKMenu, 0, 0),
			w32KeyInput(w32VKMenu, 0, w32KeyEventFKeyUp),
		})
		_, _, _ = w32ProcSetForegroundWindow.Call(hwnd)
		_, _, _ = w32ProcBringWindowToTop.Call(hwnd)
	}
	if attached {
		_, _, _ = w32ProcAttachThreadInput.Call(tid, cur, 0)
	}
	return nil
}

// ---- app launcher -----------------------------------------------------------

// w32StartMenuRoots returns the per-user and all-users Start Menu program
// folders — the canonical "installed applications" set on Windows.
func w32StartMenuRoots() []string {
	var roots []string
	if d := os.Getenv("APPDATA"); d != "" {
		roots = append(roots, filepath.Join(d, "Microsoft", "Windows", "Start Menu", "Programs"))
	}
	pd := os.Getenv("ProgramData")
	if pd == "" {
		pd = `C:\ProgramData`
	}
	return append(roots, filepath.Join(pd, "Microsoft", "Windows", "Start Menu", "Programs"))
}

func (win32Windows) listApps() ([]appInfo, error) {
	seen := map[string]bool{}
	var apps []appInfo
	for _, root := range w32StartMenuRoots() {
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // unreadable subfolder — skip, keep walking
			}
			if d.IsDir() || !strings.EqualFold(filepath.Ext(path), ".lnk") {
				return nil
			}
			name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
			key := strings.ToLower(name)
			if name == "" || seen[key] {
				return nil // dedupe by name; per-user copy shadows the system one
			}
			seen[key] = true
			apps = append(apps, appInfo{ID: path, Name: name})
			return nil
		})
	}
	sort.Slice(apps, func(i, j int) bool {
		return strings.ToLower(apps[i].Name) < strings.ToLower(apps[j].Name)
	})
	return apps, nil
}

func (win32Windows) openApp(id string) error {
	// The viewer echoes ids back, so validate strictly before launching
	// anything: must be an existing .lnk inside one of the Start Menu roots we
	// enumerate from — never an arbitrary attacker-controlled path/command.
	clean := filepath.Clean(id)
	if !strings.EqualFold(filepath.Ext(clean), ".lnk") {
		return fmt.Errorf("not a Start Menu shortcut")
	}
	inRoot := false
	for _, root := range w32StartMenuRoots() {
		rel, err := filepath.Rel(root, clean)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			inRoot = true
			break
		}
	}
	if !inRoot {
		return fmt.Errorf("app id outside the Start Menu")
	}
	if fi, err := os.Stat(clean); err != nil || fi.IsDir() {
		return fmt.Errorf("no such app")
	}
	// ShellExecute "open" resolves the .lnk like a double-click and launches
	// the target detached — no child process to reap, and the winOps worker
	// never waits on the GUI app.
	verb, _ := windows.UTF16PtrFromString("open")
	file, err := windows.UTF16PtrFromString(clean)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
}
