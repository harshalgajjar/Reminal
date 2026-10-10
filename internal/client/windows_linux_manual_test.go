// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build linux

package client

// Manual integration test for the X11 window backend, which every other test
// skips because it needs a real display, a real window manager, and the wmctrl /
// xdotool / ImageMagick trio actually installed.
//
// It is gated on REMINAL_X11_TEST=1 so `go test ./...` on a developer machine
// (or in CI) stays unaffected. Run it inside the container built by
// scripts/x11-test/Dockerfile, which provides Xvfb, openbox and an xterm to
// point at.
//
// The point is coverage of the code path, not of X11: every assertion is about
// what the backend returns to the rest of reminal — geometry the click mapper
// can use, JPEG bytes the viewer can decode, an app list the launcher can show.

import (
	"bytes"
	"image/jpeg"
	"os"
	"strings"
	"testing"
	"time"
)

func requireX11(t *testing.T) linuxWindows {
	t.Helper()
	if os.Getenv("REMINAL_X11_TEST") != "1" {
		t.Skip("set REMINAL_X11_TEST=1 (and run under a real X11 display) to enable")
	}
	b := linuxWindows{}
	if msg := b.unsupported(); msg != "" {
		t.Fatalf("backend reports unsupported: %s", msg)
	}
	return b
}

// findTestWindow returns the window the container opened for us to poke at.
func findTestWindow(t *testing.T, b linuxWindows) winInfo {
	t.Helper()
	wins, err := b.list()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(wins) == 0 {
		t.Fatal("no windows enumerated — is xterm running on this display?")
	}
	for _, w := range wins {
		if strings.EqualFold(w.App, "xterm") {
			return w
		}
	}
	return wins[0]
}

func TestX11List(t *testing.T) {
	b := requireX11(t)
	wins, err := b.list()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(wins) == 0 {
		t.Fatal("no windows enumerated")
	}
	for _, w := range wins {
		t.Logf("id=%s app=%q title=%q geom=%dx%d@%d,%d crop=%d,%d",
			w.ID, w.App, w.Title, w.W, w.H, w.X, w.Y, w.CropL, w.CropT)
		// Geometry is what click mapping divides by; zero or negative would send
		// every click to the wrong place (or panic the mapper).
		if w.W < 40 || w.H < 40 {
			t.Errorf("window %s has unusable size %dx%d — list() should have dropped it", w.ID, w.W, w.H)
		}
		// Real windows carry an X11 hex id, which focus/capture hand straight to
		// wmctrl/import. The one exception is the whole-desktop pseudo-window,
		// which every backend reports as "display:<n>" and which capture()
		// redirects to the root window — see TestX11FullDesktop.
		if !isDisplayID(w.ID) && !strings.HasPrefix(w.ID, "0x") {
			t.Errorf("window id %q is neither an X11 hex id nor a display: pseudo-window", w.ID)
		}
		if w.App == "" {
			t.Errorf("window %s has empty app name — the viewer groups by it", w.ID)
		}
	}
}

// trueRect asks X directly where the window is, independently of anything the
// backend computes — the oracle both geometry tests are measured against.
func trueRect(t *testing.T, id string) (x, y, w, h int) {
	t.Helper()
	args := []string{"-id", id}
	if id == "root" {
		args = []string{"-root"} // the root window has no id to pass
	}
	out, err := run("xwininfo", args...)
	if err != nil {
		t.Skipf("xwininfo unavailable, no independent oracle: %v", err)
	}
	seen := 0
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		for _, f := range []struct {
			prefix string
			into   *int
		}{
			{"Absolute upper-left X:", &x},
			{"Absolute upper-left Y:", &y},
			{"Width:", &w},
			{"Height:", &h},
		} {
			if rest, ok := strings.CutPrefix(line, f.prefix); ok {
				*f.into = atoi(strings.TrimSpace(rest))
				seen++
			}
		}
	}
	if seen != 4 {
		t.Fatalf("could not read window rect from xwininfo:\n%s", out)
	}
	return x, y, w, h
}

// TestX11GeometryIsAbsolute pins list()'s geometry to what X itself reports for
// the window. It regresses a bug that only shows up on a reparenting window
// manager: xdotool (and wmctrl -G) add the client's offset inside its frame to
// coordinates that are already absolute, so the backend placed every window one
// titlebar too low and sent every click there too.
func TestX11GeometryIsAbsolute(t *testing.T) {
	b := requireX11(t)
	w := findTestWindow(t, b)

	wantX, wantY, _, _ := trueRect(t, w.ID)
	if w.X != wantX || w.Y != wantY {
		t.Errorf("list() reports origin (%d,%d) but X says the client is at (%d,%d); "+
			"clicks and region captures would be off by (%d,%d)",
			w.X, w.Y, wantX, wantY, w.X-wantX, w.Y-wantY)
	}
}

func TestX11Capture(t *testing.T) {
	b := requireX11(t)
	w := findTestWindow(t, b)
	img, err := b.capture(w)
	if err != nil {
		t.Fatalf("capture %s: %v", w.ID, err)
	}
	if len(img) < 512 {
		t.Fatalf("capture returned %d bytes — too small to be a frame", len(img))
	}
	// The viewer decodes these as JPEG; anything else renders as a broken image.
	if img[0] != 0xFF || img[1] != 0xD8 {
		t.Fatalf("capture is not JPEG (starts %#x %#x)", img[0], img[1])
	}
	t.Logf("capture ok: %d bytes for %dx%d window", len(img), w.W, w.H)
	dumpFrame(t, "capture.jpg", img)
}

// On an Xvfb that keeps its screen in a file (the test bed with XVFB_FBDIR),
// a frame read from that file shows what import shows of the same window —
// same size, same pixels within JPEG's give — and costs a fraction of it.
func TestX11FramebufferMatchesImport(t *testing.T) {
	b := requireX11(t)
	fb := currentXvfbFB()
	if fb == nil {
		if os.Getenv("XVFB_FBDIR") != "" {
			t.Fatal("XVFB_FBDIR is set, and no framebuffer file was found for this display")
		}
		t.Skip("this display keeps no framebuffer file (set XVFB_FBDIR for the test bed)")
	}
	w := findTestWindow(t, b)
	fromFB, err := b.capture(w)
	if err != nil {
		t.Fatalf("framebuffer capture: %v", err)
	}
	fromImport, err := b.captureImport(w)
	if err != nil {
		t.Fatalf("import capture: %v", err)
	}
	a, errA := jpeg.Decode(bytes.NewReader(fromFB))
	c, errC := jpeg.Decode(bytes.NewReader(fromImport))
	if errA != nil || errC != nil {
		t.Fatalf("decode: %v / %v", errA, errC)
	}
	if a.Bounds().Size() != c.Bounds().Size() {
		t.Fatalf("framebuffer frame %v, import's %v", a.Bounds().Size(), c.Bounds().Size())
	}
	var diff, n uint64
	lo, hi := uint32(0xffff), uint32(0)
	for y := 0; y < a.Bounds().Dy(); y++ {
		for x := 0; x < a.Bounds().Dx(); x++ {
			r1, g1, b1, _ := a.At(x, y).RGBA()
			if l := (r1 + g1 + b1) / 3; l < lo {
				lo = l
			} else if l > hi {
				hi = l
			}
			r2, g2, b2, _ := c.At(x, y).RGBA()
			for _, d := range [][2]uint32{{r1, r2}, {g1, g2}, {b1, b2}} {
				if d[0] > d[1] {
					diff += uint64(d[0]-d[1]) >> 8
				} else {
					diff += uint64(d[1]-d[0]) >> 8
				}
				n++
			}
		}
	}
	// A blank frame agrees with a blank frame: the window shows something
	// (the test bed's xterm, its prompt), dark on light.
	if hi < lo || (hi-lo)>>8 < 50 {
		t.Fatalf("the framebuffer frame is flat (luminance %d to %d): nothing of the window in it", lo>>8, hi>>8)
	}
	if mean := float64(diff) / float64(n); mean > 6 {
		t.Errorf("the frames differ by %.1f a channel on average: the framebuffer frame is not the window", mean)
	} else {
		t.Logf("framebuffer and import frames agree: %.2f a channel on average", mean)
	}
	dumpFrame(t, "capture-fb.jpg", fromFB)
	// What each costs a frame (the framebuffer's encoded fresh each time).
	const n2 = 10
	t0 := time.Now()
	for i := 0; i < n2; i++ {
		xvfbLast.mu.Lock()
		xvfbLast.m = nil
		xvfbLast.mu.Unlock()
		if _, err := b.capture(w); err != nil {
			t.Fatal(err)
		}
	}
	fbEach := time.Since(t0) / n2
	t0 = time.Now()
	for i := 0; i < n2; i++ {
		if _, err := b.captureImport(w); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("a %dx%d window: %v a frame from the framebuffer, %v by import", w.W, w.H, fbEach, time.Since(t0)/n2)
}

// dumpFrame writes a captured frame to $REMINAL_X11_DUMP so a human can confirm
// it shows the window and not, say, a black rectangle — which is what a broken
// `import -window` produces, and which passes every byte-level assertion.
func dumpFrame(t *testing.T, name string, img []byte) {
	t.Helper()
	dir := os.Getenv("REMINAL_X11_DUMP")
	if dir == "" {
		return
	}
	if err := os.WriteFile(dir+"/"+name, img, 0o644); err != nil {
		t.Logf("dump %s: %v", name, err)
		return
	}
	t.Logf("wrote %s/%s", dir, name)
}

func TestX11CaptureRegion(t *testing.T) {
	b := requireX11(t)
	w := findTestWindow(t, b)
	img, err := b.captureRegion(w.X, w.Y, w.W, w.H)
	if err != nil {
		t.Fatalf("captureRegion: %v", err)
	}
	if len(img) < 512 || img[0] != 0xFF || img[1] != 0xD8 {
		t.Fatalf("captureRegion returned %d bytes, not a JPEG", len(img))
	}
	t.Logf("captureRegion ok: %d bytes", len(img))
	dumpFrame(t, "region.jpg", img)
}

func TestX11FocusAndExists(t *testing.T) {
	b := requireX11(t)
	w := findTestWindow(t, b)
	if !b.exists(w.ID) {
		t.Errorf("exists(%s) is false for a window list() just returned", w.ID)
	}
	if b.exists("0xdeadbeef") {
		t.Error("exists() returned true for a bogus window id")
	}
	if err := b.focus(w); err != nil {
		t.Errorf("focus %s: %v", w.ID, err)
	}
}

func TestX11Input(t *testing.T) {
	b := requireX11(t)
	w := findTestWindow(t, b)
	if err := b.focus(w); err != nil {
		t.Fatalf("focus: %v", err)
	}
	// Each of these shells out to xdotool with a different argument shape; a typo
	// in any one of them is invisible until someone tries it from a phone.
	if err := b.typeText(w, "echo reminal-x11-ok"); err != nil {
		t.Errorf("typeText: %v", err)
	}
	if err := b.key(w, "Return"); err != nil {
		t.Errorf("key Return: %v", err)
	}
	if err := b.clickN(w, 0.5, 0.5, 1, false); err != nil {
		t.Errorf("clickN: %v", err)
	}
	if err := b.clickN(w, 0.5, 0.5, 2, false); err != nil {
		t.Errorf("clickN double: %v", err)
	}
	if err := b.clickN(w, 0.5, 0.5, 1, true); err != nil {
		t.Errorf("clickN right: %v", err)
	}
	if err := b.scroll(w, 0.5, 0.5, 0, -3); err != nil {
		t.Errorf("scroll: %v", err)
	}
	if err := b.drag(w, [][2]float64{{0.3, 0.3}, {0.6, 0.6}}); err != nil {
		t.Errorf("drag: %v", err)
	}
	if err := b.releaseInput(); err != nil {
		t.Errorf("releaseInput: %v", err)
	}
}

// TestX11TypeTextLanded proves typeText actually reached the application rather
// than merely exiting 0 — the failure mode that a "no error" assertion misses.
func TestX11TypeTextLanded(t *testing.T) {
	b := requireX11(t)
	w := findTestWindow(t, b)
	if !strings.EqualFold(w.App, "xterm") {
		t.Skip("needs the xterm test window to read its title back")
	}
	if err := b.focus(w); err != nil {
		t.Fatalf("focus: %v", err)
	}
	// The container runs xterm with a shell that puts the last command in the
	// title, so typing a command and pressing Return changes the window title.
	marker := "reminalx11"
	if err := b.typeText(w, "printf '\\033]0;"+marker+"\\007'"); err != nil {
		t.Fatalf("typeText: %v", err)
	}
	if err := b.key(w, "Return"); err != nil {
		t.Fatalf("key: %v", err)
	}
	deadline := 40
	for i := 0; i < deadline; i++ {
		wins, err := b.list()
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		for _, x := range wins {
			if x.ID == w.ID && strings.Contains(x.Title, marker) {
				t.Logf("typed text reached the app: title is now %q", x.Title)
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Error("typed text never reached the application — window title never changed")
}

// TestX11ClickLandsInWindow checks where a tap actually puts the pointer. The
// geometry bug this regresses was invisible to every "did it error" assertion:
// xdotool exited 0 while moving the pointer a titlebar's height below the spot
// the user touched, and a tap near the bottom edge left the window entirely.
func TestX11ClickLandsInWindow(t *testing.T) {
	b := requireX11(t)
	w := findTestWindow(t, b)
	tx, ty, tw, th := trueRect(t, w.ID)

	for _, p := range []struct{ fx, fy float64 }{{0.5, 0.5}, {0.1, 0.05}, {0.9, 0.95}} {
		if err := b.clickN(w, p.fx, p.fy, 1, false); err != nil {
			t.Fatalf("clickN(%v,%v): %v", p.fx, p.fy, err)
		}
		out, err := run("xdotool", "getmouselocation", "--shell")
		if err != nil {
			t.Fatalf("getmouselocation: %v", err)
		}
		var gotX, gotY int
		for _, line := range strings.Split(out, "\n") {
			if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "X="); ok {
				gotX = atoi(rest)
			}
			if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "Y="); ok {
				gotY = atoi(rest)
			}
		}
		// Measured against X's own rect, not against w — comparing the landing
		// point to the same geometry that positioned it would agree with itself
		// no matter how wrong both were.
		wantX := tx + int(p.fx*float64(tw))
		wantY := ty + int(p.fy*float64(th))
		if gotX != wantX || gotY != wantY {
			t.Errorf("tap at (%.2f,%.2f) moved the pointer to (%d,%d); the window is really %dx%d@%d,%d so it should be (%d,%d)",
				p.fx, p.fy, gotX, gotY, tw, th, tx, ty, wantX, wantY)
		}
		if gotX < tx || gotX >= tx+tw || gotY < ty || gotY >= ty+th {
			t.Errorf("tap at (%.2f,%.2f) landed at (%d,%d), outside the window — it hit whatever is behind it",
				p.fx, p.fy, gotX, gotY)
		}
	}
}

// TestX11FullDesktop covers the viewer's "View full desktop" button, which
// looks for a pseudo-window whose id starts with "display:". Without one, a
// perfectly good X session answers "Not supported by this host".
func TestX11FullDesktop(t *testing.T) {
	b := requireX11(t)
	wins, err := b.list()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var desk *winInfo
	for i := range wins {
		if isDisplayID(wins[i].ID) {
			desk = &wins[i]
			break
		}
	}
	if desk == nil {
		t.Fatal("list() returned no display: pseudo-window — the viewer would say the host can't do full desktop")
	}
	t.Logf("desktop entry: id=%s app=%q title=%q %dx%d@%d,%d",
		desk.ID, desk.App, desk.Title, desk.W, desk.H, desk.X, desk.Y)

	// The screen has to match what X reports for the root window, or the pane
	// is framed wrong and every click inside it is mapped against a bad rect.
	rx, ry, rw, rh := trueRect(t, "root")
	if desk.X != rx || desk.Y != ry || desk.W != rw || desk.H != rh {
		t.Errorf("desktop rect is %dx%d@%d,%d but the root window is %dx%d@%d,%d",
			desk.W, desk.H, desk.X, desk.Y, rw, rh, rx, ry)
	}
	// The viewer groups by App and shows Title; both are visible UI.
	if desk.App != "Desktop" {
		t.Errorf("desktop app is %q, want \"Desktop\" (matches the macOS/Windows backends)", desk.App)
	}
	if desk.Title == "" {
		t.Error("desktop entry has no title")
	}

	img, err := b.capture(*desk)
	if err != nil {
		t.Fatalf("capture desktop: %v", err)
	}
	if len(img) < 512 || img[0] != 0xFF || img[1] != 0xD8 {
		t.Fatalf("desktop capture returned %d bytes, not a JPEG", len(img))
	}
	t.Logf("desktop capture ok: %d bytes", len(img))
	dumpFrame(t, "desktop.jpg", img)

	// Panes call these on the id they were opened with; a display must not be
	// treated as a missing window (which would close the pane immediately).
	if !b.exists(desk.ID) {
		t.Error("exists() is false for the desktop — its pane would close on open")
	}
	if err := b.focus(*desk); err != nil {
		t.Errorf("focus(desktop): %v — there is no window to raise, it should no-op", err)
	}
}

func TestX11ListApps(t *testing.T) {
	b := requireX11(t)
	apps, err := b.listApps()
	if err != nil {
		t.Fatalf("listApps: %v", err)
	}
	t.Logf("listApps returned %d entries", len(apps))
	for i, a := range apps {
		if a.ID == "" || a.Name == "" {
			t.Errorf("app %d has empty id/name: %+v", i, a)
		}
		if !strings.HasSuffix(a.ID, ".desktop") {
			t.Errorf("app %d id %q is not a .desktop path; openApp parses it as one", i, a.ID)
		}
		if i < 5 {
			t.Logf("  %s -> %s", a.Name, a.ID)
		}
	}
}
