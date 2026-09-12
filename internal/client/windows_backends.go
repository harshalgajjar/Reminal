// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"fmt"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
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

// permissionHint reports whether this process holds Screen Recording permission,
// returning a viewer-facing warning if not. Without it macOS still enumerates
// windows but hands back black/empty captures — so we tell the user how to fix it
// instead of showing silent blank panes.
//
// Detection goes through reminal-capture's `check` (a native, non-prompting
// CGPreflightScreenCaptureAccess). The old osascript/JXA check was dead code —
// $.CGPreflightScreenCaptureAccess is undefined in JavaScript-for-Automation, so
// the call always errored and the hint never fired. No helper (screencapture-only
// path) => we can't cheaply preflight, so stay quiet rather than warn spuriously.
func (darwinWindows) permissionHint() string {
	// Capture runs in the DAEMON (sh.reminal), so ask it — not this session, which
	// may be attributed to Terminal. "" (daemon unreachable) → stay quiet; the
	// capture path surfaces the "service starting" state instead.
	if mirrorCheck() == "no" {
		return "Screen Recording isn't granted for reminal on this Mac, so windows " +
			"list but can't be mirrored. Run  reminal permissions  on the host to " +
			"grant it (and remote-control access), then reopen the window."
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
// "id<TAB>pid<TAB>bundle<TAB>owner<TAB>title<TAB>x<TAB>y<TAB>w<TAB>h" lines.
// Layer 0 filters to
// normal application windows (skips the menu bar, Dock, shadows, etc.).
const jxaListScript = `ObjC.import("CoreGraphics"); ObjC.import("Foundation"); ObjC.import("AppKit");
var arr = ObjC.castRefToObject($.CGWindowListCopyWindowInfo($.kCGWindowListOptionOnScreenOnly | $.kCGWindowListExcludeDesktopElements, $.kCGNullWindowID));
var n = arr.count, out = [];
for (var i = 0; i < n; i++) {
  var w = arr.objectAtIndex(i);
  if (w.objectForKey("kCGWindowLayer").intValue !== 0) continue;
  var owner = w.objectForKey("kCGWindowOwnerName");
  var name = w.objectForKey("kCGWindowName");
  var num = w.objectForKey("kCGWindowNumber").intValue;
  var pid = w.objectForKey("kCGWindowOwnerPID").intValue;
  var running = $.NSRunningApplication.runningApplicationWithProcessIdentifier(pid);
  var bundle = running && running.bundleURL ? ObjC.unwrap(running.bundleURL.path) : "";
  var b = w.objectForKey("kCGWindowBounds");
  out.push([num, pid, bundle, owner ? ObjC.unwrap(owner) : "", name ? ObjC.unwrap(name) : "",
    b.objectForKey("X").intValue, b.objectForKey("Y").intValue,
    b.objectForKey("Width").intValue, b.objectForKey("Height").intValue].join("\t"));
}
// Whole desktops ride the same list as pseudo-windows ("display:<id>", owner
// "Desktop"). CGDisplayBounds gives CG-coordinate (top-left origin) rects — the
// same space as the window bounds above, so input mapping works unchanged.
var screens = $.NSScreen.screens;
for (var i = 0; i < screens.count; i++) {
  var did = screens.objectAtIndex(i).deviceDescription.objectForKey("NSScreenNumber").intValue;
  var db = $.CGDisplayBounds(did);
  var dw = Math.round(db.size.width), dh = Math.round(db.size.height);
  var label = (screens.count > 1 ? "Display " + (i + 1) : "Entire screen") + " — " + dw + "×" + dh;
  out.push(["display:" + did, 0, "", "Desktop", label,
    Math.round(db.origin.x), Math.round(db.origin.y), dw, dh].join("\t"));
}
out.join("\n");`

// isDisplayID reports whether a window id names a whole display (a desktop
// pseudo-window from the list above) rather than one CGWindow.
func isDisplayID(id string) bool { return strings.HasPrefix(id, "display:") }

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
		if len(f) < 9 {
			continue
		}
		// Title may itself contain tabs; the numeric fields are the last four.
		n := len(f)
		id, pid, appPath, owner := f[0], atoi(f[1]), f[2], f[3]
		title := strings.Join(f[4:n-4], "\t")
		x, y := atoi(f[n-4]), atoi(f[n-3])
		w, h := atoi(f[n-2]), atoi(f[n-1])
		if w < 40 || h < 40 {
			continue
		}
		wins = append(wins, winInfo{ID: id, PID: pid, AppPath: appPath, App: owner, Title: title, X: x, Y: y, W: w, H: h})
	}
	darwinAddWindowIcons(wins)
	return wins, nil
}

// macOS exposes the canonical icon for any application bundle through
// NSWorkspace. Render it into a small Retina-aware bitmap in one JXA process
// for the whole batch: spawning osascript once per app makes opening the Apps
// menu needlessly slow. The resulting PNG data URLs can be rendered directly
// by the browser and stay end-to-end encrypted with the rest of the list.
const jxaAppIconsScript = `ObjC.import("AppKit");
function run(argv) {
  // AppKit renders this point size at 2× on Retina hosts, yielding a crisp
  // 24px bitmap for the browser without bloating the encrypted app list.
  var out = [], n = 12;
  for (var i = 0; i < argv.length; i++) {
    try {
      var path = String(argv[i]);
      var src = $.NSWorkspace.sharedWorkspace.iconForFile(path);
      if (!src) continue;
      var dst = $.NSImage.alloc.initWithSize($.NSMakeSize(n, n));
      dst.lockFocus;
      src.drawInRectFromRectOperationFraction($.NSMakeRect(0, 0, n, n), $.NSZeroRect,
        $.NSCompositingOperationCopy, 1);
      dst.unlockFocus;
      var rep = $.NSBitmapImageRep.imageRepWithData(dst.TIFFRepresentation);
      var png = rep.representationUsingTypeProperties($.NSBitmapImageFileTypePNG, $({}));
      out.push(path + "\t" + ObjC.unwrap(png.base64EncodedStringWithOptions(0)));
    } catch (_) {}
  }
  return out.join("\n");
}`

var darwinIconCache sync.Map // bundle path -> data URL (empty string caches failures)

// iconFetchTimeout caps one icon batch. Generous for a cold cache of a few
// hundred apps; nothing like the forever a blocked consent prompt would cost.
const iconFetchTimeout = 20 * time.Second

func darwinIcons(paths []string) map[string]string {
	icons := make(map[string]string, len(paths))
	missing := make([]string, 0, len(paths))
	seen := make(map[string]bool, len(paths))
	for _, path := range paths {
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		if cached, ok := darwinIconCache.Load(path); ok {
			if icon, _ := cached.(string); icon != "" {
				icons[path] = icon
			}
			continue
		}
		missing = append(missing, path)
	}
	if len(missing) != 0 {
		args := append([]string{"-l", "JavaScript", "-e", jxaAppIconsScript, "--"}, missing...)
		// Icons are decoration. Bounded, because this is osascript and on a
		// machine that has not granted Automation it can sit on a consent
		// dialog indefinitely — and the app list must still arrive without
		// icons rather than never.
		out, err := runTimeout(iconFetchTimeout, "osascript", args...)
		found := make(map[string]string, len(missing))
		if err == nil {
			for _, line := range strings.Split(out, "\n") {
				f := strings.SplitN(line, "\t", 2)
				if len(f) == 2 && f[0] != "" && f[1] != "" {
					found[f[0]] = "data:image/png;base64," + f[1]
				}
			}
		}
		for _, path := range missing {
			icon := found[path]
			darwinIconCache.Store(path, icon)
			if icon != "" {
				icons[path] = icon
			}
		}
	}
	return icons
}

func darwinAddWindowIcons(wins []winInfo) {
	paths := make([]string, 0, len(wins))
	for i := range wins {
		paths = append(paths, wins[i].AppPath)
	}
	icons := darwinIcons(paths)
	for i := range wins {
		wins[i].Icon = icons[wins[i].AppPath]
	}
}

// winMaxWidth is the width we downscale captured frames to. Retina windows
// capture at 2× (a 1728px window → 3456px, ~1.3 MB JPEG), which blows past the
// relay's 1 MiB WS frame cap. 1100px keeps frames ~55 KB — small enough to
// stream at a higher frame rate (less to encode and send) while still looking
// sharp scaled into a pane, and it leaves headroom for pinch-zoom.
const winMaxWidth = 1100

func (darwinWindows) capture(w winInfo) ([]byte, error) {
	if isDisplayID(w.ID) {
		// Whole desktop: grab the display's rect (same space as CGWindowList).
		return screencaptureJPEG(fmt.Sprintf("-R%d,%d,%d,%d", w.X, w.Y, w.W, w.H))
	}
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
	// Finder is the one app users expect that no folder scan can find: Apple
	// ships it in /System/Library/CoreServices, which isn't worth scanning
	// wholesale — it's full of internal .app bundles (Dock, Control Center,
	// loginwindow…) that would drown the list in machinery nobody can
	// meaningfully open. Special-case just Finder; `open` takes its bundle
	// path like any other app's.
	if _, err := os.Stat("/System/Library/CoreServices/Finder.app"); err == nil {
		add("/System/Library/CoreServices", "Finder.app")
	}
	sort.Slice(apps, func(i, j int) bool {
		return strings.ToLower(apps[i].Name) < strings.ToLower(apps[j].Name)
	})
	paths := make([]string, len(apps))
	for i := range apps {
		paths[i] = apps[i].ID
	}
	icons := darwinIcons(paths)
	// Keep comfortable headroom below the relay's 1 MiB encrypted-frame cap.
	// Encryption base64-expands the JSON again, so cap the already-base64 icon
	// strings at 560 KiB. Extremely large app collections retain names and use
	// the browser's fallback tile after this budget is exhausted.
	const iconBudget = 600 << 10
	used := 0
	for i := range apps {
		icon := icons[apps[i].ID]
		if icon == "" || used+len(icon) > iconBudget {
			continue
		}
		apps[i].Icon = icon
		used += len(icon)
	}
	return apps, nil
}

func (darwinWindows) openApp(id string) error {
	// `open <bundle path>` launches the app, or foregrounds it (and opens a
	// window if it has none) when already running.
	_, err := run("open", id)
	return err
}

func (darwinWindows) focus(w winInfo) error {
	if isDisplayID(w.ID) {
		return nil // a desktop has no window to raise; clicks land wherever aimed
	}
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

// close presses the window's red close button via Accessibility, so the app
// closes it exactly as if the user clicked it — including any "save changes?"
// sheet the app puts up. Uses the SAME best-window matching as focus (score on
// title + geometry, target the process by PID) so multi-window apps close the
// right one. Not a kill: no process is signalled, and a window with no close
// button (or an app that ignores it) simply stays open.
func (darwinWindows) close(w winInfo) error {
	if isDisplayID(w.ID) {
		return nil // a whole desktop has no window to close
	}
	script := fmt.Sprintf(`tell application "System Events" to tell (first process whose unix id is %d)
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
		if bestWin is not missing value then
			perform action "AXPress" of (first button of bestWin whose subrole is "AXCloseButton")
		end if
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

// dragPhase posts one step of a LIVE drag: press, move, release, each as its
// own event as the finger produces it. The batched drag below replays a whole
// path after the fact, which is why a drag never tracked the pointer. The
// button is system-wide state, so these separate calls compose into one
// continuous gesture as far as the target app is concerned.
func (darwinWindows) dragPhase(w winInfo, phase string, fx, fy float64) error {
	p, err := captureHelperPath()
	if err != nil {
		return err
	}
	x := w.X + int(fx*float64(w.W))
	y := w.Y + int(fy*float64(w.H))
	_, err = run(p, "drag", phase, strconv.Itoa(x), strconv.Itoa(y))
	return err
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
	// Post via the compiled helper when it's installed: the JXA bridge builds a
	// scroll event whose wheel deltas don't marshal — it posts as a silent
	// no-op, so viewer scrolling did nothing (the mouse-move from the same
	// script works, which is why clicks were fine). The helper is also ~5×
	// faster per event than an osascript spawn.
	if p, err := captureHelperPath(); err == nil {
		if _, err := run(p, "scroll", strconv.Itoa(x), strconv.Itoa(y), strconv.Itoa(w1), strconv.Itoa(w2)); err == nil {
			return nil
		}
	}
	// Fallback for installs without the helper (may no-op on some macOS
	// versions — kept because it's still the only path there).
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

// darwinModifiers renders our neutral modifier names as an AppleScript
// "using {...}" clause, or "" for none.
func darwinModifiers(mods []string) string {
	names := map[string]string{"cmd": "command down", "ctrl": "control down", "alt": "option down", "shift": "shift down"}
	var parts []string
	for _, m := range mods {
		if v := names[m]; v != "" {
			parts = append(parts, v)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return " using {" + strings.Join(parts, ", ") + "}"
}

func (darwinWindows) key(w winInfo, name string) error {
	mods, base := splitKeyChord(name)
	using := darwinModifiers(mods)
	// A printable single char goes through layout-aware `keystroke`; a named
	// key (return, left, …) through `key code`. Modifiers ride either — so
	// cmd+c, ctrl+shift+t and cmd+left all inject the same way.
	if len(base) == 1 && base[0] > ' ' {
		script := fmt.Sprintf(`tell application "System Events" to keystroke %s%s`, asStr(base), using)
		_, err := run("osascript", "-e", script)
		return err
	}
	code, ok := darwinKeyCodes[base]
	if !ok {
		return fmt.Errorf("unknown key %q", name)
	}
	script := fmt.Sprintf(`tell application "System Events" to key code %d%s`, code, using)
	_, err := run("osascript", "-e", script)
	return err
}

func (darwinWindows) exists(id string) bool {
	if isDisplayID(id) {
		// Displays don't appear in CGWindowList; treat as present. A truly
		// disconnected display vanishes from list(), which the stream's
		// geometry poll (findWindow) already turns into a closed pane.
		return true
	}
	// Check the SAME on-screen list the picker (list) uses, so a pane's lifetime
	// tracks the picker exactly: closed and minimized windows leave this list
	// immediately, whereas kCGWindowListOptionAll retains a closed window's
	// (still-capturable) backing store for a while — which froze panes open
	// forever. Occluded/covered windows stay on-screen, so they're unaffected.
	// id is a CGWindowNumber we produced, so atoi is safe and keeps it out of the
	// script as a bare int.
	script := fmt.Sprintf(`ObjC.import("CoreGraphics");ObjC.import("Foundation");
var a=ObjC.castRefToObject($.CGWindowListCopyWindowInfo($.kCGWindowListOptionOnScreenOnly,$.kCGNullWindowID));
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
// Works on X11 sessions with wmctrl (enumerate/focus), ImageMagick `import`
// (capture), xdotool (input) and xwininfo (geometry). Wayland blocks synthetic
// input and cross-window capture for these tools, so it reports unsupported
// there.
//
// Exercised against a real X server, window manager and window by the gated
// TestX11* suite in windows_linux_manual_test.go; scripts/x11-test/run.sh brings
// up that desktop in a container.

type linuxWindows struct{}

func (linuxWindows) unsupported() string {
	// Headless first. With no display of any kind — a cloud VM, a plain SSH box
	// — the old code fell through to "install wmctrl", sending people after
	// packages that cannot help because there is no desktop to mirror at all.
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" && !isWaylandSession() {
		return "no desktop session on this host — window mirroring needs a graphical login; the terminal works as normal"
	}
	// Wayland: capture goes through a compositor screenshot tool (see
	// waylandcapture.go). Only the full-desktop view is supported so far. If no
	// tool is installed, say exactly that — the old code let the X11 path run and
	// stream a BLACK frame here, because Xwayland's root has no desktop content.
	if isWaylandSession() {
		if waylandShotTool() == "" {
			return "Wayland desktop view needs a screenshot tool — install one of: " + waylandShotDeps + ", or log in to an Xorg session"
		}
		return ""
	}
	if !have("wmctrl") {
		return "install wmctrl (and xdotool + imagemagick + x11-utils) to mirror windows on Linux"
	}
	return ""
}

// permissionHint surfaces a one-line caveat to the viewer. On X11 there's no
// per-app screen-capture permission, so it's silent. On Wayland it sets
// expectations: the desktop view works via a screenshot tool, but per-window
// mirroring and input injection have no portable Wayland path yet, and the
// compositor may prompt once to allow screen sharing.
func (linuxWindows) permissionHint() string {
	if isWaylandSession() {
		return "Wayland: the full-desktop view works, but per-window mirroring and click/keyboard control aren't supported here yet — and the host may prompt once to allow screen sharing."
	}
	return ""
}

func (linuxWindows) list() ([]winInfo, error) {
	// -l list, -G geometry, -x include WM_CLASS. Columns:
	//   id desktop wm_class x y w h host title...
	out, err := run("wmctrl", "-lGx")
	if err != nil {
		// A pure Wayland session (no Xwayland) has no wmctrl window list, but the
		// desktop is still capturable via the screenshot path — carry on with no
		// X windows and let the display:0 entry below make "View full desktop"
		// available. On X11, a wmctrl failure is still a hard error.
		if !isWaylandSession() {
			return nil, err
		}
		out = ""
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
	// The whole desktop rides the same list as a pseudo-window ("display:0"),
	// exactly as the macOS and Windows backends do — that entry is what the
	// viewer's "View full desktop" button looks for, and without it Linux hosts
	// answer "Not supported by this host" even with a perfectly good X session.
	//
	// One entry, not one per monitor: X11 composites every output into a single
	// root framebuffer, and `import -window root` captures precisely that. Its
	// origin is the root's own, so click mapping needs no special case.
	x, y, sw, sh, ok := xrootGeom()
	if !ok && isWaylandSession() {
		// No Xwayland root to measure — size the desktop from one screenshot.
		if w, h, wok := waylandScreenSize(); wok {
			x, y, sw, sh, ok = 0, 0, w, h, true
		}
	}
	if ok && sw >= 40 && sh >= 40 {
		wins = append(wins, winInfo{
			ID:    "display:0",
			App:   "Desktop",
			Title: fmt.Sprintf("Entire screen — %d×%d", sw, sh),
			X:     x, Y: y, W: sw, H: sh,
		})
	}
	return wins, nil
}

// linuxGeom returns a window's true absolute geometry: the client area's origin
// in root coordinates, which is what every click, drag, scroll and region
// capture is computed from. Returns zeros on error, so the caller's w/h < 40
// check drops the window.
//
// Neither wmctrl -G nor xdotool can be trusted here, and they are wrong in the
// same way: on a reparenting window manager both add the client's offset inside
// its frame to coordinates that are already absolute, so they report the window
// one titlebar too low. Measured under openbox, where the frame is 1px of border
// and a 20px titlebar: xwininfo says the client is at (61,60), xdotool says
// (62,80). Acting on that put every tap a titlebar's height below where the user
// aimed, and pushed taps near the bottom edge off the window entirely.
//
// xwininfo reports the translated origin directly, so prefer it. Where it isn't
// installed, fall back to xdotool with the frame extents subtracted — which is
// the same correction, and degrades to today's behaviour when the window manager
// publishes no extents.
func linuxGeom(id string) (x, y, w, h int) {
	if x, y, w, h, ok := xwininfoGeom(id); ok {
		return x, y, w, h
	}
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
	if l, _, t, _, ok := xpropExtents(id, "_NET_FRAME_EXTENTS"); ok {
		x, y = x-l, y-t
	}
	return x, y, w, h
}

// xwininfoGeom returns the window's absolute (root-relative) client origin and
// size. ok is false when xwininfo isn't installed or the window has gone away,
// leaving the caller to fall back.
func xwininfoGeom(id string) (x, y, w, h int, ok bool) {
	out, err := run("xwininfo", "-id", id)
	if err != nil {
		return 0, 0, 0, 0, false
	}
	return parseXwininfo(out)
}

// xrootGeom returns the root window's rect — the whole X screen, which is what
// `import -window root` captures.
func xrootGeom() (x, y, w, h int, ok bool) {
	out, err := run("xwininfo", "-root")
	if err != nil {
		return 0, 0, 0, 0, false
	}
	return parseXwininfo(out)
}

func parseXwininfo(out string) (x, y, w, h int, ok bool) {
	found := 0
	field := func(line, prefix string) (int, bool) {
		rest, cut := strings.CutPrefix(line, prefix)
		if !cut {
			return 0, false
		}
		return atoi(strings.TrimSpace(rest)), true
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		// "Absolute" distinguishes these from the "Relative upper-left" pair,
		// which is the offset inside the frame — the very number that makes
		// xdotool wrong.
		if v, hit := field(line, "Absolute upper-left X:"); hit {
			x, found = v, found+1
		} else if v, hit := field(line, "Absolute upper-left Y:"); hit {
			y, found = v, found+1
		} else if v, hit := field(line, "Width:"); hit {
			w, found = v, found+1
		} else if v, hit := field(line, "Height:"); hit {
			h, found = v, found+1
		}
	}
	return x, y, w, h, found == 4
}

// gtkFrameExtents reads _GTK_FRAME_EXTENTS (left, right, top, bottom) — the
// invisible CSD margins GTK draws for its drop shadow and resize grips. ok is
// false when the property is absent (non-CSD windows, or no xprop), meaning
// there's nothing to adjust.
func gtkFrameExtents(id string) (left, right, top, bottom int, ok bool) {
	return xpropExtents(id, "_GTK_FRAME_EXTENTS")
}

// xpropExtents reads a four-cardinal (left, right, top, bottom) window property.
// ok is false when the property is absent or xprop isn't installed, which every
// caller treats as "no adjustment needed".
func xpropExtents(id, atom string) (left, right, top, bottom int, ok bool) {
	out, err := run("xprop", "-id", id, atom)
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
	// Wayland: X11 root/window grabs come back black (see waylandcapture.go).
	// Capture via a compositor screenshot tool instead — the whole screen for
	// the desktop, or the window's screen rectangle for a specific window.
	if isWaylandSession() {
		if isDisplayID(w.ID) {
			return waylandCapture(nil)
		}
		r := image.Rect(w.X, w.Y, w.X+w.W, w.Y+w.H)
		return waylandCapture(&r)
	}
	if !have("import") {
		return nil, fmt.Errorf("install imagemagick (provides `import`) to capture windows")
	}
	// import writes JPEG to stdout; -window takes the hex window id.
	args := []string{"-window", w.ID}
	if isDisplayID(w.ID) {
		// Whole desktop: the root window IS the composited screen, so there is
		// no per-window id to pass and no CSD shadow to crop.
		args = []string{"-window", "root"}
	}
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
	if isWaylandSession() {
		r := image.Rect(x, y, x+w, y+h)
		return waylandCapture(&r)
	}
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
	if isDisplayID(w.ID) {
		return nil // a desktop has no window to raise; clicks land wherever aimed
	}
	_, err := run("wmctrl", "-i", "-a", w.ID)
	return err
}

// close asks the window manager to close the window with -c, which sends the
// ICCCM/EWMH _NET_CLOSE_WINDOW message — the same graceful close as the title
// bar's ✕, so the app can still prompt to save. Not a kill (-c, not wmctrl's
// process-killing paths); the same hex id we already hold targets it.
func (linuxWindows) close(w winInfo) error {
	if isDisplayID(w.ID) {
		return nil // a whole desktop has no window to close
	}
	_, err := run("wmctrl", "-i", "-c", w.ID)
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
	mods, base := splitKeyChord(name)
	sym := base
	if s, ok := linuxKeySyms[base]; ok {
		sym = s
	} else if !(len(base) == 1 && base[0] > ' ') {
		return fmt.Errorf("unknown key %q", name)
	}
	names := map[string]string{"cmd": "super", "ctrl": "ctrl", "alt": "alt", "shift": "shift"}
	prefix := ""
	for _, m := range mods {
		if v := names[m]; v != "" {
			prefix += v + "+"
		}
	}
	_, err := run("xdotool", "key", prefix+sym) // e.g. ctrl+c, super+l, ctrl+shift+t
	return err
}

func (linuxWindows) exists(id string) bool {
	if isDisplayID(id) {
		return true // the screen is always there; a dead X server fails elsewhere
	}
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
