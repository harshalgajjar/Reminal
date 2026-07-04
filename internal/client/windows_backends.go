// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// This file holds the concrete windowBackend implementations. They only shell
// out (no cgo, no OS-specific imports), so every backend compiles on every
// platform; newWindowBackend picks the right one at runtime. A macOS binary
// links linuxWindows too but never calls it, and vice versa.

// ---- macOS: osascript + screencapture --------------------------------------

type darwinWindows struct{}

func (darwinWindows) unsupported() string {
	if !have("osascript") || !have("screencapture") {
		return "osascript/screencapture not found — is this macOS?"
	}
	return ""
}

// permissionHint asks CoreGraphics whether this process holds Screen Recording
// permission. Without it, macOS still lets us enumerate windows but hands back
// black/empty captures — so we warn instead of showing silent blank panes.
// CGPreflightScreenCaptureAccess doesn't prompt, so it's safe to call often.
func (darwinWindows) permissionHint() string {
	out, err := run("osascript", "-l", "JavaScript", "-e",
		`ObjC.import('CoreGraphics'); $.CGPreflightScreenCaptureAccess() ? 'ok' : 'no'`)
	if err == nil && strings.TrimSpace(out) == "no" {
		return "Screen Recording is off, so windows list but can't be captured. " +
			"Enable it for your terminal in System Settings ▸ Privacy & Security ▸ " +
			"Screen Recording, then reconnect."
	}
	return ""
}

// jxaListScript enumerates on-screen windows via CoreGraphics
// (CGWindowListCopyWindowInfo) through JavaScript-for-Automation's ObjC
// bridge. This is ~50-100× faster than driving System Events (a fast syscall
// vs. per-window Apple Events), and it yields the real kCGWindowNumber for
// each window — which lets screencapture grab the window by id even when it's
// occluded, and requires no Accessibility permission (only Screen Recording,
// which capture needs anyway). Emits tab-separated
// "id<TAB>owner<TAB>title<TAB>x<TAB>y<TAB>w<TAB>h" lines. Layer 0 filters to
// normal application windows (skips the menu bar, Dock, shadows, etc.).
const jxaListScript = `ObjC.import("CoreGraphics"); ObjC.import("Foundation");
var arr = ObjC.castRefToObject($.CGWindowListCopyWindowInfo($.kCGWindowListOptionOnScreenOnly | $.kCGWindowListExcludeDesktopElements, $.kCGNullWindowID));
var n = arr.count, out = [];
for (var i = 0; i < n; i++) {
  var w = arr.objectAtIndex(i);
  if (w.objectForKey("kCGWindowLayer").intValue !== 0) continue;
  var owner = w.objectForKey("kCGWindowOwnerName");
  var name = w.objectForKey("kCGWindowName");
  var num = w.objectForKey("kCGWindowNumber").intValue;
  var pid = w.objectForKey("kCGWindowOwnerPID").intValue;
  var b = w.objectForKey("kCGWindowBounds");
  out.push([num, pid, owner ? ObjC.unwrap(owner) : "", name ? ObjC.unwrap(name) : "",
    b.objectForKey("X").intValue, b.objectForKey("Y").intValue,
    b.objectForKey("Width").intValue, b.objectForKey("Height").intValue].join("\t"));
}
out.join("\n");`

func (darwinWindows) list() ([]winInfo, error) {
	out, err := run("osascript", "-l", "JavaScript", "-e", jxaListScript)
	if err != nil {
		return nil, err
	}
	var wins []winInfo
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 8 {
			continue
		}
		// Title may itself contain tabs; the numeric fields are the last four.
		n := len(f)
		id, pid, owner := f[0], atoi(f[1]), f[2]
		title := strings.Join(f[3:n-4], "\t")
		x, y := atoi(f[n-4]), atoi(f[n-3])
		w, h := atoi(f[n-2]), atoi(f[n-1])
		if w < 40 || h < 40 {
			continue
		}
		wins = append(wins, winInfo{ID: id, PID: pid, App: owner, Title: title, X: x, Y: y, W: w, H: h})
	}
	return wins, nil
}

// winMaxWidth is the width we downscale captured frames to. Retina windows
// capture at 2× (a 1728px window → 3456px, ~1.3 MB JPEG), which blows past the
// relay's 1 MiB WS frame cap. 1100px keeps frames ~55 KB — small enough to
// stream at a higher frame rate (less to encode and send) while still looking
// sharp scaled into a pane, and it leaves headroom for pinch-zoom.
const winMaxWidth = 1100

func (darwinWindows) capture(w winInfo) ([]byte, error) {
	// -l<id>: capture exactly this window (even if occluded), -o: no drop shadow.
	return screencaptureJPEG("-o", "-l"+w.ID)
}

func (darwinWindows) captureRegion(x, y, w, h int) ([]byte, error) {
	// -R<x,y,w,h>: capture this screen rectangle, so a context menu drawn over
	// the window (its own OS window) lands in the same frame. No -o: that's a
	// window-shadow flag and doesn't apply to a rect.
	return screencaptureJPEG(fmt.Sprintf("-R%d,%d,%d,%d", x, y, w, h))
}

// screencaptureJPEG runs `screencapture -x -t jpg <target...> raw`, then
// downscales + recompresses it under the relay's frame cap. target is the
// window/region selector (-l<id> or -R<rect>) plus any extra flags.
func screencaptureJPEG(target ...string) ([]byte, error) {
	raw, err := tmpImage("jpg")
	if err != nil {
		return nil, err
	}
	defer os.Remove(raw)
	args := append([]string{"-x", "-t", "jpg"}, target...)
	args = append(args, raw)
	if _, err := run("screencapture", args...); err != nil {
		return nil, err
	}
	small, err := tmpImage("jpg")
	if err != nil {
		return nil, err
	}
	defer os.Remove(small)
	// Downscale + recompress so a frame fits under the relay's frame cap.
	if _, err := run("sips", "-Z", strconv.Itoa(winMaxWidth), "-s", "format", "jpeg",
		"-s", "formatOptions", "45", raw, "--out", small); err != nil {
		// sips failed — fall back to the full-res capture rather than nothing.
		return os.ReadFile(raw)
	}
	return os.ReadFile(small)
}

func (darwinWindows) listApps() ([]appInfo, error) {
	// Scan the standard app folders, plus one level into subfolders (some apps
	// live in e.g. /Applications/Adobe.../Foo.app). ID is the bundle path — an
	// unambiguous handle for `open`; Name is the display label. Dedupe by name.
	dirs := []string{"/Applications", "/System/Applications", "/System/Applications/Utilities"}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, "Applications"))
	}
	seen := map[string]bool{}
	var apps []appInfo
	add := func(dir, name string) {
		if !strings.HasSuffix(name, ".app") {
			return
		}
		disp := strings.TrimSuffix(name, ".app")
		if seen[disp] {
			return
		}
		seen[disp] = true
		apps = append(apps, appInfo{ID: filepath.Join(dir, name), Name: disp})
	}
	for _, d := range dirs {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".app") {
				add(d, e.Name())
			} else if e.IsDir() {
				// One level deep — catches vendor-subfoldered apps without a slow
				// full-disk walk.
				sub := filepath.Join(d, e.Name())
				if subEntries, err := os.ReadDir(sub); err == nil {
					for _, se := range subEntries {
						add(sub, se.Name())
					}
				}
			}
		}
	}
	sort.Slice(apps, func(i, j int) bool {
		return strings.ToLower(apps[i].Name) < strings.ToLower(apps[j].Name)
	})
	return apps, nil
}

func (darwinWindows) openApp(id string) error {
	// `open <bundle path>` launches the app, or foregrounds it (and opens a
	// window if it has none) when already running.
	_, err := run("open", id)
	return err
}

func (darwinWindows) focus(w winInfo) error {
	// Raise the SPECIFIC window, not just the app. Merely activating the app
	// (the old NSRunningApplication approach) leaves the target unfocused
	// whenever the app has several windows or the window sits behind a
	// different app — so clicks/keys silently miss it. "set frontmost" activates
	// the app and switches to its Space; AXRaise then brings the one window
	// forward.
	//
	// Match that window by POSITION, not title: two windows of the same app
	// (e.g. two Chrome windows) often share a title, so "first window whose name
	// is X" would raise the wrong one and clicks would land on it. The AX window
	// position lines up with the CGWindowList bounds we enumerated, so we pick
	// the window whose top-left is closest to this pane's origin, and only fall
	// back to a title match if none is close.
	// Pick the exact window by scoring each of the app's windows on BOTH title
	// and geometry, then raise the best. Neither alone is enough: two Chrome
	// windows maximized to the same rect share position+size (so geometry can't
	// separate them — title can), while a window with a live-updating title like
	// a clock may no longer match the title we enumerated (so title can't — its
	// unique position can). A title match dominates (big score bonus); geometry
	// distance breaks ties and carries when the title has drifted. If no window
	// is readable we leave the app merely activated.
	//
	// The title test is CONTAINS, not equals: the name we enumerate comes from
	// CGWindowList (the page title, e.g. "Saba… - The Knot") while AX reports the
	// full title-bar text ("Saba… - The Knot - Part of group X - Google Chrome").
	// Exact equality would never match, so two same-size Chrome windows couldn't
	// be told apart and clicks landed on the wrong one.
	//
	// Target the process by its unix id (PID), NOT by name. Several distinct
	// processes can share a name — e.g. Chrome profiles and installed PWAs all
	// run as "Google Chrome" under different PIDs — and `tell process "Google
	// Chrome"` binds to just one of them (whichever System Events finds first).
	// Windows owned by the other same-named processes would then be invisible to
	// this AX query, so focus() could never raise them and their clicks silently
	// landed on whatever sat at those screen coords. w.PID comes from the same
	// CGWindowList enumeration as the geometry, so it always matches this window.
	script := fmt.Sprintf(`tell application "System Events" to tell (first process whose unix id is %d)
	set frontmost to true
	try
		set bestWin to missing value
		set bestScore to 1.0E+12
		repeat with win in windows
			try
				set p to position of win
				set sz to size of win
				set dx to ((item 1 of p) - %d)
				set dy to ((item 2 of p) - %d)
				set dw to ((item 1 of sz) - %d)
				set dh to ((item 2 of sz) - %d)
				set sc to (dx * dx + dy * dy) + (dw * dw + dh * dh)
				if %s is not "" and (name of win) contains %s then set sc to sc - 100000000
				if sc < bestScore then
					set bestScore to sc
					set bestWin to win
				end if
			end try
		end repeat
		if bestWin is not missing value then perform action "AXRaise" of bestWin
	end try
end tell`, w.PID, w.X, w.Y, w.W, w.H, asStr(w.Title), asStr(w.Title))
	_, err := run("osascript", "-e", script)
	return err
}

// jxaEvents runs a JXA snippet with CoreGraphics/Foundation imported. Input
// injection uses Quartz CGEvents (real HID-level events), which — unlike System
// Events' "click at" / "keystroke" — post reliably as long as the process has
// Accessibility permission. Same no-cgo, no-dependency approach as enumeration.
func jxaEvents(body string) error {
	// A real HID event source delivers to apps more reliably than a null
	// source. NOTE: synthetic clicks/keys only reach apps when the Mac is
	// UNLOCKED — macOS routes everything to loginwindow behind the lock
	// screen (window capture still works, which is why viewing looks live
	// but control does nothing on a locked Mac).
	_, err := run("osascript", "-l", "JavaScript", "-e",
		`ObjC.import("CoreGraphics");ObjC.import("Foundation");var src=$.CGEventSourceCreate($.kCGEventSourceStateHIDSystemState);`+body)
	return err
}

func (darwinWindows) clickN(w winInfo, fx, fy float64, count int, right bool) error {
	if count < 1 {
		count = 1
	}
	x := w.X + int(fx*float64(w.W))
	y := w.Y + int(fy*float64(w.H))
	btn, down, up := "kCGMouseButtonLeft", "kCGEventLeftMouseDown", "kCGEventLeftMouseUp"
	if right {
		btn, down, up = "kCGMouseButtonRight", "kCGEventRightMouseDown", "kCGEventRightMouseUp"
	}
	// Move, then press/release at absolute screen points. The down/up MUST
	// carry a click-state and be separated by a brief gap — without those,
	// many apps see the events as mere cursor movement and never register a
	// click. click-state = count gives native single/double/triple clicks: the
	// viewer sends count 2 on the second tap so the OS pairs them into a
	// double-click regardless of network jitter (the field is authoritative).
	body := fmt.Sprintf(`
var p=$.CGPointMake(%d,%d);
function e(t){var ev=$.CGEventCreateMouseEvent(src,t,p,$.%s);$.CGEventSetIntegerValueField(ev,$.kCGMouseEventClickState,%d);$.CGEventPost($.kCGHIDEventTap,ev);}
e($.kCGEventMouseMoved);
e($.%s);
$.NSThread.sleepForTimeInterval(0.03);
e($.%s);`, x, y, btn, count, down, up)
	return jxaEvents(body)
}

func (darwinWindows) drag(w winInfo, pts [][2]float64) error {
	if len(pts) == 0 {
		return nil
	}
	var sb strings.Builder
	for i, p := range pts {
		if i > 0 {
			sb.WriteByte(',')
		}
		x := w.X + int(p[0]*float64(w.W))
		y := w.Y + int(p[1]*float64(w.H))
		fmt.Fprintf(&sb, "[%d,%d]", x, y)
	}
	// Press at the first point, drag through the rest with small gaps so apps
	// track the motion (text selection, sliders), release at the last.
	body := fmt.Sprintf(`
var pts=[%s];
function ev(t,x,y){var e=$.CGEventCreateMouseEvent(src,t,$.CGPointMake(x,y),$.kCGMouseButtonLeft);$.CGEventPost($.kCGHIDEventTap,e);}
ev($.kCGEventMouseMoved,pts[0][0],pts[0][1]);
ev($.kCGEventLeftMouseDown,pts[0][0],pts[0][1]);
$.NSThread.sleepForTimeInterval(0.05);
for(var i=1;i<pts.length;i++){ev($.kCGEventLeftMouseDragged,pts[i][0],pts[i][1]);$.NSThread.sleepForTimeInterval(0.008);}
$.NSThread.sleepForTimeInterval(0.05);
ev($.kCGEventLeftMouseUp,pts[pts.length-1][0],pts[pts.length-1][1]);`, sb.String())
	return jxaEvents(body)
}

func (darwinWindows) scroll(w winInfo, fx, fy, dx, dy float64) error {
	x := w.X + int(fx*float64(w.W))
	y := w.Y + int(fy*float64(w.H))
	// Scroll wheel events land on the window under the pointer, so move there
	// first. CGEvent wheel is inverted vs the web/DOM convention (positive =
	// up), so negate: positive dy (scroll down) → negative wheel1. Pixel units
	// give smooth, 1:1 scrolling.
	w1, w2 := -int(dy), -int(dx)
	body := fmt.Sprintf(`
var mv=$.CGEventCreateMouseEvent(src,$.kCGEventMouseMoved,$.CGPointMake(%d,%d),$.kCGMouseButtonLeft);$.CGEventPost($.kCGHIDEventTap,mv);
var sc=$.CGEventCreateScrollWheelEvent2(src,$.kCGScrollEventUnitPixel,2,%d,%d,0);$.CGEventPost($.kCGHIDEventTap,sc);`, x, y, w1, w2)
	return jxaEvents(body)
}

func (darwinWindows) typeText(w winInfo, text string) error {
	// System Events "keystroke" types arbitrary text reliably (layout-aware),
	// where the CGEvent unicode path doesn't marshal correctly from JXA. asStr
	// escapes the text into an AppleScript string literal so viewer input can't
	// break out of the script.
	script := fmt.Sprintf(`tell application "System Events" to keystroke %s`, asStr(text))
	_, err := run("osascript", "-e", script)
	return err
}

// darwinKeyCodes maps our neutral special-key names to macOS virtual key codes.
var darwinKeyCodes = map[string]int{
	"return": 36, "enter": 36, "tab": 48, "space": 49,
	"delete": 51, "forwarddelete": 117, "escape": 53,
	"left": 123, "right": 124, "down": 125, "up": 126,
	"home": 115, "end": 119, "pageup": 116, "pagedown": 121,
}

func (darwinWindows) key(w winInfo, name string) error {
	code, ok := darwinKeyCodes[strings.ToLower(name)]
	if !ok {
		return fmt.Errorf("unknown key %q", name)
	}
	// System Events "key code" for named keys — proven reliable, same path as
	// keystroke. (CGEvent works too, but there's no reason to mix mechanisms.)
	script := fmt.Sprintf(`tell application "System Events" to key code %d`, code)
	_, err := run("osascript", "-e", script)
	return err
}

func (darwinWindows) exists(id string) bool {
	// Check the FULL window list (all Spaces, minimized included) so we only
	// report "gone" when the window is truly closed. id is a CGWindowNumber we
	// produced, so atoi is safe and keeps it out of the script as a bare int.
	script := fmt.Sprintf(`ObjC.import("CoreGraphics");ObjC.import("Foundation");
var a=ObjC.castRefToObject($.CGWindowListCopyWindowInfo($.kCGWindowListOptionAll,$.kCGNullWindowID));
var t=%d,found=false;for(var i=0;i<a.count;i++){if(a.objectAtIndex(i).objectForKey("kCGWindowNumber").intValue===t){found=true;break;}}
found?"1":"0";`, atoi(id))
	out, err := run("osascript", "-l", "JavaScript", "-e", script)
	if err != nil {
		return true // don't close a pane over a transient failure
	}
	return strings.TrimSpace(out) == "1"
}

func (darwinWindows) releaseInput() error {
	// Post button-up for every mouse button at the current cursor location, so
	// a stranded press can't leave the desktop grabbed. (Modifier keys rarely
	// stick on macOS since keystrokes are atomic.)
	body := `
var loc = $.CGEventGetLocation($.CGEventCreate($()));
[$.kCGEventLeftMouseUp, $.kCGEventRightMouseUp, $.kCGEventOtherMouseUp].forEach(function(t){
  $.CGEventPost($.kCGHIDEventTap, $.CGEventCreateMouseEvent(src, t, loc, $.kCGMouseButtonLeft));
});`
	return jxaEvents(body)
}

// asStr renders s as an AppleScript string literal (quoted, with backslash and
// quote escaped) so viewer-supplied text can't break out of a script.
func asStr(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

// ---- Linux/X11: wmctrl + xdotool + ImageMagick -----------------------------
//
// Best-effort and currently untested on real hardware (developed on macOS).
// Works on X11 sessions with wmctrl (enumerate/focus), ImageMagick `import`
// (capture), and xdotool (input). Wayland blocks synthetic input and
// cross-window capture for these tools, so it reports unsupported there.

type linuxWindows struct{}

func (linuxWindows) unsupported() string {
	if os.Getenv("WAYLAND_DISPLAY") != "" && os.Getenv("DISPLAY") == "" {
		return "Wayland isn't supported yet — X11 (or Xwayland) required for window mirroring"
	}
	if !have("wmctrl") {
		return "install wmctrl (and xdotool + imagemagick) to mirror windows on Linux"
	}
	return ""
}

// permissionHint has no analogue on X11 — there's no per-app screen-capture
// permission, so if the tools are present, capture works.
func (linuxWindows) permissionHint() string { return "" }

func (linuxWindows) list() ([]winInfo, error) {
	// -l list, -G geometry, -x include WM_CLASS. Columns:
	//   id desktop wm_class x y w h host title...
	out, err := run("wmctrl", "-lGx")
	if err != nil {
		return nil, err
	}
	var wins []winInfo
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		// `wmctrl -lGx` columns: id desktop x y w h wm_class host title...
		// We use wmctrl only for id / class / title — its -G geometry is
		// unreliable on GNOME/Mutter (it double-counts the frame offset, e.g.
		// reports 2× the real x/y), which made clicks land outside the window.
		// The real absolute geometry comes from xdotool below.
		f := strings.Fields(line)
		if len(f) < 9 {
			continue
		}
		if f[1] == "-1" { // sticky panels / docks — not real windows
			continue
		}
		id := f[0]
		app := f[6] // WM_CLASS "instance.Class" — keep the class
		if dot := strings.LastIndexByte(app, '.'); dot >= 0 {
			app = app[dot+1:]
		}
		title := strings.Join(f[8:], " ")
		x, y, w, h := linuxGeom(id)
		if w < 40 || h < 40 {
			continue
		}
		wi := winInfo{ID: id, App: app, Title: title, X: x, Y: y, W: w, H: h}
		// GTK client-side-decoration windows (Firefox, GNOME apps) pad their
		// geometry with an invisible shadow margin. `import -window` keeps the
		// top-left shadow in the captured image (and trims the bottom-right), so
		// the frame shows a black border and the visible content is inset. Expose
		// the pure content rect and record the top-left shadow so capture() can
		// crop it off — that removes the border and makes clicks line up 1:1 with
		// what the viewer sees (e.g. Firefox's tiny tab-close buttons).
		if l, r, t, b, ok := gtkFrameExtents(id); ok && w > l+r && h > t+b {
			wi.X, wi.Y = x+l, y+t
			wi.W, wi.H = w-l-r, h-t-b
			wi.CropL, wi.CropT = l, t
		}
		wins = append(wins, wi)
	}
	return wins, nil
}

// linuxGeom returns a window's true absolute geometry via xdotool, which
// (unlike wmctrl -G) reports coordinates that match where the window actually
// is on screen — so clicks land where they should. Returns zeros on error, so
// the caller's w/h < 40 check drops the window.
func linuxGeom(id string) (x, y, w, h int) {
	out, err := run("xdotool", "getwindowgeometry", "--shell", id)
	if err != nil {
		return 0, 0, 0, 0
	}
	for _, line := range strings.Split(out, "\n") {
		kv := strings.SplitN(strings.TrimSpace(line), "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "X":
			x = atoi(kv[1])
		case "Y":
			y = atoi(kv[1])
		case "WIDTH":
			w = atoi(kv[1])
		case "HEIGHT":
			h = atoi(kv[1])
		}
	}
	return x, y, w, h
}

// gtkFrameExtents reads _GTK_FRAME_EXTENTS (left, right, top, bottom) — the
// invisible CSD margins GTK draws for its drop shadow and resize grips. ok is
// false when the property is absent (non-CSD windows, or no xprop), meaning
// there's nothing to adjust.
func gtkFrameExtents(id string) (left, right, top, bottom int, ok bool) {
	out, err := run("xprop", "-id", id, "_GTK_FRAME_EXTENTS")
	if err != nil {
		return 0, 0, 0, 0, false
	}
	eq := strings.IndexByte(out, '=')
	if eq < 0 { // "_GTK_FRAME_EXTENTS:  not found."
		return 0, 0, 0, 0, false
	}
	parts := strings.Split(out[eq+1:], ",")
	if len(parts) != 4 {
		return 0, 0, 0, 0, false
	}
	return atoi(strings.TrimSpace(parts[0])), atoi(strings.TrimSpace(parts[1])),
		atoi(strings.TrimSpace(parts[2])), atoi(strings.TrimSpace(parts[3])), true
}

func (linuxWindows) capture(w winInfo) ([]byte, error) {
	if !have("import") {
		return nil, fmt.Errorf("install imagemagick (provides `import`) to capture windows")
	}
	// import writes JPEG to stdout; -window takes the hex window id.
	args := []string{"-window", w.ID}
	// Crop off the invisible top-left CSD shadow (see list()) so the frame is
	// pure window content — no black border, and pixels line up 1:1 with the
	// content rect we map clicks against. w.W×w.H is the content size; the shadow
	// sits at offset (CropL, CropT) in import's output.
	if w.CropL > 0 || w.CropT > 0 {
		args = append(args, "-crop",
			fmt.Sprintf("%dx%d+%d+%d", w.W, w.H, w.CropL, w.CropT), "+repage")
	}
	// Downscale to winMaxWidth and recompress (like the macOS sips step) so a
	// frame is ~50 KB instead of the ~300-500 KB a full-res 1332px window
	// produces — full-res frames are slow to encode, transfer, and decode, which
	// tanks the effective frame rate (and can blow past the relay's 1 MiB WS
	// frame cap). "NxN>" fits the window inside that box, only shrinking, so
	// smaller windows stay crisp.
	box := strconv.Itoa(winMaxWidth) + "x" + strconv.Itoa(winMaxWidth) + ">"
	args = append(args, "-resize", box, "-quality", "55", "jpg:-")
	return runRaw("import", args...)
}

func (linuxWindows) captureRegion(x, y, w, h int) ([]byte, error) {
	if !have("import") {
		return nil, fmt.Errorf("install imagemagick (provides `import`) to capture windows")
	}
	// Grab a rectangle of the root window, so an override-redirect menu popup
	// drawn over the target is composited into the same frame. Downscale to match
	// capture()'s framing.
	box := strconv.Itoa(winMaxWidth) + "x" + strconv.Itoa(winMaxWidth) + ">"
	return runRaw("import", "-window", "root",
		"-crop", fmt.Sprintf("%dx%d+%d+%d", w, h, x, y), "+repage",
		"-resize", box, "-quality", "55", "jpg:-")
}

func (linuxWindows) focus(w winInfo) error {
	_, err := run("wmctrl", "-i", "-a", w.ID)
	return err
}

func (linuxWindows) listApps() ([]appInfo, error) {
	// Freedesktop .desktop entries across the standard data dirs (incl. flatpak
	// and snap). ID is the .desktop path; Name is its display label. Skip
	// NoDisplay/Hidden and non-Application entries. Dedupe by desktop-file name
	// so a user override shadows the system copy.
	dirs := []string{
		"/usr/share/applications", "/usr/local/share/applications",
		"/var/lib/flatpak/exports/share/applications",
		"/var/lib/snapd/desktop/applications",
	}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs,
			filepath.Join(home, ".local/share/applications"),
			filepath.Join(home, ".local/share/flatpak/exports/share/applications"))
	}
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		dirs = append(dirs, filepath.Join(xdg, "applications"))
	}
	seen := map[string]bool{}
	var apps []appInfo
	for _, d := range dirs {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".desktop") || seen[e.Name()] {
				continue
			}
			path := filepath.Join(d, e.Name())
			de := parseDesktopEntry(path)
			if t := de["Type"]; t != "" && t != "Application" {
				continue
			}
			if strings.EqualFold(de["NoDisplay"], "true") || strings.EqualFold(de["Hidden"], "true") {
				continue
			}
			name := de["Name"]
			if name == "" {
				continue
			}
			seen[e.Name()] = true
			apps = append(apps, appInfo{ID: path, Name: name})
		}
	}
	sort.Slice(apps, func(i, j int) bool {
		return strings.ToLower(apps[i].Name) < strings.ToLower(apps[j].Name)
	})
	return apps, nil
}

func (linuxWindows) openApp(id string) error {
	// Prefer gtk-launch (honours the .desktop's StartupNotify, Terminal, etc.);
	// fall back to running the entry's Exec line detached. Either way we don't
	// wait on the GUI app, which would block the winOps worker for its lifetime.
	if have("gtk-launch") {
		base := strings.TrimSuffix(filepath.Base(id), ".desktop")
		if err := launchDetached("gtk-launch", base); err == nil {
			return nil
		}
	}
	args := desktopExecArgs(parseDesktopEntry(id)["Exec"])
	if len(args) == 0 {
		return fmt.Errorf("no launchable Exec in %s", id)
	}
	return launchDetached(args[0], args[1:]...)
}

// parseDesktopEntry reads the [Desktop Entry] group of a freedesktop .desktop
// file into a key→value map (first value wins; localized keys like Name[de] are
// skipped so the default Name is used).
func parseDesktopEntry(path string) map[string]string {
	m := map[string]string{}
	data, err := os.ReadFile(path)
	if err != nil {
		return m
	}
	inEntry := false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			inEntry = line == "[Desktop Entry]"
			continue
		}
		if !inEntry || line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		if strings.ContainsRune(key, '[') { // localized variant, e.g. Name[de]
			continue
		}
		if _, ok := m[key]; !ok {
			m[key] = strings.TrimSpace(line[eq+1:])
		}
	}
	return m
}

// desktopExecArgs turns a .desktop Exec line into argv, dropping the field
// codes (%f %F %u %U %i %c %k …) since we launch with no document argument.
func desktopExecArgs(execLine string) []string {
	if execLine == "" {
		return nil
	}
	var out []string
	for _, f := range strings.Fields(execLine) {
		if len(f) == 2 && f[0] == '%' { // %u, %F, etc.
			continue
		}
		out = append(out, f)
	}
	return out
}

// launchDetached starts a GUI program without waiting for it (a GUI app runs
// for as long as the user keeps it open; waiting would pin the winOps worker).
// A background reap avoids leaving a zombie when it eventually exits.
func launchDetached(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func (linuxWindows) clickN(w winInfo, fx, fy float64, count int, right bool) error {
	if !have("xdotool") {
		return fmt.Errorf("install xdotool for input control")
	}
	absX := w.X + int(fx*float64(w.W))
	absY := w.Y + int(fy*float64(w.H))
	btn := "1"
	if right {
		btn = "3" // X11 secondary/context button
	}
	// X11 has no click-state field, so it coalesces rapid successive clicks into
	// double/triple by timing itself: each viewer tap is one click here (unlike
	// macOS, where count is authoritative). Right-click uses button 3.
	_, err := run("xdotool", "mousemove", fmt.Sprintf("%d", absX), fmt.Sprintf("%d", absY), "click", btn)
	return err
}

func (linuxWindows) drag(w winInfo, pts [][2]float64) error {
	if !have("xdotool") {
		return fmt.Errorf("install xdotool for input control")
	}
	if len(pts) == 0 {
		return nil
	}
	// One xdotool invocation chains the whole gesture with small holds so apps
	// register a real press-and-move (text selection, sliders, drag-and-drop)
	// instead of collapsing an instantaneous press+release into a click. --sync
	// makes each move wait until the pointer actually arrives before the next
	// step, so the motion isn't coalesced away.
	var args []string
	for i, p := range pts {
		x := w.X + int(p[0]*float64(w.W))
		y := w.Y + int(p[1]*float64(w.H))
		args = append(args, "mousemove", "--sync", fmt.Sprintf("%d", x), fmt.Sprintf("%d", y))
		if i == 0 {
			// Press, then hold briefly before the first move so the app sees a
			// grab starting at the origin.
			args = append(args, "mousedown", "1", "sleep", "0.06")
		} else {
			args = append(args, "sleep", "0.012")
		}
	}
	args = append(args, "sleep", "0.06", "mouseup", "1")
	_, err := run("xdotool", args...)
	return err
}

func (linuxWindows) scroll(w winInfo, fx, fy, dx, dy float64) error {
	if !have("xdotool") {
		return fmt.Errorf("install xdotool for input control")
	}
	x := w.X + int(fx*float64(w.W))
	y := w.Y + int(fy*float64(w.H))
	// X11 has no smooth scroll: wheel is buttons 4 (up) / 5 (down) / 6 (left) /
	// 7 (right), one notch per click. Convert the pixel-ish delta to notches
	// (~40px each), capped so a fast flick can't fire a huge burst.
	notches := func(d float64) int {
		n := int(d)
		if n < 0 {
			n = -n
		}
		n /= 40
		if d != 0 && n == 0 {
			n = 1
		}
		if n > 10 {
			n = 10
		}
		return n
	}
	args := []string{"mousemove", strconv.Itoa(x), strconv.Itoa(y)}
	vb, hb := "5", "7"
	if dy < 0 {
		vb = "4"
	}
	if dx < 0 {
		hb = "6"
	}
	for i := 0; i < notches(dy); i++ {
		args = append(args, "click", vb)
	}
	for i := 0; i < notches(dx); i++ {
		args = append(args, "click", hb)
	}
	if len(args) == 3 { // only the mousemove — nothing to scroll
		return nil
	}
	_, err := run("xdotool", args...)
	return err
}

func (linuxWindows) typeText(w winInfo, text string) error {
	if !have("xdotool") {
		return fmt.Errorf("install xdotool for input control")
	}
	_, err := run("xdotool", "type", "--clearmodifiers", "--", text)
	return err
}

var linuxKeySyms = map[string]string{
	"return": "Return", "enter": "Return", "tab": "Tab", "space": "space",
	"delete": "BackSpace", "forwarddelete": "Delete", "escape": "Escape",
	"left": "Left", "right": "Right", "down": "Down", "up": "Up",
	"home": "Home", "end": "End", "pageup": "Prior", "pagedown": "Next",
}

func (linuxWindows) key(w winInfo, name string) error {
	if !have("xdotool") {
		return fmt.Errorf("install xdotool for input control")
	}
	sym, ok := linuxKeySyms[strings.ToLower(name)]
	if !ok {
		return fmt.Errorf("unknown key %q", name)
	}
	_, err := run("xdotool", "key", sym)
	return err
}

func (linuxWindows) exists(id string) bool {
	// wmctrl -l lists all managed windows (any workspace); the hex id appears
	// as the first column. Present → still open.
	out, err := run("wmctrl", "-l")
	if err != nil {
		return true
	}
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) > 0 && strings.EqualFold(f[0], id) {
			return true
		}
	}
	return false
}

func (linuxWindows) releaseInput() error {
	if !have("xdotool") {
		return nil
	}
	// xdotool takes one button/key per sub-command, but chains them in a single
	// invocation. Release the three mouse buttons and common modifiers.
	_, err := run("xdotool",
		"mouseup", "1", "mouseup", "2", "mouseup", "3",
		"keyup", "ctrl", "keyup", "alt", "keyup", "shift", "keyup", "super")
	return err
}

// runRaw executes a command and returns its raw stdout bytes (no trimming) so
// binary output like a captured JPEG survives intact.
func runRaw(name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("%s: %s", name, strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return out, nil
}
