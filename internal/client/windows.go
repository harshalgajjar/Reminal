// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/reminal/reminal/internal/keepawake"
	"github.com/reminal/reminal/internal/protocol"
)

// Window mirroring lets a viewer list the host's on-screen windows, stream a
// chosen one as periodic JPEG frames, and inject mouse/keyboard input to
// control it — a single-window remote desktop layered on the existing
// end-to-end-encrypted session. Everything OS-specific lives behind
// windowBackend; the streaming + dispatch machinery on the Agent is
// platform-neutral, so a second backend (Linux/X11, Wayland) drops in via
// newWindowBackend without touching agent.go.

// winInfo describes one on-screen window on the host. It is serialized to the
// viewer verbatim (the browser renders the dropdown from these fields), so
// keep it JSON-clean. ID is an opaque, backend-defined handle the viewer
// echoes back in window_ctl / window_input — on macOS it encodes the owning
// process name and the window's index; on X11 it's the numeric window id.
type winInfo struct {
	ID    string `json:"id"`
	App   string `json:"app"`
	Title string `json:"title"`
	Icon  string `json:"icon,omitempty"`
	X     int    `json:"x"`
	Y     int    `json:"y"`
	W     int    `json:"w"`
	H     int    `json:"h"`
	// PID is the owning process id (macOS), used to activate the app reliably
	// via NSRunningApplication. Not sent to the viewer.
	PID int `json:"-"`
	// AppPath identifies the owning application bundle when the backend can
	// provide it. It is used only to look up the app icon and never leaves the
	// host (the opaque window ID remains the viewer-facing handle).
	AppPath string `json:"-"`
	// CropL/CropT are the left/top invisible CSD shadow margins to crop off the
	// captured image (Linux). X/Y/W/H already describe the content rect inside
	// that shadow, so cropping by these offsets yields a borderless frame whose
	// pixels line up 1:1 with the coordinates we map clicks against. Zero for
	// windows without a shadow and on macOS. Not sent to the viewer.
	CropL int `json:"-"`
	CropT int `json:"-"`
}

// winMenuState snapshots a window's screen bounds at the moment a right-click
// opened a context menu, plus when to stop region-capturing. See Agent.winMenu.
type winMenuState struct {
	x, y, w, h int
	until      time.Time
}

// windowBackend abstracts the OS-specific bits of window mirroring. macOS is
// implemented today (osascript + screencapture); Linux/X11 has a best-effort
// implementation via wmctrl + xdotool + ImageMagick. Wayland and Windows can
// be added as further backends behind newWindowBackend.
type windowBackend interface {
	// list enumerates the host's on-screen windows.
	list() ([]winInfo, error)
	// capture returns a JPEG of the given window's current pixels.
	capture(w winInfo) ([]byte, error)
	// captureRegion returns a JPEG of a raw screen rectangle (top-left origin,
	// screen points). Unlike capture (one window by id), it grabs whatever is
	// on screen there — used briefly after a right-click so the context menu,
	// which the OS draws as a separate window overlapping this one, is included.
	captureRegion(x, y, w, h int) ([]byte, error)
	// focus raises the window to the front so captures aren't occluded and
	// injected input lands on it.
	focus(w winInfo) error
	// clickN injects a click at (fx, fy) — fractions in 0..1 from the window's
	// top-left — with the given click-state (1=single, 2=double, 3=triple) and
	// button (right=true for the secondary/context button). The viewer reports
	// the click count so apps see native single/double/triple-clicks regardless
	// of network jitter; count falls back to the Agent's own rapid-click timer
	// for older viewers that don't send it.
	clickN(w winInfo, fx, fy float64, count int, right bool) error
	// drag presses at pts[0], drags through the intermediate points, and
	// releases at the last — a real click-drag (text selection, sliders,
	// dragging files). Points are (fx, fy) fractions in 0..1.
	drag(w winInfo, pts [][2]float64) error
	// scroll injects a scroll-wheel gesture at (fx, fy) — fractions in 0..1 —
	// by dx/dy pixel-ish deltas (positive dy scrolls the content down, matching
	// a mouse wheel / a two-finger swipe up).
	scroll(w winInfo, fx, fy, dx, dy float64) error
	// typeText types literal text into the focused window.
	typeText(w winInfo, text string) error
	// key sends a single named special key ("return", "tab", "escape",
	// "delete", "up"/"down"/"left"/"right") to the focused window.
	key(w winInfo, name string) error
	// exists reports whether a window id is still open, checking the FULL
	// window list (all Spaces, including minimized/off-screen) so a minimize
	// or Space switch isn't mistaken for a close. Returns true on error, to
	// avoid closing a pane over a transient failure.
	exists(id string) bool
	// releaseInput releases any held mouse button and modifier keys, so an
	// interrupted click/drag can never leave the host's desktop stuck in a
	// grab. Called when a pane's stream stops and when a viewer disconnects.
	releaseInput() error
	// unsupported returns a human-readable reason if this backend can't run
	// here (missing tools, wrong session type), or "" if it's usable.
	unsupported() string
	// permissionHint returns a human-readable warning if windows can be listed
	// but likely can't be captured (e.g. macOS Screen Recording permission is
	// off), or "" when capture should work. Surfaced to the viewer so a blank
	// pane comes with an explanation and a fix instead of a silent freeze.
	permissionHint() string
	// listApps enumerates launchable installed applications, so the viewer can
	// offer an app launcher (open an app, then mirror its window).
	listApps() ([]appInfo, error)
	// openApp launches (or foregrounds) the app with the given id — a .app path
	// on macOS, a .desktop file path on Linux — bringing up its window.
	openApp(id string) error
}

// appInfo describes one launchable installed application. ID is an opaque,
// backend-defined handle the viewer echoes back in app_open (a bundle path on
// macOS, a .desktop file path on Linux); Name is the display label.
type appInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Icon string `json:"icon,omitempty"`
}

// newWindowBackend picks the backend for the current OS. Unknown platforms get
// a stub whose every call reports that window mirroring isn't supported yet.
func newWindowBackend() windowBackend {
	// Native (build-tagged) backends first — Windows/Win32 lives behind this
	// seam so its syscalls never enter non-Windows builds.
	if b := newNativeWindowBackend(); b != nil {
		return b
	}
	switch runtime.GOOS {
	case "darwin":
		return darwinWindows{}
	case "linux":
		return linuxWindows{}
	default:
		return stubWindows{os: runtime.GOOS}
	}
}

// findWindow returns the window with the given ID from a fresh enumeration, so
// callers always act on current bounds (windows move and resize between the
// list the viewer saw and the moment it clicks).
func findWindow(b windowBackend, id string) (winInfo, error) {
	wins, err := b.list()
	if err != nil {
		return winInfo{}, err
	}
	for _, w := range wins {
		if w.ID == id {
			return w, nil
		}
	}
	return winInfo{}, fmt.Errorf("window no longer open")
}

// ---- Agent wiring -----------------------------------------------------------

// windows lazily constructs (once) the backend for this host and starts the
// serialized worker that drains winOps.
func (a *Agent) windows() windowBackend {
	a.winOnce.Do(func() {
		a.winBackend = newWindowBackend()
		a.winOps = make(chan func(), 64)
		go func() {
			for op := range a.winOps {
				// Recover per-op: window ops run backend calls (osascript/xdotool/
				// screencapture, coordinate math) and some are driven by untrusted
				// viewer input. A panic in one must NOT take down the whole agent —
				// drop it and keep serving the queue.
				func() {
					defer func() {
						if r := recover(); r != nil {
							recoverLog("winOps", r)
						}
					}()
					op()
				}()
			}
		}()
	})
	return a.winBackend
}

// enqueueWinOp schedules a window operation (list / ctl / input) to run on the
// single worker goroutine, off the relay reader. Drops the op if the queue is
// full rather than blocking the reader — a lost keystroke beats a frozen
// terminal, and the viewer can retry.
func (a *Agent) enqueueWinOp(op func()) {
	a.windows() // ensure backend + worker exist
	select {
	case a.winOps <- op:
	default:
	}
}

// enqueueWinOpImportant queues an op that MUST run — unlike enqueueWinOp it never
// drops under backpressure. Used for releasing a held mouse button when viewers
// leave: a dropped release strands the host's desktop in a grab (a drag floods
// winOps AND holds a button, so the release is exactly what enqueueWinOp would
// drop). It blocks until the op is queued, so call it from its own goroutine —
// the op still runs on the worker, serialized with in-flight input. If the worker
// is wedged this leaks one goroutine (a far better failure than a stuck button)
// without blocking the relay reader.
func (a *Agent) enqueueWinOpImportant(op func()) {
	a.windows() // ensure backend + worker exist
	a.winOps <- op
}

// handleWindowList enumerates the host's windows and sends the encrypted list
// back to viewers in reply to a window_list request.
func (a *Agent) handleWindowList(conn *websocket.Conn) {
	b := a.windows()
	var payload struct {
		Windows     []winInfo               `json:"windows"`
		Unsupported string                  `json:"unsupported,omitempty"`
		Error       string                  `json:"error,omitempty"`
		Hint        string                  `json:"hint,omitempty"`
		Notes       map[string][]windowNote `json:"notes,omitempty"`
	}
	// Notes ride with the list rather than being pushed on connect. Pushing at
	// connect raced the viewer's key exchange — it had no cryptoKey yet and
	// silently dropped the message, which is why notes vanished on every
	// reconnect. A window_list request only happens once the viewer is keyed
	// and ready, so this is the one moment it is guaranteed to be heard.
	if a.notes != nil {
		payload.Notes = a.notes.snapshot()
	}
	if reason := b.unsupported(); reason != "" {
		payload.Unsupported = reason
	} else if wins, err := b.list(); err != nil {
		payload.Error = err.Error()
	} else {
		payload.Windows = wins
		payload.Hint = b.permissionHint() // e.g. Screen Recording is off
	}
	a.sendWindowMsg(conn, protocol.TypeWindowList, payload)
}

// handleAppList enumerates launchable installed apps and sends the encrypted
// list back in reply to an app_list request, so the viewer can offer a launcher.
func (a *Agent) handleAppList(conn *websocket.Conn) {
	b := a.windows()
	var payload struct {
		Apps        []appInfo `json:"apps"`
		Unsupported string    `json:"unsupported,omitempty"`
		Error       string    `json:"error,omitempty"`
	}
	if reason := b.unsupported(); reason != "" {
		payload.Unsupported = reason
	} else if apps, err := b.listApps(); err != nil {
		payload.Error = err.Error()
	} else {
		payload.Apps = apps
	}
	a.sendWindowMsg(conn, protocol.TypeAppList, payload)
}

// handleAppOpen launches (or foregrounds) the app the viewer picked; its window
// then shows up on the next window_list. Best-effort — a bad id just no-ops.
func (a *Agent) handleAppOpen(encData string) {
	plaintext, err := a.box.Decrypt(encData)
	if err != nil {
		return
	}
	var ev struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(plaintext, &ev) != nil || ev.ID == "" {
		return
	}
	b := a.windows()
	if b.unsupported() != "" {
		return
	}
	_ = b.openApp(ev.ID)
}

// handleWindowCtl starts or stops streaming a window in response to a
// window_ctl request from a viewer.
func (a *Agent) handleWindowCtl(encData string) {
	plaintext, err := a.box.Decrypt(encData)
	if err != nil {
		return
	}
	var req struct {
		Action string `json:"action"`
		ID     string `json:"id"`
		// Viewer is stable per tab. Without it a stop cannot be attributed, so
		// one viewer closing a pane stopped the stream for everyone watching
		// that window. Absent for viewers older than this field, which keeps
		// their old all-or-nothing behaviour.
		Viewer   string `json:"viewer"`
		MaxWidth int    `json:"max_width"`
		Quality  int    `json:"quality"`
	}
	if json.Unmarshal(plaintext, &req) != nil {
		return
	}
	switch req.Action {
	case "start":
		a.startWindowStream(req.ID, req.Viewer)
		// Old/official viewers omit these fields. Leave those streams in host-side
		// auto mode so they still sharpen when WebRTC becomes direct.
		if req.MaxWidth != 0 || req.Quality != 0 {
			a.tuneWindowStream(req.ID, windowQuality{MaxWidth: req.MaxWidth, Quality: req.Quality})
		}
	case "quality":
		a.tuneWindowStream(req.ID, windowQuality{MaxWidth: req.MaxWidth, Quality: req.Quality})
	case "stop":
		if a.dropWindowSub(req.ID, req.Viewer) {
			return // somebody else is still watching this window
		}
		a.stopWindowStream(req.ID)
		a.releaseWindowInput() // never leave a button held after a pane closes
	}
}

// windowQuality is a viewer-requested capture profile. Values are deliberately
// bounded on the host: a browser is an authenticated remote input, not a source
// we trust with arbitrary encoder sizes or CPU usage.
type windowQuality struct {
	MaxWidth int
	Quality  int
}

func (q windowQuality) normalized() windowQuality {
	if q.MaxWidth == 0 {
		q.MaxWidth = winMaxWidth
	}
	if q.Quality == 0 {
		q.Quality = winCaptureQuality
	}
	q.MaxWidth = max(720, min(2880, q.MaxWidth))
	q.Quality = max(35, min(82, q.Quality))
	return q
}

// tuneWindowStream replaces any stale pending profile with the newest one.
// Resizing a pane can emit several requests; only its final size matters.
func (a *Agent) tuneWindowStream(id string, q windowQuality) {
	q = q.normalized()
	a.winMu.Lock()
	ch := a.winQuality[id]
	a.winMu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- q:
	default:
		select {
		case <-ch:
		default:
		}
		select {
		case ch <- q:
		default:
		}
	}
}

// inputBlockNoticeEvery bounds how often the same obstruction is announced. An
// input event arrives per mouse MOVE, so an unbounded notice would bury the
// session in a message about the one thing it cannot do.
const inputBlockNoticeEvery = 60 * time.Second

// noteInputBlocked tells the session, at most occasionally, that its input is
// going nowhere and why. Silence is the actual defect being fixed here: taps
// that vanish read as a broken mirror, and the true cause — an elevated window
// holding focus, which Windows will not let us inject past — is invisible from
// the viewer and unguessable from the host.
//
// It goes through broadcastNotice so it lands in the terminal both sides can
// see, with no protocol change and nothing to deploy: the viewer showing this
// may be a browser tab from a year ago.
func (a *Agent) noteInputBlocked(who string) {
	if who == "" {
		return
	}
	a.winMu.Lock()
	fresh := who != a.inputBlockWho || time.Since(a.inputBlockAt) > inputBlockNoticeEvery
	if fresh {
		a.inputBlockWho, a.inputBlockAt = who, time.Now()
	}
	a.winMu.Unlock()
	if !fresh {
		return
	}
	a.broadcastNotice("\"" + who + "\" runs as administrator — Windows blocks remote input while it has focus. " +
		"Close it, or click it on the machine itself.")
}

// updateMenuState maintains the right-click context-menu region-capture window for
// the darwin (daemon-proxied) input path. A right-click arms a brief region grab
// around the window so the OS-drawn menu (a separate overlapping window) shows in
// the mirror; a left-click clears it. Only a right-click resolves the bounds.
func (a *Agent) updateMenuState(plaintext []byte) {
	var ev struct {
		ID     string `json:"id"`
		Kind   string `json:"kind"`
		Button string `json:"button"`
	}
	if json.Unmarshal(plaintext, &ev) != nil || ev.Kind != "click" {
		return
	}
	if ev.Button == "right" {
		w, err := findWindow(a.windows(), ev.ID)
		if err != nil {
			return
		}
		a.winMu.Lock()
		if a.winMenu == nil {
			a.winMenu = map[string]winMenuState{}
		}
		a.winMenu[w.ID] = winMenuState{x: w.X, y: w.Y, w: w.W, h: w.H, until: time.Now().Add(winMenuHold)}
		a.winMu.Unlock()
		return
	}
	a.winMu.Lock()
	delete(a.winMenu, ev.ID)
	a.winMu.Unlock()
}

// handleWindowInput injects a mouse/keyboard event into the target window.
func (a *Agent) handleWindowInput(encData string) {
	plaintext, err := a.box.Decrypt(encData)
	if err != nil {
		return
	}
	if runtime.GOOS == "darwin" {
		// Inject in the daemon (sh.reminal) so one grant covers Accessibility +
		// Automation for every session, terminal or "+". Runs on the serialized
		// winOps worker, so forwarding synchronously keeps events ordered. Still
		// track the right-click context-menu window here — the capture path reads
		// a.winMenu to region-capture (through the daemon) so the menu shows.
		mirrorForwardInput(string(plaintext))
		a.updateMenuState(plaintext)
		a.noteWindowFlush(windowInputID(plaintext))
		return
	}
	var ev windowInput
	if json.Unmarshal(plaintext, &ev) != nil {
		return
	}
	b := a.windows()
	if b.unsupported() != "" {
		return
	}
	a.noteInputBlocked(inputBlocker(ev.ID))
	a.noteWindowFlush(ev.ID)
	applyWindowInput(b, &a.winInput, ev, func(w winInfo, right bool) {
		// A right-click opens a context menu drawn as its own window (missed by
		// capture-by-id). Snapshot this window's bounds and region-capture it
		// briefly so the menu shows; a left click dismisses it.
		a.winMu.Lock()
		defer a.winMu.Unlock()
		if a.winMenu == nil {
			a.winMenu = map[string]winMenuState{}
		}
		if right {
			a.winMenu[w.ID] = winMenuState{x: w.X, y: w.Y, w: w.W, h: w.H, until: time.Now().Add(winMenuHold)}
		} else {
			delete(a.winMenu, w.ID)
		}
	})
}

// handleWindowAck records a viewer's ack of a rendered frame, unblocking the
// next frame for that window (see streamWindow). Best-effort: an unknown id or a
// momentarily full channel just drops, since only the newest seq matters.
// Acks arrive over WS (encrypted) or, when a WebRTC DataChannel is up, over that
// channel as plaintext JSON — both funnel into deliverWindowAck.
func (a *Agent) handleWindowAck(encData string) {
	plaintext, err := a.box.Decrypt(encData)
	if err != nil {
		return
	}
	var ev struct {
		ID  string `json:"id"`
		Seq uint64 `json:"seq"`
		Key bool   `json:"key"`
	}
	if json.Unmarshal(plaintext, &ev) != nil {
		return
	}
	if ev.Key {
		a.requestWindowKey(ev.ID)
	}
	a.deliverWindowAck(ev.ID, ev.Seq)
}

// windowInputID pulls the target window id out of an already-decrypted
// window_input payload. Used to mark the stream for an immediate relay flush
// without a second JSON parse of the full event.
func windowInputID(plaintext []byte) string {
	var ev struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(plaintext, &ev) != nil {
		return ""
	}
	return ev.ID
}

// noteWindowFlush asks the window's stream to ship its next relay batch now.
// Viewer input just happened; waiting out the 200ms billing interval would
// hold the frame that contains the click. No-op if that window is not live.
func (a *Agent) noteWindowFlush(id string) {
	if id == "" {
		return
	}
	a.winMu.Lock()
	f := a.winFlush[id]
	a.winMu.Unlock()
	if f != nil {
		f.Store(true)
	}
}

// requestWindowKey marks a window's stream as needing an immediate keyframe.
// A viewer raises this when it detects a gap in the frame sequence: its decoder
// has diverged from the encoder and will render smears until a self-contained
// IDR arrives. Coalesced into a single flag — ten gapped frames need one key,
// not ten. No-op for an unknown id or a JPEG stream (every frame is a key).
func (a *Agent) requestWindowKey(id string) {
	a.winMu.Lock()
	f := a.winKeyReq[id]
	a.winMu.Unlock()
	if f != nil {
		f.Store(true)
	}
}

// deliverWindowAck feeds a decoded (id, seq) ack to the window's pacing channel.
func (a *Agent) deliverWindowAck(id string, seq uint64) {
	a.winMu.Lock()
	ch := a.winAck[id]
	a.winMu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- seq:
	default:
		// Full — drop the oldest so the newest seq still gets through.
		select {
		case <-ch:
		default:
		}
		select {
		case ch <- seq:
		default:
		}
	}
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// addWindowSub records that a viewer wants this window. Anonymous viewers (an
// older build, which sends no id) are not tracked: nothing can be attributed to
// them, so they keep the behaviour they were written against.
func (a *Agent) addWindowSub(id, viewer string) {
	if viewer == "" {
		return
	}
	a.winMu.Lock()
	defer a.winMu.Unlock()
	if a.winSubs == nil {
		a.winSubs = map[string]map[string]bool{}
	}
	if a.winSubs[id] == nil {
		a.winSubs[id] = map[string]bool{}
	}
	a.winSubs[id][viewer] = true
}

// dropWindowSub removes a viewer's interest and reports whether the stream
// should be LEFT RUNNING because somebody else still wants it.
//
// A window has one stream shared by every viewer of it, and a stop used to end
// it outright. So two people watching the same window, one of them closing the
// pane, froze the other's picture until its own stall detection re-asked for it
// — around ten seconds of a still image with nothing to say why.
func (a *Agent) dropWindowSub(id, viewer string) bool {
	a.winMu.Lock()
	defer a.winMu.Unlock()
	subs := a.winSubs[id]
	if viewer == "" || subs == nil {
		// Unattributable, so it has to be taken at face value: stop, and forget
		// everyone, since we cannot know who is left.
		delete(a.winSubs, id)
		return false
	}
	delete(subs, viewer)
	if len(subs) == 0 {
		delete(a.winSubs, id)
		return false
	}
	return true
}

// forgetWindowSubs clears a window's subscribers when its stream ends for any
// other reason, so a later start is not judged against a stale set.
func (a *Agent) forgetWindowSubs(id string) {
	a.winMu.Lock()
	if id == "" {
		a.winSubs = nil
	} else {
		delete(a.winSubs, id)
	}
	a.winMu.Unlock()
}

// startWindowStream launches a capture goroutine for the given window unless
// one is already running for it. Multiple windows can stream concurrently.
func (a *Agent) startWindowStream(id, viewer string) {
	a.addWindowSub(id, viewer)
	b := a.windows()
	if b.unsupported() != "" {
		return
	}
	w, err := findWindow(b, id)
	if err != nil {
		return
	}
	a.winMu.Lock()
	if a.winStreams == nil {
		a.winStreams = map[string]chan struct{}{}
		a.winAck = map[string]chan uint64{}
		a.winKeyReq = map[string]*atomic.Bool{}
		a.winFlush = map[string]*atomic.Bool{}
	}
	// Keep this independent of winStreams for tests and hot-restart state made
	// by an older image, where the pre-existing maps do not include quality.
	if a.winQuality == nil {
		a.winQuality = map[string]chan windowQuality{}
	}
	if a.winFlush == nil {
		a.winFlush = map[string]*atomic.Bool{}
	}
	if _, ok := a.winStreams[id]; ok {
		a.winMu.Unlock() // already streaming this window
		// Somebody new just asked for it, though. H.264 deltas are useless
		// without the frames they were built from, so a viewer joining a
		// stream already in flight has nothing to decode until the next IDR —
		// which it will not ask for either, because its gap detection needs a
		// sequence cursor it has yet to establish. Hand it an entry point.
		a.requestWindowKey(id)
		return
	}
	stop := make(chan struct{})
	// Buffered so an incoming ack never blocks the reader goroutine; streamWindow
	// only cares about the newest seq, so a slot or two is plenty.
	ack := make(chan uint64, 4)
	quality := make(chan windowQuality, 1)
	keyReq := &atomic.Bool{}
	flush := &atomic.Bool{}
	a.winStreams[id] = stop
	a.winAck[id] = ack
	a.winQuality[id] = quality
	a.winKeyReq[id] = keyReq
	a.winFlush[id] = flush
	// First window under mirror → keep the display awake so the host can't
	// idle-lock and strand remote control (see winAwake).
	if a.winAwake == nil {
		a.winAwake = keepawake.StartDisplay()
	}
	a.winMu.Unlock()

	go a.streamWindow(w, stop, ack, quality, keyReq, flush)
}

// stopWindowStream ends the stream for one window id (its pane was closed).
// An empty id stops every stream (viewer left / connection dropped).
func (a *Agent) stopWindowStream(id string) {
	a.forgetWindowSubs(id)
	a.winMu.Lock()
	if id == "" {
		for k, ch := range a.winStreams {
			close(ch)
			delete(a.winStreams, k)
		}
		a.winAck = map[string]chan uint64{}
		a.winQuality = map[string]chan windowQuality{}
		a.winKeyReq = map[string]*atomic.Bool{}
		a.winFlush = map[string]*atomic.Bool{}
	} else if ch, ok := a.winStreams[id]; ok {
		close(ch)
		delete(a.winStreams, id)
		delete(a.winAck, id)
		delete(a.winQuality, id)
		delete(a.winKeyReq, id)
		delete(a.winFlush, id)
	}
	// Last window stopped → let the display sleep/lock again. Capture and run the
	// stop func outside the lock (it kills+waits a child process).
	var release func()
	if len(a.winStreams) == 0 && a.winAwake != nil {
		release, a.winAwake = a.winAwake, nil
	}
	a.winMu.Unlock()
	if release != nil {
		release()
	}
}

// setStayUnlocked holds or releases a session-wide display-sleep inhibitor so
// the host can't idle-lock (see keepawake.StartDisplay). Driven by the "always
// unlocked" setting/env at startup and toggled live by the stayunlock control
// command. Idempotent.
func (a *Agent) setStayUnlocked(on bool) {
	a.stayMu.Lock()
	defer a.stayMu.Unlock()
	if on && a.stayAwake == nil {
		a.stayAwake = keepawake.StartDisplay()
	} else if !on && a.stayAwake != nil {
		a.stayAwake()
		a.stayAwake = nil
	}
}

// winLookupReuseTTL is how long a resolved window may be reused for the
// repeating event kinds before it is looked up again — long enough to cover a
// continuous gesture, short enough that a window which moved or closed is
// noticed within one.
const winLookupReuseTTL = 400 * time.Millisecond

// Resolved-window cache. findWindow enumerates EVERY window on the system
// through an osascript — measured at ~114ms on an idle machine — and
// both injection paths ran it for every event. The viewer emits a scroll
// roughly every 50ms while a finger moves, so the lookup ALONE put the host
// more than twice behind the input rate: the backlog grew for as long as the
// gesture lasted and kept draining after the finger stopped, which is what
// made scrolling overshoot rather than merely lag.
type winLookupEntry struct {
	w  winInfo
	at time.Time
}

var (
	winLookupMu    sync.Mutex
	winLookupCache = map[string]winLookupEntry{}
)

// resolveWindowFor resolves a window id for an input event. The repeating
// event kinds reuse a recent lookup; anything that depends on exact, current
// geometry re-resolves. Entries expire with winLookupReuseTTL, so a window
// that moves, resizes or closes is picked up within one gesture's worth of
// time — the same bound that already governs re-raising.
func resolveWindowFor(b windowBackend, id string, reuse bool) (winInfo, error) {
	now := time.Now()
	if reuse {
		winLookupMu.Lock()
		e, ok := winLookupCache[id]
		winLookupMu.Unlock()
		if ok && now.Sub(e.at) < winLookupReuseTTL {
			return e.w, nil
		}
	}
	w, err := findWindow(b, id)
	if err != nil {
		winLookupMu.Lock()
		delete(winLookupCache, id) // gone — don't let a stale entry outlive it
		winLookupMu.Unlock()
		return w, err
	}
	winLookupMu.Lock()
	for k, e := range winLookupCache { // opportunistic sweep; the map stays tiny
		if now.Sub(e.at) > winLookupReuseTTL {
			delete(winLookupCache, k)
		}
	}
	winLookupCache[id] = winLookupEntry{w: w, at: now}
	winLookupMu.Unlock()
	return w, nil
}

// reuseLookupFor decides whether an event may reuse a recently resolved
// window instead of paying the ~114ms enumeration again. The rule is whether
// the event depends on the window's CURRENT rectangle:
//
//   - scroll repeats many times a second and only moves content under a fixed
//     pointer; key doesn't use geometry at all.
//   - a live drag re-resolves once at "begin"; the window cannot meaningfully
//     move out from under a drag the user is performing on it, and re-resolving
//     per move is exactly the cost the phased protocol exists to avoid.
//   - a click, and the legacy whole-path drag, place a pointer at an exact
//     spot. A stale rectangle there is a click in the wrong place.
func reuseLookupFor(kind, phase string) bool {
	switch kind {
	case "scroll", "key":
		return true
	case "drag":
		return phase == "move" || phase == "end"
	}
	return false
}

// windowInput is one decrypted viewer input event. Both paths that inject input
// — the in-process backend and the daemon — decode into this and hand it to
// applyWindowInput, so there is one description of the wire format and one
// implementation of what each kind means.
type windowInput struct {
	ID      string       `json:"id"`
	Kind    string       `json:"kind"`
	X       float64      `json:"x"`
	Y       float64      `json:"y"`
	Dx      float64      `json:"dx"`
	Dy      float64      `json:"dy"`
	Path    [][2]float64 `json:"path"`
	Text    string       `json:"text"`
	Special string       `json:"special"`
	Button  string       `json:"button"` // "right" for the secondary button; else left
	Count   int          `json:"count"`  // viewer-reported click count (1/2/3); 0 = unset
	Phase   string       `json:"phase"`  // live-drag phase: begin, move, end
}

// clickRun turns a stream of clicks into native single/double/triple clicks for
// viewers too old to report a count themselves. Extracted so both injection
// paths have it: the daemon — the path macOS actually takes — had no fallback,
// so an older viewer never got a double-click on a Mac.
type clickRun struct {
	mu    sync.Mutex
	n     int
	at    time.Time
	lastX int
	lastY int
}

// count returns the click-state (1=single, 2=double, 3=triple) for a click at
// (fx, fy) in window w, by timing it against the previous click — mirroring how
// the OS coalesces rapid clicks in one spot. Mutex-guarded: the daemon serves
// each input on its own connection, concurrently.
func (c *clickRun) count(w winInfo, fx, fy float64) int {
	x := w.X + int(fx*float64(w.W))
	y := w.Y + int(fy*float64(w.H))
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if now.Sub(c.at) < 450*time.Millisecond && absInt(x-c.lastX) <= 4 && absInt(y-c.lastY) <= 4 {
		c.n++
		if c.n > 3 {
			c.n = 1 // wrap after triple; further clicks start fresh
		}
	} else {
		c.n = 1
	}
	c.at, c.lastX, c.lastY = now, x, y
	return c.n
}

// dragStallTimeout is how long a live drag may go without a move or an end
// before the button is released for it. The viewer sends moves continuously
// while a finger is down, so a gap this long means the gesture is not coming
// back.
const dragStallTimeout = 5 * time.Second

// dragGuard releases a held mouse button when a live drag stops arriving.
//
// Phased drags press on "begin" and release on "end", which leaves the button
// down in between — and the host desktop grabbed if the end never comes. The
// existing releases only fire when the last viewer leaves or a pane is closed,
// so a socket that drops mid-drag and then RECONNECTS strands the button
// indefinitely: the viewer count never reaches zero and no pane is stopped.
// The batched drag this replaced could not do that, because it was one replay
// that always ended with a mouse-up.
type dragGuard struct {
	mu    sync.Mutex
	timer *time.Timer
}

// arm starts (or extends) the watchdog. release is called if the drag goes
// quiet, and must be safe to call from a timer goroutine.
func (g *dragGuard) arm(release func()) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.timer != nil {
		g.timer.Stop()
	}
	g.timer = time.AfterFunc(dragStallTimeout, release)
}

// disarm cancels the watchdog — the drag ended as it should have.
func (g *dragGuard) disarm() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.timer != nil {
		g.timer.Stop()
		g.timer = nil
	}
}

// inputState is the per-injector state applyWindowInput carries between events:
// which window is in front, the run of rapid clicks, and the live-drag
// watchdog. Bundled because there are two injectors — the agent and the daemon
// — and each needs its own of all three; passing them separately grew the call
// signature every time one was added.
type inputState struct {
	front  frontWindowTracker
	clicks clickRun
	drag   dragGuard
}

// phasedDragger is a backend that can inject a drag as it happens rather than
// replaying a path afterwards. Optional: a backend without it gets the batched
// form, and only hosts whose backend has it advertise drag_phases.
type phasedDragger interface {
	dragPhase(w winInfo, phase string, fx, fy float64) error
}

// dragWatch (re)arms the stall watchdog to release at the last point the drag
// reached, so an abandoned gesture lifts where the finger was rather than
// somewhere it never went.
func (st *inputState) dragWatch(pd phasedDragger, w winInfo, fx, fy float64) {
	st.drag.arm(func() { _ = pd.dragPhase(w, "up", fx, fy) })
}

// Bounds on what one viewer event may ask the host to do. The viewer sends far
// less than any of these — a phased drag carries at most a few points, typed
// runs are chunked — but a viewer is not something to take sizes from on
// trust. It is a remote party whose messages the relay merely forwards, and
// the work each field commands is not proportional to the field's own size:
// every point of a phased drag is a separate injection, and a typed run
// becomes a script argument.
//
// Truncating rather than rejecting: an oversized event is far likelier to be a
// bug at the other end than an attack, and dropping it whole would lose a real
// gesture where clipping it loses only its tail.
const (
	winMaxDragPoints = 64   // per phased event; each point is an injection
	winMaxPathPoints = 512  // a whole-path drag becomes one script, so cheaper
	winMaxTypeRunes  = 4096 // one typed run
	winMaxClickCount = 3    // single, double, triple — nothing beyond means anything
)

// clampPath limits a viewer-supplied point list.
func clampPath(pts [][2]float64, max int) [][2]float64 {
	if len(pts) > max {
		return pts[:max]
	}
	return pts
}

// clampText limits a viewer-supplied string by RUNES, so a cut never lands
// inside a multi-byte character and hands the host invalid UTF-8.
func clampText(s string, max int) string {
	if len(s) <= max { // bytes >= runes, so this is a cheap early out
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

// applyWindowInput injects one viewer event. front keeps a window from being
// re-raised while it is already in front; clicks supplies a count for viewers
// that don't report one; noteMenu (optional) records a right-click so the menu
// it opens can be region-captured.
//
// This exists once because it used to exist twice — here and in the daemon —
// and the copies drifted every time: only one learned the raise rule, only one
// learned phased drags, only one had a click-count fallback. The platform that
// runs the daemon copy was the one left behind each time.
func applyWindowInput(b windowBackend, st *inputState, ev windowInput, noteMenu func(w winInfo, right bool)) {
	w, err := resolveWindowFor(b, ev.ID, reuseLookupFor(ev.Kind, ev.Phase))
	if err != nil {
		return
	}
	switch ev.Kind {
	case "click":
		count := ev.Count
		if count < 1 {
			count = st.clicks.count(w, ev.X, ev.Y)
		}
		if count > winMaxClickCount {
			count = winMaxClickCount
		}
		right := ev.Button == "right"
		_ = b.focus(w)
		st.front.note(ev.ID)
		_ = b.clickN(w, ev.X, ev.Y, count, right)
		if noteMenu != nil {
			noteMenu(w, right)
		}
	case "drag":
		pd, phased := b.(phasedDragger)
		switch {
		case ev.Phase == "":
			// The viewer buffered the gesture and sent it after the pointer
			// lifted; replayed at scripted speed.
			_ = b.focus(w)
			st.front.note(ev.ID)
			_ = b.drag(w, clampPath(ev.Path, winMaxPathPoints))
		case !phased:
			// This backend never advertised drag_phases, so a phased event
			// means a viewer got it wrong. Acting on it would press and release
			// once per chunk — a burst of clicks, not a drag.
			return
		case ev.Phase == "begin":
			_ = b.focus(w)
			st.front.note(ev.ID)
			_ = pd.dragPhase(w, "down", ev.X, ev.Y)
			st.dragWatch(pd, w, ev.X, ev.Y)
		case ev.Phase == "move":
			// Each accumulated point in turn, so the target sees the real path
			// rather than a jump to wherever the finger ended up.
			for _, p := range clampPath(ev.Path, winMaxDragPoints) {
				_ = pd.dragPhase(w, "move", p[0], p[1])
			}
			if n := len(ev.Path); n > 0 {
				st.dragWatch(pd, w, ev.Path[n-1][0], ev.Path[n-1][1])
			} else {
				st.dragWatch(pd, w, ev.X, ev.Y)
			}
		case ev.Phase == "end":
			for _, p := range clampPath(ev.Path, winMaxDragPoints) {
				_ = pd.dragPhase(w, "move", p[0], p[1])
			}
			// Compatibility with viewers that began a drag on the first
			// threshold-crossing pointermove but did not include that movement in
			// the end packet. A down followed by an up at another coordinate is
			// not a drag to many apps; guarantee at least one dragged event.
			if len(ev.Path) == 0 {
				_ = pd.dragPhase(w, "move", ev.X, ev.Y)
			}
			st.drag.disarm()
			_ = pd.dragPhase(w, "up", ev.X, ev.Y)
		}
	case "scroll":
		if st.front.needsRaise(ev.ID) {
			_ = b.focus(w)
		}
		_ = b.scroll(w, ev.X, ev.Y, ev.Dx, ev.Dy)
	case "key":
		// Keys land on whatever is focused; the target was focused by clicking
		// it, so no raise per keystroke — that would add a window raise to
		// every character.
		if ev.Special != "" {
			_ = b.key(w, ev.Special)
		} else if ev.Text != "" {
			_ = b.typeText(w, clampText(ev.Text, winMaxTypeRunes))
		}
	}
}

// frontWindowTracker remembers which window was last brought to the front, so
// an event only pays for a raise when it actually changes the front window.
//
// Raising is expensive out of all proportion to what it does: on macOS focus()
// runs an AppleScript that walks EVERY window of the target application over
// the Accessibility bridge reading each one's position and size — measured at
// ~410ms against a browser, against ~20ms for the injection itself. A whole
// desktop skips it (nothing to raise), which is the entire reason scrolling
// the desktop felt immediate and scrolling a window did not.
//
// The rule used to be "skip the raise if the last scroll was under 400ms ago",
// which could never work: at ~570ms an event, scrolls queued further apart
// than the gap they were tested against, so every one looked like a fresh
// gesture and paid again. The cost being avoided defeated the test for
// avoiding it. Keying on WHICH window is in front has no such feedback.
//
// One implementation, deliberately: this decision previously existed twice —
// once here and once in the daemon — and only the copy that never runs on
// macOS was correct.
type frontWindowTracker struct {
	mu sync.Mutex
	id string
	at time.Time
}

// frontWindowTTL bounds how long the record is trusted. Nothing reports when
// the person at the host clicks something themselves, so it has to go stale on
// its own.
const frontWindowTTL = 4 * time.Second

// needsRaise reports whether id must be raised, and records that it now is.
func (t *frontWindowTracker) needsRaise(id string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if id == t.id && time.Since(t.at) < frontWindowTTL {
		t.at = time.Now() // still in front; keep the record fresh
		return false
	}
	t.id, t.at = id, time.Now()
	return true
}

// note records a raise performed for its own sake (a click, a drag), so a
// scroll that follows knows the window is already in front.
func (t *frontWindowTracker) note(id string) {
	t.mu.Lock()
	t.id, t.at = id, time.Now()
	t.mu.Unlock()
}

// windowFrameInterval is a floor on the frame period — a cheap-to-capture window
// with instant acks could otherwise spin a core respawning the capture tool. The
// real pacing is ack-driven (see streamWindow), so this only caps the ceiling at
// ~16 fps; the usual capture cost (import/screencapture ~130ms) sits well above
// it, making the floor a no-op in practice.
const windowFrameInterval = 60 * time.Millisecond

// ackWaitTimeout bounds how long streamWindow waits for a frame ack before
// proceeding anyway. It keeps the stream alive if an ack is lost or the viewer
// is an older build that never acks (it just runs slower), and lets a
// backgrounded viewer trickle instead of freezing.
const ackWaitTimeout = 800 * time.Millisecond

// maxFramesInFlight caps how many sent-but-unacked frames a stream allows. 1
// would be strict lock-step (every frame waits a full round-trip, serializing
// capture and network); 2 lets the next capture overlap the previous frame's
// ack round-trip, so throughput stays near the capture rate while latency is
// still bounded — it can't accumulate past this many frames on a slow link.
const maxFramesInFlight = 2

// maxFramesInFlightH264 is the same gate for h264 streams. Video frames are
// ~10× smaller than JPEGs (a delta AU is a few KB), so a deeper pipeline costs
// little buffered latency but keeps the stream flowing when the ack round-trip
// is slower than a frame interval. The window must cover RTT×fps or throughput
// becomes RTT-bound (2-in-flight at 100ms RTT caps ~20fps); at 60fps this
// covers a 200ms round trip, and it's a ceiling — only reached on a slow link.
const maxFramesInFlightH264 = 12

// wsFrameMinInterval caps the window-frame rate on the BILLED WebSocket relay
// path, independent of the peer-to-peer DataChannel rate. The native capture
// helper can push ~30fps, and P2P viewers get all of it for free — but every WS
// frame is a paid relay message, so a WS-only viewer (P2P didn't connect) is
// throttled to ~5fps to keep Cloudflare cost bounded no matter how fast the host
// captures. The heartbeat isn't capped (it's tiny and only every few seconds).
const wsFrameMinInterval = 200 * time.Millisecond

// winCapAnnounceInterval is how often a viewer with nothing open re-asserts
// that fact (see announceCaps in the viewer). Every one of those crosses the
// billed relay, so it is as slow as viewerCapTTL allows rather than as fast as
// convenient.
const winCapAnnounceInterval = 25 * time.Second

// streamNoWatcherTimeout stops a stream once every connected viewer has said it
// has no pane open for this long.
//
// The ack-idle timeout below cannot do this. It is evaluated only inside the
// in-flight gate, which a stream enters only when it is AHEAD of the viewer —
// and an idle window emits no frames, so its sequence never advances, the gate
// is never entered, and the check never runs. A busy unwatched window reaps
// itself in seconds; a quiet one never did.
//
// That went unnoticed while every pane sent a stop when it closed. Popping a
// window out deliberately does not, so the popup can adopt the stream instead
// of racing to restart it — which means a popup that never arrives (blocked,
// or closed at once) leaves a capture helper and its encoder running for a
// window nobody is watching, heartbeating over the billed relay and holding
// the display awake through winAwake.
//
// It must exceed viewerCapTTL. A viewer that goes away leaves its last report
// behind, and a report of "no panes" from a viewer that is no longer there
// subtracts from the count of viewers wanting frames just as a live one does —
// so with a shorter timeout than the record's lifetime, a ghost could hold the
// total at zero long enough to reap a stream someone was still watching. The
// record expiring first is what makes that impossible.
const streamNoWatcherTimeout = 120 * time.Second

// streamAckIdleTimeout stops a stream whose viewer was acking frames but then
// went silent this long. An unclean viewer drop (phone asleep, network lost,
// laptop closed) sends no WebSocket close, so the relay can be slow to tell the
// agent count==0 — meanwhile we'd keep capturing and sending frames to a viewer
// that isn't there, burning relay requests with nobody watching. Only checked
// while we're actively sending frames (an idle window sends none and expects no
// acks); a returning viewer re-sends window_ctl start. Only armed once the
// viewer has acked at least once, so a hypothetical non-acking client is never
// cut off.
const streamAckIdleTimeout = 12 * time.Second

// Change-detection knobs. A captured frame is reduced to a winSigN×winSigN
// grid of per-cell averages across THREE channels — luma (Y) and both chroma
// planes (Cb, Cr) — box-averaged so localized noise averages out; if no cell
// moved by winSigThreshold or more in ANY channel since the last SENT frame,
// the window is treated as unchanged and the frame is skipped — sparing the
// relay a frame message and its ack (each is a billed Durable Object request).
// Chroma matters: a colour-only change (a status dot green→red, syntax
// recolouring) can leave luma nearly constant, so a luma-only signature would
// miss it entirely and freeze the pane until something else forced a frame.
// The grid is deliberately fine (winSigN cells across ~1100px ≈ 23px/cell) so a
// small localized edit — a few characters, a caret, a spinner — occupies a big
// enough fraction of its cell to shift that cell's average past the threshold,
// instead of being diluted to nothing by averaging over a large block. So we
// only ever skip visually identical frames and don't drop small/colour changes.
// An unchanged window still sends one frame per winMinFrameInterval — change
// detection decides which frames are worth sending EARLY, never whether a pane
// gets frames at all; winHeartbeat only governs a tiny liveness ping (see
// streamWindow) so the viewer knows the host is alive between frames.
// winProbeFrame is the one exception: while a DataChannel is open but not yet
// confirmed to carry frames, we send a real frame this often even if nothing
// changed — otherwise a static window would never give the channel a frame to
// prove itself and P2P would never engage. Once confirmed, it's back to 0 fps.
const (
	winSigN         = 48
	winSigThreshold = 12
	winHeartbeat    = 3 * time.Second
	winProbeFrame   = 1 * time.Second
	// winMenuHold is how long after a right-click we region-capture a window so
	// its context menu shows. Long enough to read/aim at a menu; a left click
	// (dismiss or item-select) ends it sooner. See Agent.winMenu.
	winMenuHold = 8 * time.Second
	// winLiveCheck is how often, while a mirrored window's picture is static, we
	// re-verify it's still open (see streamWindow). Bounds how long a closed
	// window can sit frozen before its pane is dropped, without spending an
	// existence check on every idle frame.
	winLiveCheck = 1200 * time.Millisecond
	// winMinFrameInterval floors delivery at 1fps on the subprocess capture
	// path: after a second without a sent frame, the newest frame is re-sent
	// even though the change detector saw nothing. Both detectors — SCK's
	// frame status and the signature grid — have missed real changes in the
	// field, and a miss used to freeze the pane until the next detected
	// change. One frame per idle second bounds that to a second of staleness,
	// and costs nothing while nobody watches: streams stop without viewers.
	// The native helper keeps its own 1fps floor (startIdleRefresh), which
	// additionally RE-CAPTURES rather than re-sends, so its floor also heals
	// misses inside SCK itself.
	winMinFrameInterval = time.Second
)

// frameSig is a coarse per-cell average of a frame across the luma (Y) and both
// chroma (Cb, Cr) planes. Keeping chroma is what lets sigDiffers catch a change
// that shifts colour but not brightness (green→red status dot, recoloured text)
// — a luma-only signature is blind to those and would freeze the pane.
type frameSig struct {
	y, cb, cr [winSigN * winSigN]byte
}

// frameSignature reduces a JPEG frame to a coarse frameSig by box-averaging each
// cell. It reads the Y/Cb/Cr planes directly for JPEG's native YCbCr (chroma is
// subsampled, so several luma pixels share a chroma sample — fine for a coarse
// average); a non-YCbCr image falls back to a per-pixel RGB→YCbCr conversion. ok
// is false when the frame can't be decoded, in which case the caller treats the
// frame as changed (always sends).
func frameSignature(jpegBytes []byte) (sig frameSig, ok bool) {
	img, err := jpeg.Decode(bytes.NewReader(jpegBytes))
	if err != nil {
		return sig, false
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w == 0 || h == 0 {
		return sig, false
	}
	var sumY, sumCb, sumCr [winSigN * winSigN]uint64
	var cnt [winSigN * winSigN]uint32
	if yc, isYCbCr := img.(*image.YCbCr); isYCbCr {
		for y := 0; y < h; y++ {
			cy := y * winSigN / h
			for x := 0; x < w; x++ {
				idx := cy*winSigN + x*winSigN/w
				sumY[idx] += uint64(yc.Y[yc.YOffset(b.Min.X+x, b.Min.Y+y)])
				co := yc.COffset(b.Min.X+x, b.Min.Y+y)
				sumCb[idx] += uint64(yc.Cb[co])
				sumCr[idx] += uint64(yc.Cr[co])
				cnt[idx]++
			}
		}
	} else {
		for y := 0; y < h; y++ {
			cy := y * winSigN / h
			for x := 0; x < w; x++ {
				r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
				yy, cb, cr := color.RGBToYCbCr(uint8(r>>8), uint8(g>>8), uint8(bl>>8))
				idx := cy*winSigN + x*winSigN/w
				sumY[idx] += uint64(yy)
				sumCb[idx] += uint64(cb)
				sumCr[idx] += uint64(cr)
				cnt[idx]++
			}
		}
	}
	for i := range sig.y {
		if cnt[i] > 0 {
			sig.y[i] = byte(sumY[i] / uint64(cnt[i]))
			sig.cb[i] = byte(sumCb[i] / uint64(cnt[i]))
			sig.cr[i] = byte(sumCr[i] / uint64(cnt[i]))
		}
	}
	return sig, true
}

// sigDiffers reports whether any grid cell changed by at least winSigThreshold
// in ANY of the three channels (luma or either chroma) since the last signature.
func sigDiffers(a, b frameSig) bool {
	for i := range a.y {
		if absDiffByte(a.y[i], b.y[i]) >= winSigThreshold ||
			absDiffByte(a.cb[i], b.cb[i]) >= winSigThreshold ||
			absDiffByte(a.cr[i], b.cr[i]) >= winSigThreshold {
			return true
		}
	}
	return false
}

func absDiffByte(x, y byte) int {
	d := int(x) - int(y)
	if d < 0 {
		d = -d
	}
	return d
}

// streamWindow captures w and broadcasts each JPEG frame to viewers until stop
// is closed or the connection goes away. It is ack-paced: after sending a frame
// it waits for the viewer to acknowledge rendering it before capturing the next,
// so at most one frame is ever in flight. That bounds latency (it can't
// accumulate on a slow link — the classic "the lag just keeps growing" failure)
// and makes the frame rate adapt to whatever the viewer can actually consume,
// while guaranteeing every frame sent is freshly captured. On exit it clears its
// own map entry (unless already replaced) so the id can be re-streamed.
//
// wsSinkNeeded reports whether a window frame must ALSO be sent over the
// (per-message-billed) WS relay: true whenever some viewer isn't covered by a
// confirmed P2P DataChannel — none confirmed yet, an unknown/zero viewer count,
// or more viewers than confirmed channels (a WS-only viewer). Only an all-P2P
// viewer set skips WS, so P2P still keeps frames off the relay when everyone can
// use it, while a mixed set never leaves a WS-only viewer frozen.
func wsSinkNeeded(viewerCount, confirmed int) bool {
	return confirmed == 0 || viewerCount <= 0 || viewerCount > confirmed
}

// helperRetryCooldown is how long a stream waits before re-attempting the
// native capture helper after it failed to start or died. A helper failure —
// SCK transiently losing the window (moved to another Space, minimized), a
// crash — used to demote the stream to the ~5fps screencapture path silently
// and PERMANENTLY; now it just degrades until the next retry succeeds.
const helperRetryCooldown = 10 * time.Second

// helperUnavailableRetry is the pause after a failure that only means the
// daemon wasn't reachable. A restart or upgrade is over in about a second, so
// the full helperRetryCooldown just leaves the pane showing a capture error
// long after capture became possible again.
const helperUnavailableRetry = 500 * time.Millisecond

// helperFastDeath is how soon after starting a helper's death is taken as
// evidence against the codec rather than as ordinary churn. A long-lived
// helper dying is a daemon restart or window change; one that dies immediately
// failed to set itself up.
const helperFastDeath = 3 * time.Second

// h264SuspendMax caps the backoff below. A machine that keeps failing is
// answering the question — stop asking it every minute.
const h264SuspendMax = 30 * time.Minute

// h264SuspendFor is the FIRST suspension after h264 looked broken.
// Each consecutive failure doubles it, up to h264SuspendMax, and a helper that
// starts in h264 clears the run.
//
// A fixed interval is wrong in both directions. Latching permanently — what
// this replaced — meant one misread, such as a daemon bounce caught inside
// helperFastDeath, cost the pane its video for as long as it stayed open.
// Retrying at a fixed minute forever is the mirror of that fault: on a machine
// with no usable encoder, every retry restarts the helper, fails, and restarts
// it again, so the picture hitches twice a minute for the life of the session
// to re-ask a question already answered. Backing off recovers quickly from a
// wrong guess and goes quiet on a right one.
const h264SuspendFor = 60 * time.Second

// winSinks is the resolved set of destinations for ONE frame — the single
// source of truth for who receives it. Every throttle (the billed-WS rate cap,
// the probe cadence) is applied during resolution, so "will anyone take this
// frame" and the actual sends can never disagree. They once could: probing
// channels passed the send gate while the probe throttle skipped the send, so
// seq inflated ~30/s with nothing delivered, the viewer's acks fell permanently
// behind, and the in-flight gate degraded a healthy stream to a few fps.
type winSinks struct {
	confirmed []*rtcPeer // proven peers — get every frame
	probe     []*rtcPeer // unproven — at most one frame per winProbeFrame
	ws        bool       // billed relay — at most one frame per wsFrameMinInterval
}

func (s winSinks) any() bool { return len(s.confirmed) > 0 || len(s.probe) > 0 || s.ws }

// expectsAck reports whether a frame sent to these sinks should produce a
// viewer ack: true only for proven receivers (confirmed P2P, or the relay). A
// probe-only send doesn't count — the channel is unproven, and the demote logic
// must only punish silence from viewers that demonstrably received frames.
func (s winSinks) expectsAck() bool { return len(s.confirmed) > 0 || s.ws }

// winStream is the state machine for one mirrored window. One runs per
// streamed id (see startWindowStream); all fields are goroutine-local.
type winStream struct {
	a       *Agent
	b       windowBackend
	w       winInfo
	stop    <-chan struct{}
	ack     <-chan uint64
	quality <-chan windowQuality
	profile windowQuality
	// forceSend makes the next capture ship whatever the change detector
	// thinks, and bypasses the relay pacing while it does. Raised for a viewer
	// that has joined an in-flight stream and has no picture at all; cleared
	// only once a frame has actually gone out.
	forceSend bool
	// keyReq is raised by a viewer that saw a sequence gap and needs a fresh
	// IDR to resync its decoder (see requestWindowKey).
	keyReq *atomic.Bool
	// flush is raised by viewer input so the next relay batch ships immediately
	// instead of waiting out wsFrameMinInterval (see noteWindowFlush).
	flush *atomic.Bool
	// shipNow is set when input just landed: drop unsent pre-click AUs, re-key,
	// and send the next frame without waiting for the billing interval. Does
	// not add messages — it only moves the next one earlier.
	shipNow bool

	// Frame source: the native SCK helper when available (hardware capture +
	// JPEG at winHelperFPS with its own change detection), else per-frame
	// screencapture. capNative reports which produced the CURRENT frame;
	// helperErr is why the helper isn't running (stamped onto fallback frames
	// so the viewer's popover can say WHY it's on the slow path — usually a
	// locked screen or a window SCK can't see).
	helper        *winHelper
	helperRetryAt time.Time
	helperErr     string
	capNative     bool
	codec         string // "" (jpeg), or "h264" when every viewer can decode it
	wsVideo       bool   // some viewer needs the relay copy — batch video for it
	// h264BrokenUntil suspends h264 on this stream after a failure that looked
	// like the codec's fault (old daemon, no encoder). Bounded rather than
	// permanent: the evidence is a guess, and a wrong guess used to cost the
	// stream its video for as long as the pane stayed open. Re-testing once a
	// minute is cheap, and a genuinely broken encoder still can't flap.
	h264BrokenUntil time.Time
	// h264Suspend is the current backoff length; it doubles per consecutive
	// failure and resets once a helper actually starts in h264.
	h264Suspend time.Duration
	// Relay batching: AUs accumulate here and ship as ONE message per
	// wsFrameMinInterval, because the relay bills per message, not per byte.
	wsBatch         []winFrame
	wsBatchSeq0     uint64
	wsBatchBytes    int
	lastCodecSwitch time.Time // upswitch hysteresis anchor
	helperStarted   time.Time // when the current helper came up (fast-death detection)

	// Ack pacing: seq stamps sent frames, acked tracks the viewer's highest
	// confirmation. sentSinceAck counts ack-expected frames sent since the last
	// consumed ack — the demote evidence (see dispatch).
	seq, acked   uint64
	lastAck      time.Time
	gotAnyAck    bool
	sentSinceAck int

	// Change detection for the screencapture path (the helper does its own).
	// The pending signature is committed only when the frame is actually sent —
	// committing on detection would mark a skipped frame "already seen" and
	// silently drop its content.
	lastSig, pendingSig   frameSig
	haveSig, pendingSigOK bool

	// Cadence stamps.
	lastSent       time.Time // last frame OR heartbeat (paces both)
	noWatcherSince time.Time // when every viewer last reported no panes
	// geoCh carries an off-thread geometry probe back to this loop; geoBusy
	// keeps one in flight at a time. The probe enumerates every window on the
	// system, which is far too slow to do between frames.
	geoCh        chan geoResult
	geoBusy      bool
	lastWSFrame  time.Time // billed-relay rate cap
	lastProbe    time.Time // probe-frame cadence
	lastGeoCheck time.Time // window liveness / geometry poll
	geoFails     int       // consecutive failed geometry lookups (transient osascript errors)
	lastImg      []byte    // newest frame — lets a probe go out while idle
	fails        int       // consecutive capture failures while the window exists
}

// streamWindow captures w and streams JPEG frames to viewers until stop closes
// or the connection goes away. Frames ride confirmed P2P DataChannels at the
// full capture rate and the billed WS relay at a capped rate (wsFrameMinInterval)
// for viewers without P2P; unproven channels get one probe frame a second until
// the viewer acks one over them. The stream is ack-paced: at most
// maxFramesInFlight unacknowledged frames, so latency can't accumulate on a
// slow link and the rate adapts to what the viewer actually consumes.
func (a *Agent) streamWindow(w winInfo, stop <-chan struct{}, ack <-chan uint64, quality <-chan windowQuality, keyReq, flush *atomic.Bool) {
	s := &winStream{a: a, b: a.windows(), w: w, stop: stop, ack: ack, quality: quality, profile: (windowQuality{}).normalized(), keyReq: keyReq, flush: flush, lastGeoCheck: time.Now()}
	defer s.cleanup()
	s.run()
}

// cleanup clears the stream's map entry (unless already replaced) so the id can
// be re-streamed, drops any right-click region-capture entry, releases the
// keep-awake inhibitor when this was the last stream, and stops the helper.
func (s *winStream) cleanup() {
	s.flushWSBatch(s.a.liveConn())
	if s.helper != nil {
		s.helper.stop()
	}
	a := s.a
	a.winMu.Lock()
	if ch, ok := a.winStreams[s.w.ID]; ok && ch == s.stop {
		delete(a.winStreams, s.w.ID)
		delete(a.winAck, s.w.ID)
		delete(a.winQuality, s.w.ID)
		delete(a.winKeyReq, s.w.ID)
		delete(a.winFlush, s.w.ID)
	}
	delete(a.winMenu, s.w.ID)
	// A stream that exits on its own — window closed, viewer went silent —
	// bypasses stopWindowStream, so it must release the keep-awake inhibitor
	// itself when it's the last one; otherwise a window closing under mirror
	// pins the host display awake forever. Only release if we actually removed
	// the last stream (a replaced id leaves the map non-empty).
	var release func()
	if len(a.winStreams) == 0 && a.winAwake != nil {
		release, a.winAwake = a.winAwake, nil
	}
	a.winMu.Unlock()
	if release != nil {
		release()
	}
}

func (s *winStream) run() {
	for {
		// A closed stop channel must end the loop HERE, not wherever a blocking
		// call happens to notice it. Every select below treats closed-stop as
		// "return immediately" — so a replaced stream (same id reopened, see
		// startWindowStream) with spare in-flight capacity and an alive helper
		// never blocked at all: helper.next returned instantly on the closed
		// channel, the capNative path has no loop floor, and the orphaned
		// goroutine spun a full core while holding its capture stream open
		// (observed: three replaced panes pinning an agent at 300% CPU).
		select {
		case <-s.stop:
			return
		default:
		}
		conn := s.a.liveConn()
		if conn == nil {
			return
		}
		s.drainAcks()
		s.applyQuality()
		s.takeInputFlush()
		if !s.watched() {
			return
		}
		if !s.waitCapacity() {
			return
		}
		s.negotiateCodec()
		// A viewer lost sync (gap in the sequence): re-key before capturing, so
		// the very next AU it receives is a self-contained entry point.
		s.noteKeyRequest()
		start := time.Now()
		f, err := s.capture()
		switch {
		case err != nil || (len(f.Data) == 0 && !s.capNative):
			// No pixels from the subprocess path: closed window, or capture
			// genuinely broken (permissions). The helper path never lands here —
			// an empty helper read just means "no change this interval".
			if !s.captureFailure(conn, err) {
				return
			}
		case f.H264:
			// Compressed-video path: every non-empty AU is fresh content — a
			// detected change, or the helper's own 1fps idle refresh — and MUST
			// ship in order; drop policy lives in dispatchH264/winHelper, never
			// here.
			s.fails = 0
			if !s.checkWindow(conn, len(f.Data) > 0) {
				return
			}
			if len(f.Data) > 0 {
				s.dispatchH264(conn, f)
			} else {
				// Idle interval: ship anything still buffered rather than let
				// the last motion before a pause sit unsent until the next one.
				s.flushWSBatch(conn)
				if time.Since(s.lastSent) >= winHeartbeat {
					confirmed, probing, _ := s.a.rtcSinks()
					s.sendHeartbeat(conn, confirmed, probing, s.a.framesWantedBy())
				}
			}
		default:
			s.fails = 0
			// forceSend stays raised until a frame actually goes out (see
			// dispatch): a viewer with nothing on screen is not served by a
			// send that the pacing throttle then drops.
			changed := s.detectChange(f.Data) || s.forceSend
			if len(f.Data) > 0 {
				s.lastImg = f.Data
			}
			// checkWindow gets the detector's honest verdict — its existence
			// poll runs while the picture is static, and the 1fps floor below
			// must not read as "always moving" there.
			if !s.checkWindow(conn, changed) {
				return
			}
			// Change detection is a bandwidth optimization, not a delivery
			// contract: it has missed real changes in the field, and a miss
			// froze the pane until the next detected change. The floor bounds
			// any miss to a second of staleness by re-sending the newest frame
			// after a second of silence (see winMinFrameInterval).
			refresh := len(s.lastImg) > 0 && time.Since(s.lastSent) >= winMinFrameInterval
			s.dispatch(conn, changed || refresh)
		}
		// Floor the loop period so a cheap subprocess capture with instant acks
		// can't spin a core. The helper path self-paces (next blocks until SCK
		// delivers, at most winHelperFPS), so flooring it would only cap P2P.
		if !s.capNative {
			if rem := windowFrameInterval - time.Since(start); rem > 0 {
				select {
				case <-s.stop:
					return
				case <-time.After(rem):
				}
			}
		}
	}
}

// applyQuality applies the latest browser profile at a frame boundary. The
// helper is restarted only when the profile actually changes; the old canvas
// remains visible in the browser until the first sharper frame arrives.
func (s *winStream) applyQuality() {
	var newest windowQuality
	changed := false
	for {
		select {
		case newest = <-s.quality:
			changed = true
		default:
			if !changed || newest == s.profile {
				return
			}
			s.setQuality(newest)
			return
		}
	}
}

func (s *winStream) setQuality(q windowQuality) {
	q = q.normalized()
	if q == s.profile {
		return
	}
	s.profile = q
	if s.helper != nil {
		s.helper.stop()
		s.helper = nil
	}
	s.helperRetryAt = time.Time{}
	s.helperErr = ""
}

// desiredCodec picks the frame codec and delivery shape for the CURRENT sink
// topology. H.264 needs every receiver to decode it — the relay is a broadcast,
// so one incapable viewer means nobody gets video — but it is NOT restricted to
// peer-to-peer: batched over the relay it delivers 30fps for the same number of
// billed messages that one-JPEG-per-message spent on 5fps. wsVideo reports
// whether any viewer needs the relay copy this iteration.
func (s *winStream) desiredCodec() (codec string, wsVideo bool) {
	if time.Now().Before(s.h264BrokenUntil) || os.Getenv("REMINAL_NO_H264") != "" {
		return "", false
	}
	if !s.a.viewersCanH264() {
		return "", false
	}
	confirmed, _, _ := s.a.rtcSinks()
	return "h264", wsSinkNeeded(s.a.framesWantedBy(), len(confirmed))
}

// captureFPS is the frame-rate ceiling for the stream's current codec and
// delivery shape. Video affords far more frames than JPEG for the same
// bandwidth, but a stream feeding the relay stays at 30: those bytes cross
// Cloudflare rather than a direct link, and 30fps already reads as smooth.
func (s *winStream) captureFPS() int {
	if s.codec != "h264" {
		return winHelperFPS
	}
	// Spend the high-quality tier on pixels rather than redundant motion.
	// 2880-wide UI capture at 60fps exceeds the decoder throughput of many
	// phones; 30fps stays responsive and looks materially sharper.
	if s.profile.MaxWidth > 1920 {
		return 30
	}
	if s.wsVideo {
		return winHelperFPS
	}
	return winHelperFPSH264
}

// negotiateCodec restarts the frame source when the desired codec changed.
// Downswitch (h264→jpeg) is immediate — a JPEG-needing sink is waiting for
// frames right now. Upswitch is held off briefly after any switch so a
// flapping transport (probe → confirm → demote…) can't thrash helper
// restarts.
func (s *winStream) negotiateCodec() {
	want, wsVideo := s.desiredCodec()
	// The delivery shape alone changes the capture rate, so it restarts the
	// helper too — but only downward without delay (a relay viewer waiting on
	// 60fps it can't afford) and upward under the same hysteresis as a codec
	// switch (a viewer flapping between transports must not thrash restarts).
	if want == s.codec && wsVideo == s.wsVideo {
		return
	}
	if want == s.codec && wsVideo != s.wsVideo && !wsVideo && time.Since(s.lastCodecSwitch) < 3*time.Second {
		return
	}
	if want == "h264" && s.codec != "h264" && time.Since(s.lastCodecSwitch) < 3*time.Second {
		return
	}
	s.flushWSBatch(s.a.liveConn()) // never strand buffered AUs across a restart
	s.codec, s.wsVideo = want, wsVideo
	s.lastCodecSwitch = time.Now()
	if s.helper != nil {
		s.helper.stop()
		s.helper = nil
	}
	s.helperErr = ""
	s.helperRetryAt = time.Time{} // a codec switch never inherits a failure cooldown
}

// watched reports whether anyone still wants this stream, ending it when every
// viewer has reported no panes for streamNoWatcherTimeout. Viewers that have
// not reported at all are assumed to want frames, so this never acts on
// silence — only on everyone actively saying they are watching nothing.
func (s *winStream) watched() bool {
	// A viewer count of zero is "we have not heard from the relay", not
	// "nobody is here" — the same reading wsSinkNeeded takes of it, and a
	// genuine zero already stops every stream through the count transition.
	// It is also what resetViewerSize leaves behind between connections, so
	// treating it as agreement would reap live panes across a reconnect.
	if s.a.currentViewerCount() <= 0 || s.a.framesWantedBy() > 0 {
		s.noWatcherSince = time.Time{}
		return true
	}
	if s.noWatcherSince.IsZero() {
		s.noWatcherSince = time.Now()
		return true
	}
	return time.Since(s.noWatcherSince) < streamNoWatcherTimeout
}

// consumeAck folds one viewer ack into the pacing state.
func (s *winStream) consumeAck(v uint64) {
	if v > s.acked {
		s.acked = v
	}
	s.lastAck, s.gotAnyAck, s.sentSinceAck = time.Now(), true, 0
}

// drainAcks consumes every ack that arrived since the last iteration, without
// blocking. Draining only inside the in-flight gate (which is entered when 2+
// frames are outstanding) let lastAck go stale while the viewer was acking
// perfectly — which the demote logic then misread as a dead channel.
func (s *winStream) drainAcks() {
	for {
		select {
		case v := <-s.ack:
			s.consumeAck(v)
		default:
			return
		}
	}
}

// waitCapacity blocks while maxFramesInFlight frames are unacknowledged, so we
// never outrun the viewer by more than that — latency stays bounded instead of
// accumulating on a slow link. Returns false when the stream should end: stop
// closed, or a viewer that used to ack has been silent past
// streamAckIdleTimeout (it vanished uncleanly; don't stream to nobody).
func (s *winStream) waitCapacity() bool {
	limit := uint64(maxFramesInFlight)
	if s.codec == "h264" {
		// The relay acks once per 200ms batch, so more than one batch in
		// flight is just click-to-photon delay. P2P keeps the deeper
		// pipeline — those frames never touch the billed path.
		if s.wsVideo {
			limit = 6
		} else {
			limit = maxFramesInFlightH264
		}
	}
	for s.seq-s.acked >= limit {
		select {
		case v := <-s.ack:
			s.consumeAck(v)
		case <-time.After(ackWaitTimeout):
			if s.gotAnyAck && time.Since(s.lastAck) > streamAckIdleTimeout {
				return false
			}
			s.acked = s.seq // assume delivered; keep the stream moving
		case <-s.stop:
			return false
		}
	}
	return true
}

// releaseWindowInput releases any held mouse button so an interrupted click/drag
// can't leave the host's desktop grabbed. On macOS the injection ran in the daemon
// (sh.reminal), so the release must too — releasing in this session's (Terminal)
// context wouldn't touch the daemon's held press.
func (a *Agent) releaseWindowInput() {
	if runtime.GOOS == "darwin" {
		mirrorRelease()
		return
	}
	_ = a.windows().releaseInput()
}

// startCaptureHelper returns the frame source for a window. On macOS it dials the
// daemon's mirror service — so ALL capture runs in the one granted sh.reminal
// process and a single reminal.app grant covers every session (terminal or "+");
// elsewhere it spawns the capture helper directly.
func startCaptureHelper(id string, maxWidth, quality, fps int, codec string) (*winHelper, error) {
	if runtime.GOOS == "darwin" {
		return startMirrorCapture(id, maxWidth, quality, fps, codec)
	}
	return startWinHelper(id, maxWidth, quality, fps, codec)
}

// ensureHelper keeps the native capture helper alive: reaps one that died and
// (re)starts it, subject to helperRetryCooldown after a failure. An h264 helper
// that died FAST (bad framing from an old daemon that ignored the codec arg, no
// VT encoder on this machine, instant handshake failures) marks h264 broken for
// this stream and drops straight back to JPEG — without the fast-death guard the
// stream would flap h264→die→cooldown→h264 forever. A LONG-lived h264 helper
// dying is transient (daemon restart, window churn) and retries as h264.
func (s *winStream) ensureHelper() {
	if os.Getenv("REMINAL_NO_CAPTURE_HELPER") != "" {
		return
	}
	if s.helper != nil {
		if s.helper.alive() {
			return
		}
		msg := s.helper.errorText()
		if msg == "" {
			msg = "capture helper exited"
		}
		s.helperErr = msg
		badFraming := s.helper.badFraming.Load()
		s.helper.stop()
		s.helper = nil
		fast := time.Since(s.helperStarted) < helperFastDeath
		s.helperRetryAt = time.Now().Add(helperRetryCooldown)
		if !fast {
			// It ran fine and then stopped: a daemon restart, a window change.
			// Come back promptly and let the start attempt classify what is
			// wrong — sitting out ten seconds here is why every restart left
			// panes reading "capture helper exited" long after capture worked.
			s.helperRetryAt = time.Now().Add(helperUnavailableRetry)
		}
		if s.codec == "h264" && (badFraming || fast) {
			s.suspendH264()
			s.helperRetryAt = time.Time{} // retry as jpeg right away
		}
	}
	if time.Now().Before(s.helperRetryAt) {
		return
	}
	h, err := startCaptureHelper(s.w.ID, s.profile.MaxWidth, s.profile.Quality, s.captureFPS(), s.codec)
	if err == nil {
		s.helper = h
		s.helperErr = ""
		s.helperStarted = time.Now()
		if s.codec == "h264" {
			s.h264Suspend = 0 // it works here; forget the run of failures
		}
		return
	}
	s.helperErr = err.Error()
	// The service was simply not there to answer — a daemon restart, an
	// upgrade. That is not a verdict on the codec, and it clears in about a
	// second, so keep the codec and come back almost immediately. Latching
	// h264 off here (and then sitting out a ten-second cooldown) is what made
	// every `restart --all` quietly drop live panes to JPEG until they were
	// closed and reopened.
	if errors.Is(err, errMirrorUnavailable) {
		// Say only the part that means something to whoever is looking at the
		// pane. The wrapped cause is a socket path and an errno — worth having
		// in a log, but it is the whole of what the overlay displayed, so a
		// restart put "dial unix /Users/…/mirror.sock: connect: no such file
		// or directory" on screen where "starting — retry" was the message.
		s.helperErr = errMirrorUnavailable.Error()
		s.helperRetryAt = time.Now().Add(helperUnavailableRetry)
		return
	}
	if s.codec == "h264" {
		// Couldn't start in h264 mode for a reason the daemon actually
		// reported (helper missing under an old daemon, VTCompressionSession
		// unavailable). Fall back to JPEG immediately — the viewer is waiting
		// for frames — and re-test h264 once the suspension lapses.
		s.suspendH264()
		if h, jerr := startCaptureHelper(s.w.ID, s.profile.MaxWidth, s.profile.Quality, winHelperFPS, ""); jerr == nil {
			s.helper = h
			s.helperErr = ""
			s.helperStarted = time.Now()
			return
		}
		s.helperRetryAt = time.Now().Add(helperRetryCooldown)
		return
	}
	s.helperRetryAt = time.Now().Add(helperRetryCooldown)
}

// suspendH264 drops this stream to JPEG and schedules a re-test. Callers reach
// here on evidence that h264 specifically failed — never on a failure that
// says nothing about the codec (see errMirrorUnavailable).
func (s *winStream) suspendH264() {
	if s.h264Suspend == 0 {
		s.h264Suspend = h264SuspendFor
	} else if s.h264Suspend *= 2; s.h264Suspend > h264SuspendMax {
		s.h264Suspend = h264SuspendMax
	}
	s.h264BrokenUntil = time.Now().Add(s.h264Suspend)
	s.codec = ""
}

// activeMenu returns the window's live right-click region-capture entry, if the
// hold hasn't expired (see handleWindowInput's right-click path).
func (a *Agent) activeMenu(id string) (winMenuState, bool) {
	a.winMu.Lock()
	defer a.winMu.Unlock()
	m, ok := a.winMenu[id]
	if ok && time.Now().After(m.until) {
		delete(a.winMenu, id)
		return m, false
	}
	return m, ok
}

// capture produces the next frame. Helper path: blocks up to winHeartbeat for
// the next frame — a change or the helper's 1fps idle refresh (an empty result
// therefore means the helper went quiet, not merely a static window; its
// H264 flag still reflects the stream codec so run() takes the right branch).
// Subprocess path (helper unavailable, or a context menu needs a region grab):
// one screencapture JPEG, change-detected by the caller via frame signatures —
// menu region grabs interleave as JPEG even mid-h264 (the viewer paints either).
func (s *winStream) capture() (f winFrame, err error) {
	menu, menuActive := s.a.activeMenu(s.w.ID)
	s.ensureHelper()
	if s.helper != nil && !menuActive {
		s.capNative = true
		f, _ = s.helper.next(s.stop, winHeartbeat)
		if s.codec == "h264" {
			f.H264 = true
		}
		return f, nil
	}
	s.capNative = false
	if menuActive {
		if runtime.GOOS == "darwin" {
			// Region-capture (for the right-click menu) also runs in the daemon.
			img, rerr := mirrorCaptureRegion(menu.x, menu.y, menu.w, menu.h)
			return winFrame{Data: img}, rerr
		}
		img, rerr := s.b.captureRegion(menu.x, menu.y, menu.w, menu.h)
		return winFrame{Data: img}, rerr
	}
	if runtime.GOOS == "darwin" {
		// No local screencapture fallback on macOS: capture must run in the daemon
		// (sh.reminal) so one grant covers every session. A nil helper here means
		// the daemon is unreachable — surface that instead of a terminal-attributed
		// capture (decision: fail-and-retry, don't silently double-grant).
		e := s.helperErr
		if e == "" {
			e = "screen-sharing service starting — retry"
		}
		return winFrame{}, errors.New(e)
	}
	img, cerr := s.b.capture(s.w)
	return winFrame{Data: img}, cerr
}

// detectChange reports whether img is new content. Helper frames always count
// (it emits changes plus its own paced idle refresh); the subprocess path
// compares coarse frame signatures.
func (s *winStream) detectChange(img []byte) bool {
	if len(img) == 0 {
		return false
	}
	if s.capNative {
		return true
	}
	sig, ok := frameSignature(img)
	s.pendingSig, s.pendingSigOK = sig, ok
	return !ok || !s.haveSig || sigDiffers(s.lastSig, sig)
}

// checkWindow verifies the mirrored window still exists (drop the pane if not)
// and, on the helper path, that its geometry hasn't changed — the helper scales
// into its start-time output size, so a resized window would render distorted
// forever. The geometry poll runs even while frames flow (that's exactly when
// resizes happen); the subprocess path polls existence only while static, as a
// closed window there keeps "capturing" its retained backing store instead of
// erroring. Returns false when the stream should end.
// geoResult is one off-thread answer to "is this window still there, and has
// it moved?".
type geoResult struct {
	w   winInfo
	err error
}

func (s *winStream) checkWindow(conn *websocket.Conn, changed bool) bool {
	if s.capNative {
		// Resolving a window enumerates every window on the system through an
		// osascript — measured at ~114ms. Running that inline stalled the
		// capture loop for the length of it every couple of seconds, which at
		// 60fps drops a visible handful of frames on a schedule, on the
		// desktop view as much as a window. So it runs off the loop, and its
		// answer is picked up whenever it happens to be ready: nothing here
		// needs the geometry sooner than that, and a window that closed is
		// noticed a moment later either way.
		if s.geoCh == nil {
			s.geoCh = make(chan geoResult, 1)
		}
		if !s.geoBusy && time.Since(s.lastGeoCheck) >= 2*winLiveCheck {
			s.lastGeoCheck = time.Now()
			s.geoBusy = true
			id, b, ch := s.w.ID, s.b, s.geoCh
			go func() { w, err := findWindow(b, id); ch <- geoResult{w, err} }()
		}
		var res geoResult
		select {
		case res = <-s.geoCh:
			s.geoBusy = false
		default:
			return true // nothing back yet; keep streaming
		}
		cur, err := res.w, res.err
		if err != nil {
			// findWindow shells out to osascript, which can fail transiently
			// under load — and the busier this loop is (60fps video), the more
			// often. Treating one failure as "the window closed" tears down a
			// perfectly live pane. Require several in a row; the screenshot
			// path's exists() already takes the same view (it reports true on
			// error for exactly this reason).
			s.geoFails++
			if s.geoFails < 3 {
				return true
			}
			s.a.sendWindowClosed(conn, s.w.ID)
			return false
		}
		s.geoFails = 0
		resized := absInt(cur.W-s.w.W) > 8 || absInt(cur.H-s.w.H) > 8
		s.w = cur
		if resized && s.helper != nil {
			s.helper.stop()
			s.helper = nil
			if h, err := startCaptureHelper(s.w.ID, s.profile.MaxWidth, s.profile.Quality, s.captureFPS(), s.codec); err == nil {
				s.helper = h
			} else {
				s.helperRetryAt = time.Now().Add(helperRetryCooldown)
			}
		}
		return true
	}
	if changed {
		s.lastGeoCheck = time.Now()
		return true
	}
	if time.Since(s.lastGeoCheck) < winLiveCheck {
		return true
	}
	s.lastGeoCheck = time.Now()
	if !s.b.exists(s.w.ID) {
		s.a.sendWindowClosed(conn, s.w.ID)
		return false
	}
	return true
}

// captureFailure handles a subprocess capture that produced no pixels: a closed
// window ends the stream; anything else (usually missing Screen Recording
// permission) surfaces a message to the viewer instead of a silent frozen pane.
// Returns false when the stream should end.
func (s *winStream) captureFailure(conn *websocket.Conn, err error) bool {
	if !s.b.exists(s.w.ID) {
		s.a.sendWindowClosed(conn, s.w.ID)
		return false
	}
	s.fails++
	if s.fails == 4 || s.fails%64 == 0 {
		msg := s.b.permissionHint()
		if msg == "" {
			if err != nil {
				msg = "Couldn't capture this window: " + err.Error()
			} else {
				msg = "Couldn't capture this window."
			}
		}
		s.a.sendWindowMsg(conn, protocol.TypeWindowFrame, struct {
			ID    string `json:"id"`
			Error string `json:"error"`
		}{ID: s.w.ID, Error: msg})
	}
	return true
}

// dispatch decides what this iteration sends: a frame (when the picture changed
// and a sink will take it, or an unproven channel is due a probe), else a
// heartbeat when it's time. All send-rate decisions live in the sink resolution
// here — nowhere else.
// noteKeyRequest folds a pending "I need an entry point" request into the
// stream's state. It is raised by a viewer that lost sync, and by
// startWindowStream when a SECOND viewer opens a window already being streamed.
//
// H.264 answers it by re-keying: deltas are useless without the frames they
// were built from. JPEG has nothing to re-key — every frame is already a
// self-contained entry point — but the newcomer still has to RECEIVE one, and
// frames are only sent when the picture changes. On a still window that is
// never. Honouring the request only for h264 (and swallowing it otherwise) is
// what left a phone joining an in-flight stream on "Connecting…" indefinitely
// while everyone already watching saw the window fine.
func (s *winStream) noteKeyRequest() {
	if s.keyReq == nil || !s.keyReq.Swap(false) {
		return
	}
	if s.helper != nil && s.codec == "h264" {
		s.helper.rekey()
		return
	}
	s.forceSend = true
}

// wsFrameDue reports whether the relay copy of a frame may go out now. Pacing
// keeps a steady stream cheap, but it must not apply to a viewer that has no
// picture at all: throttled out, its forced frame is dropped and it waits for
// the next change, which on a still window never comes.
func (s *winStream) wsFrameDue(vc, confirmed int, now time.Time) bool {
	if !wsSinkNeeded(vc, confirmed) {
		return false
	}
	return s.forceSend || s.shipNow || now.Sub(s.lastWSFrame) >= wsFrameMinInterval
}

// takeInputFlush reacts to a viewer click/key: throw away unsent pre-click
// frames (they would only delay the result) and ask the encoder for a key so
// the next AU is a valid entry point. Same number of relay messages — the
// next one just leaves sooner and is current.
func (s *winStream) takeInputFlush() {
	if s.flush == nil || !s.flush.Swap(false) {
		return
	}
	s.wsBatch = s.wsBatch[:0]
	s.wsBatchBytes = 0
	if s.helper != nil {
		s.helper.rekey()
	}
	s.shipNow = true
}

func (s *winStream) dispatch(conn *websocket.Conn, changed bool) {
	confirmed, probing, _ := s.a.rtcSinks()
	vc := s.a.framesWantedBy()

	// Demote confirmed channels only on EVIDENCE of a dead link: several
	// ack-expected frames sent since the last consumed ack, and sustained
	// silence. (Elapsed time alone falsely demoted after every idle pause — no
	// frames sent means no acks arrive — flapping the transport with no actual
	// network change.)
	if s.gotAnyAck && s.sentSinceAck >= 3 && time.Since(s.lastAck) > streamAckIdleTimeout/2 {
		s.a.unconfirmRTC()
		confirmed, probing, _ = s.a.rtcSinks()
	}

	probeDue := len(probing) > 0 && time.Since(s.lastProbe) >= winProbeFrame
	wsDue := s.wsFrameDue(vc, len(confirmed), time.Now())

	var sinks winSinks
	if changed {
		sinks = winSinks{confirmed: confirmed, ws: wsDue}
		if probeDue {
			sinks.probe = probing
		}
	} else if probeDue && len(confirmed) == 0 {
		// Static window with an unproven channel: re-send the newest frame so
		// the channel can still prove itself (heartbeats aren't confirmable —
		// only a frame ack over the channel is). Without this, P2P could never
		// engage on a window that isn't changing.
		sinks = winSinks{probe: probing}
	}

	if sinks.any() && len(s.lastImg) > 0 {
		// Served. Anything still waiting will raise the flag again.
		s.forceSend = false
		s.sendFrame(conn, sinks)
		return
	}
	// A changed frame with every sink throttled out this instant is simply
	// dropped — seq must not advance for a frame nobody receives (the viewer's
	// acks would fall behind and stall the in-flight gate); the next iteration
	// captures fresher content anyway.
	if time.Since(s.lastSent) >= winHeartbeat {
		s.sendHeartbeat(conn, confirmed, probing, vc)
	}
}

// winDCMaxMsg is the largest frame message the agent will put on a WebRTC
// DataChannel. A peer CLOSES the channel outright on any message above its
// maxMessageSize (Chrome 256 KiB; the spec mandates kill, not drop) — which
// showed up as the mirror spontaneously "falling back to relay" whenever the
// window content got busy enough to fatten a frame past the limit. The native
// helper already encodes under a byte budget; this is the hard backstop for the
// screencapture fallback path, which can't cheaply re-encode.
const winDCMaxMsg = 192 << 10

// sendFrame stamps and ships the newest frame to exactly the resolved sinks.
func (s *winStream) sendFrame(conn *websocket.Conn, sinks winSinks) {
	// Capture source, shown in the (i) popover. "shot" means the slow
	// per-frame path and invites the question "why"; the answer lives in
	// capErr. Windows is neither: its in-process GDI capture IS the native
	// path, the only one it has. Stamped as such, the popover was reporting
	// "Screenshot fallback · capture helper is macOS-only" on every Windows
	// pane — an internal detail, describing a helper Windows is not supposed
	// to have, phrased as a fault. Alongside an idle stream's honest 0 fps it
	// read as a broken mirror, which is how it was reported.
	capSrc := "shot"
	switch {
	case s.capNative:
		capSrc = "native"
	case runtime.GOOS == "windows":
		capSrc = "win32"
	}
	capErr := ""
	// Only explain the fallback where a native path exists to have fallen back
	// FROM. Off darwin, "capture helper is macOS-only" is the design, not news.
	if !s.capNative && s.helperErr != "" && runtime.GOOS == "darwin" {
		// Bounded: SCK error strings can ramble.
		capErr = s.helperErr
		if len(capErr) > 120 {
			capErr = capErr[:120] + "…"
		}
	}
	frame := struct {
		ID     string `json:"id"`
		W      int    `json:"w"`
		H      int    `json:"h"`
		Seq    uint64 `json:"seq"`
		Cap    string `json:"cap,omitempty"`     // capture source, shown in the (i) popover
		CapErr string `json:"cap_err,omitempty"` // why the native path is unavailable
		Img    string `json:"img"`               // base64 JPEG
	}{ID: s.w.ID, W: s.w.W, H: s.w.H, Seq: s.seq + 1, Cap: capSrc, CapErr: capErr, Img: base64.StdEncoding.EncodeToString(s.lastImg)}
	raw, err := json.Marshal(frame)
	if err != nil {
		return
	}
	if len(raw) > winDCMaxMsg {
		// Too big for any DataChannel — route around them entirely (better a
		// relay-capped frame than a killed channel), INCLUDING when every
		// viewer is on P2P: they'd freeze otherwise. Still relay-rate-capped;
		// if the WS window isn't open yet, drop without advancing seq and let
		// the next iteration carry fresher content.
		sinks.confirmed, sinks.probe = nil, nil
		sinks.ws = time.Since(s.lastWSFrame) >= wsFrameMinInterval || s.shipNow
		if !sinks.ws {
			return
		}
	}
	s.seq++
	for _, p := range sinks.confirmed {
		p.sendFrame(raw)
	}
	if sinks.ws {
		s.a.sendWindowMsg(conn, protocol.TypeWindowFrame, frame)
		s.lastWSFrame = time.Now()
		s.shipNow = false
	}
	if len(sinks.probe) > 0 {
		for _, p := range sinks.probe {
			p.sendFrame(raw)
		}
		s.lastProbe = time.Now()
	}
	if sinks.expectsAck() {
		s.sentSinceAck++
	}
	if !s.capNative {
		s.lastSig, s.haveSig = s.pendingSig, s.pendingSigOK
	}
	s.lastSent = time.Now()
}

// Binary DataChannel frame framing (agent→viewer). Every other DC message is
// JSON (first byte '{'); a binary h264 frame starts with a magic byte instead.
//
// v1 (winBinMagic, 0xF2) — for a reliable ordered channel:
//
//	[0]      winBinMagic
//	[1]      flags: bit0 = key AU, bit1 = more chunks of this AU follow
//	[2]      id length L
//	[3:3+L]  window id
//	[+8]     seq   (BE uint64)
//	[+4]     w     (BE uint32, logical window width — pane sizing)
//	[+4]     h     (BE uint32)
//	[rest]   Annex-B H.264 access unit (or one chunk of it)
//
// v2 (winBinMagicV2, 0xF3) — identical but with two extra header bytes after
// the flags, carrying the chunk's index and the chunk count for this AU:
//
//	[2]      chunk index
//	[3]      chunk count
//	[4]      id length L … then as above
//
// Chunking keeps every message under winDCMaxMsg (an oversized message KILLS
// the channel per spec). v1 reassembles by arrival order, which only holds
// because its channel is ordered; a v2 channel is unordered and lossy, so
// position tells you nothing and the index is what lets the viewer put an AU
// back together — or know that it can't, and ask for a keyframe.
const (
	winBinMagic    = 0xF2
	winBinMagicV2  = 0xF3
	winBinFlagKey  = 1 << 0
	winBinFlagMore = 1 << 1
)

// dispatchH264 ships one H.264 access unit to the confirmed DataChannels, or —
// when the transport shifted under us (demote, viewer left) — drops it and
// re-keys so the stream resumes from a clean IDR once negotiateCodec settles
// things. Unlike the JPEG path there is no WS sink and no probe: h264 only
// engages when every viewer is on a confirmed channel.
func (s *winStream) dispatchH264(conn *websocket.Conn, f winFrame) {
	confirmed, probing, allH264 := s.a.rtcSinks()

	// Same demote-on-evidence rule as the JPEG path.
	if s.gotAnyAck && s.sentSinceAck >= 3 && time.Since(s.lastAck) > streamAckIdleTimeout/2 {
		s.a.unconfirmRTC()
		confirmed, probing, _ = s.a.rtcSinks()
	}
	// Probing channels get the video too: an AU acked over a channel is exactly
	// the proof that channel can carry frames, so video confirms P2P just as
	// JPEG probe frames used to.
	dcSinks := append(append([]*rtcPeer{}, confirmed...), probing...)
	if !allH264 || (len(dcSinks) == 0 && !s.wsVideo) {
		// Nowhere to put it. Drop and re-key so whatever attaches next starts
		// from a clean entry point rather than mid-GOP.
		if s.helper != nil {
			s.helper.rekey()
		}
		return
	}
	s.seq++
	if len(dcSinks) > 0 {
		s.sendFrameH264(f, dcSinks)
	}
	if s.wsVideo {
		s.appendWSBatch(conn, f)
	}
	s.sentSinceAck++
	s.lastSent = time.Now()
}

// wsBatchMaxBytes bounds one relay message. The relay reads at most 1 MiB and
// the practical traversable size is well under that, so cap the raw AU bytes
// low enough that base64 (+1.34x) and the JSON/AES envelope still fit with
// room to spare. Reaching it flushes early — the interval is the usual trigger.
const wsBatchMaxBytes = 300 << 10

// appendWSBatch buffers one AU for the relay and flushes when the billing
// interval elapses (or the size cap is hit). This is the whole point of the
// relay video path: the Durable Object bills per forwarded message, so six
// 8 KB access units in ONE message cost exactly what a single 60 KB JPEG cost
// — 30fps for the price of 5.
func (s *winStream) appendWSBatch(conn *websocket.Conn, f winFrame) {
	if len(s.wsBatch) == 0 {
		s.wsBatchSeq0 = s.seq
	}
	s.wsBatch = append(s.wsBatch, f)
	s.wsBatchBytes += len(f.Data)
	if s.wsBatchBytes >= wsBatchMaxBytes || time.Since(s.lastWSFrame) >= wsFrameMinInterval || s.shipNow {
		s.flushWSBatch(conn)
		s.shipNow = false
	}
}

// flushWSBatch ships the buffered AUs as one encrypted relay message. The
// payload is the same [uint32 len][flag][AU] framing the capture helper emits,
// concatenated and base64'd once — one blob rather than per-AU JSON overhead.
// seq0 is the first AU's sequence; the viewer derives the rest by position, so
// gap detection keeps working across batches.
func (s *winStream) flushWSBatch(conn *websocket.Conn) {
	if len(s.wsBatch) == 0 || conn == nil {
		return
	}
	blob := make([]byte, 0, s.wsBatchBytes+5*len(s.wsBatch))
	for _, f := range s.wsBatch {
		flag := byte(flagH264Delta)
		if f.Key {
			flag = flagH264Key
		}
		blob = binary.BigEndian.AppendUint32(blob, uint32(len(f.Data)+1))
		blob = append(blob, flag)
		blob = append(blob, f.Data...)
	}
	s.a.sendWindowMsg(conn, protocol.TypeWindowFrame, struct {
		ID    string `json:"id"`
		W     int    `json:"w"`
		H     int    `json:"h"`
		Seq   uint64 `json:"seq"`  // last AU in the batch — what the viewer acks
		Seq0  uint64 `json:"seq0"` // first AU; the rest follow by position
		N     int    `json:"n"`
		Cap   string `json:"cap,omitempty"`
		Batch string `json:"batch"` // base64 [len][flag][AU]...
	}{
		ID: s.w.ID, W: s.w.W, H: s.w.H,
		Seq: s.wsBatchSeq0 + uint64(len(s.wsBatch)) - 1, Seq0: s.wsBatchSeq0,
		N: len(s.wsBatch), Cap: "h264",
		Batch: base64.StdEncoding.EncodeToString(blob),
	})
	s.wsBatch, s.wsBatchBytes = s.wsBatch[:0], 0
	s.lastWSFrame = time.Now()
}

// sendFrameH264 frames one AU as binary DC messages (chunked under winDCMaxMsg)
// and sends it to every sink. One seq per AU regardless of chunk count — the
// viewer acks per AU after decode, same pacing loop as JPEG.
func (s *winStream) sendFrameH264(f winFrame, sinks []*rtcPeer) {
	if len(f.Data) == 0 {
		return
	}
	// Two framings on the wire at once: a v2 peer gets chunk-indexed messages
	// it can reassemble out of order, a v1 peer the positional framing that
	// assumes ordered delivery. Built at most once each, and only if somebody
	// wants that shape.
	var v1, v2 [][]byte
	for _, p := range sinks {
		msgs := &v1
		if p.v2 {
			msgs = &v2
		}
		if *msgs == nil {
			*msgs = buildWinBinMsgs(s.w.ID, s.seq, s.w.W, s.w.H, f.Key, f.Data, winDCMaxMsg, p.v2)
			if *msgs == nil {
				continue
			}
		}
		// All-or-nothing per peer: half an access unit is undecodable, so a
		// peer over its send budget skips the whole thing and resyncs on the
		// keyframe its gap detection will ask for.
		if p.dc == nil || p.dc.BufferedAmount() > rtcSendBudget {
			continue
		}
		for _, msg := range *msgs {
			_ = p.dc.Send(msg)
		}
	}
}

// buildWinBinMsgs encodes one access unit into winBinMagic messages, each at
// most maxMsg bytes (an oversized DataChannel message kills the channel, so
// this bound is hard). All chunks of an AU share seq; every chunk except the
// last carries winBinFlagMore.
func buildWinBinMsgs(id string, seq uint64, w, h int, key bool, data []byte, maxMsg int, v2 bool) [][]byte {
	idb := []byte(id)
	hdrLen := 3 + len(idb) + 8 + 4 + 4
	if v2 {
		hdrLen += 2 // chunk index + chunk count
	}
	if len(idb) > 255 || len(data) == 0 || maxMsg <= hdrLen {
		return nil
	}
	maxData := maxMsg - hdrLen
	// v2 numbers its chunks, so the count has to be known before the first one
	// goes out — and must fit the byte that carries it.
	total := (len(data) + maxData - 1) / maxData
	if v2 && total > 255 {
		return nil
	}
	var msgs [][]byte
	for idx := 0; len(data) > 0; idx++ {
		n := min(len(data), maxData)
		chunk := data[:n]
		data = data[n:]
		flags := byte(0)
		if key {
			flags |= winBinFlagKey
		}
		if len(data) > 0 {
			flags |= winBinFlagMore
		}
		msg := make([]byte, 0, hdrLen+n)
		if v2 {
			msg = append(msg, winBinMagicV2, flags, byte(idx), byte(total))
		} else {
			msg = append(msg, winBinMagic, flags)
		}
		msg = append(msg, byte(len(idb)))
		msg = append(msg, idb...)
		msg = binary.BigEndian.AppendUint64(msg, seq)
		msg = binary.BigEndian.AppendUint32(msg, uint32(w))
		msg = binary.BigEndian.AppendUint32(msg, uint32(h))
		msg = append(msg, chunk...)
		msgs = append(msgs, msg)
	}
	return msgs
}

// sendHeartbeat pings viewers over every live path so an idle (0 fps) window
// doesn't read as a dead host. Not acked, and can't confirm a channel.
// sendHeartbeat pings viewers over every live path so an idle (0 fps) window
// doesn't read as a dead host. Not acked, and can't confirm a channel. Rides
// the ctl channel where there is one: a heartbeat is a liveness claim, and
// sending it down the same queue as the video meant that during exactly the
// congestion that delays frames it ALSO stopped arriving — so the pane
// announced the host was asleep at the one moment it was busiest.
func (s *winStream) sendHeartbeat(conn *websocket.Conn, confirmed, probing []*rtcPeer, vc int) {
	hb := struct {
		ID string `json:"id"`
		HB bool   `json:"hb"`
	}{ID: s.w.ID, HB: true}
	raw, err := json.Marshal(hb)
	if err != nil {
		return
	}
	for _, p := range confirmed {
		p.sendCtl(raw)
	}
	if wsSinkNeeded(vc, len(confirmed)) {
		s.a.sendWindowMsg(conn, protocol.TypeWindowFrame, hb)
	}
	for _, p := range probing {
		p.sendCtl(raw)
	}
	s.lastSent = time.Now()
}

// framesWantedBy is the viewer count that should decide whether a relay copy
// of each frame is needed: the relay's total, less those that have positively
// reported having no window pane open. A viewer watching nothing cannot be the
// one that needs the relay, and counting it was enough to force the whole
// stream onto the billed path at half the frame rate with 200ms of batching.
// Popping a window out hit this every time — the opener tab stays connected
// with no panes, and with no frames to receive it can never confirm a
// DataChannel either, so "viewers exceed confirmed channels" stayed true for
// as long as the tab was open.
func (a *Agent) framesWantedBy() int {
	n := a.currentViewerCount() - a.idleViewerCount()
	if n < 0 {
		return 0
	}
	return n
}

// currentViewerCount returns the relay-reported live viewer count.
func (a *Agent) currentViewerCount() int {
	a.viewerSizeMu.Lock()
	defer a.viewerSizeMu.Unlock()
	return a.viewerCount
}

// liveConn returns the current relay connection under the same lock the rest
// of the agent uses, or nil if we're not connected.
func (a *Agent) liveConn() *websocket.Conn {
	a.currentConnMu.Lock()
	defer a.currentConnMu.Unlock()
	if a.currentConn == nil {
		return nil
	}
	return a.currentConn
}

// sendWindowMsg JSON-encodes payload, encrypts it, and writes it to conn as a
// single message of the given type. Errors are swallowed — a dropped frame is
// not worth tearing down the session.
func (a *Agent) sendWindowMsg(conn *websocket.Conn, t protocol.MessageType, payload any) {
	// Callers on async paths (e.g. OnICECandidate) pass a.liveConn(), which is
	// nil while the relay connection is down — writing would nil-deref.
	if conn == nil {
		return
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	enc, err := a.box.Encrypt(raw)
	if err != nil {
		return
	}
	_ = a.writeMsg(conn, protocol.Message{Type: t, Data: enc})
}

// sendWindowClosed tells the viewer a mirrored window is gone so it drops the
// pane (handled as a window_frame with closed=true — same channel as frames).
func (a *Agent) sendWindowClosed(conn *websocket.Conn, id string) {
	a.sendWindowMsg(conn, protocol.TypeWindowFrame, struct {
		ID     string `json:"id"`
		Closed bool   `json:"closed"`
	}{ID: id, Closed: true})
}

// ---- helpers shared by backends --------------------------------------------

// run executes name with args and returns trimmed stdout, or an error carrying
// stderr for diagnostics (permission prompts, missing tools).
func run(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return "", fmt.Errorf("%s: %s", name, strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// have reports whether a command is on PATH.
func have(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

// ---- stub backend (unsupported OS) -----------------------------------------

type stubWindows struct{ os string }

func (s stubWindows) unsupported() string {
	return "window mirroring isn't supported on " + s.os + " yet"
}
func (stubWindows) permissionHint() string                                   { return "" }
func (s stubWindows) list() ([]winInfo, error)                               { return nil, nil }
func (s stubWindows) capture(winInfo) ([]byte, error)                        { return nil, nil }
func (s stubWindows) captureRegion(int, int, int, int) ([]byte, error)       { return nil, nil }
func (s stubWindows) focus(winInfo) error                                    { return nil }
func (s stubWindows) clickN(winInfo, float64, float64, int, bool) error      { return nil }
func (s stubWindows) drag(winInfo, [][2]float64) error                       { return nil }
func (stubWindows) scroll(winInfo, float64, float64, float64, float64) error { return nil }
func (s stubWindows) exists(string) bool                                     { return true }
func (s stubWindows) releaseInput() error                                    { return nil }
func (s stubWindows) typeText(winInfo, string) error                         { return nil }
func (s stubWindows) key(winInfo, string) error                              { return nil }
func (s stubWindows) listApps() ([]appInfo, error)                           { return nil, nil }
func (s stubWindows) openApp(string) error                                   { return nil }

// tmpImage writes captured bytes to a temp file path with the given extension
// so tools that only emit to a file (screencapture) can be read back.
func tmpImage(ext string) (string, error) {
	f, err := os.CreateTemp("", "reminal-win-*."+ext)
	if err != nil {
		return "", err
	}
	name := f.Name()
	_ = f.Close()
	return name, nil
}
