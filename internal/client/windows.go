// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
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
	X     int    `json:"x"`
	Y     int    `json:"y"`
	W     int    `json:"w"`
	H     int    `json:"h"`
	// PID is the owning process id (macOS), used to activate the app reliably
	// via NSRunningApplication. Not sent to the viewer.
	PID int `json:"-"`
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
}

// newWindowBackend picks the backend for the current OS. Unknown platforms get
// a stub whose every call reports that window mirroring isn't supported yet.
func newWindowBackend() windowBackend {
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
				op()
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

// handleWindowList enumerates the host's windows and sends the encrypted list
// back to viewers in reply to a window_list request.
func (a *Agent) handleWindowList(conn *websocket.Conn) {
	b := a.windows()
	var payload struct {
		Windows     []winInfo `json:"windows"`
		Unsupported string    `json:"unsupported,omitempty"`
		Error       string    `json:"error,omitempty"`
		Hint        string    `json:"hint,omitempty"`
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
	}
	if json.Unmarshal(plaintext, &req) != nil {
		return
	}
	switch req.Action {
	case "start":
		a.startWindowStream(req.ID)
	case "stop":
		a.stopWindowStream(req.ID)
		_ = a.windows().releaseInput() // never leave a button held after a pane closes
	}
}

// handleWindowInput injects a mouse/keyboard event into the target window.
func (a *Agent) handleWindowInput(encData string) {
	plaintext, err := a.box.Decrypt(encData)
	if err != nil {
		return
	}
	var ev struct {
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
	}
	if json.Unmarshal(plaintext, &ev) != nil {
		return
	}
	b := a.windows()
	if b.unsupported() != "" {
		return
	}
	w, err := findWindow(b, ev.ID)
	if err != nil {
		return
	}
	switch ev.Kind {
	case "click":
		// Raise the specific window so the click lands on it, then post a click
		// whose click-state gives native single/double/triple-click behaviour.
		// Prefer the viewer's count (it times taps at the source, immune to
		// network jitter); fall back to the Agent's own rapid-click timer for
		// older viewers that don't report it. button:"right" → context click.
		count := ev.Count
		if count < 1 {
			count = a.clickCount(w, ev.X, ev.Y)
		}
		right := ev.Button == "right"
		_ = b.focus(w)
		_ = b.clickN(w, ev.X, ev.Y, count, right)
		// A right-click opens a context menu, which macOS draws as its own window
		// (missed by capture-by-id). Snapshot this window's bounds and region-
		// capture it briefly so the menu shows; a left click dismisses the menu, so
		// clear the flag then and revert to the sharper per-window capture.
		a.winMu.Lock()
		if a.winMenu == nil {
			a.winMenu = map[string]winMenuState{}
		}
		if right {
			a.winMenu[w.ID] = winMenuState{x: w.X, y: w.Y, w: w.W, h: w.H, until: time.Now().Add(winMenuHold)}
		} else {
			delete(a.winMenu, w.ID)
		}
		a.winMu.Unlock()
	case "drag":
		_ = b.focus(w)
		_ = b.drag(w, ev.Path)
	case "scroll":
		// Raise the target window once at the start of a scroll gesture so the
		// scroll lands on IT (not whatever was focused). Skip the ~100ms raise on
		// subsequent events of the same gesture (a new gesture = a different
		// window, or a >400ms gap) so continuous scrolling stays smooth.
		if ev.ID != a.winScrollID || time.Since(a.winScrollAt) > 400*time.Millisecond {
			_ = b.focus(w)
		}
		a.winScrollID = ev.ID
		a.winScrollAt = time.Now()
		_ = b.scroll(w, ev.X, ev.Y, ev.Dx, ev.Dy)
	case "key":
		// Keys land on whatever's focused; the user focuses the target by
		// clicking it first, so we don't re-focus per keystroke (that would
		// add ~100ms osascript latency to every character).
		if ev.Special != "" {
			_ = b.key(w, ev.Special)
		} else if ev.Text != "" {
			_ = b.typeText(w, ev.Text)
		}
	}
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
	}
	if json.Unmarshal(plaintext, &ev) != nil {
		return
	}
	a.deliverWindowAck(ev.ID, ev.Seq)
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

// clickCount returns the click-state (1=single, 2=double, 3=triple) for a click
// at (fx, fy) in window w, by timing it against the previous click. Mirrors how
// the OS coalesces rapid clicks in one spot. Runs on the winOps worker only.
func (a *Agent) clickCount(w winInfo, fx, fy float64) int {
	x := w.X + int(fx*float64(w.W))
	y := w.Y + int(fy*float64(w.H))
	now := time.Now()
	if now.Sub(a.winLastClickAt) < 450*time.Millisecond &&
		absInt(x-a.winLastClickX) <= 4 && absInt(y-a.winLastClickY) <= 4 {
		a.winClickN++
		if a.winClickN > 3 {
			a.winClickN = 1 // wrap after triple; further clicks start fresh
		}
	} else {
		a.winClickN = 1
	}
	a.winLastClickAt, a.winLastClickX, a.winLastClickY = now, x, y
	return a.winClickN
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// startWindowStream launches a capture goroutine for the given window unless
// one is already running for it. Multiple windows can stream concurrently.
func (a *Agent) startWindowStream(id string) {
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
	}
	if _, ok := a.winStreams[id]; ok {
		a.winMu.Unlock() // already streaming this window
		return
	}
	stop := make(chan struct{})
	// Buffered so an incoming ack never blocks the reader goroutine; streamWindow
	// only cares about the newest seq, so a slot or two is plenty.
	ack := make(chan uint64, 4)
	a.winStreams[id] = stop
	a.winAck[id] = ack
	// First window under mirror → keep the display awake so the host can't
	// idle-lock and strand remote control (see winAwake).
	if a.winAwake == nil {
		a.winAwake = keepawake.StartDisplay()
	}
	a.winMu.Unlock()

	go a.streamWindow(w, stop, ack)
}

// stopWindowStream ends the stream for one window id (its pane was closed).
// An empty id stops every stream (viewer left / connection dropped).
func (a *Agent) stopWindowStream(id string) {
	a.winMu.Lock()
	if id == "" {
		for k, ch := range a.winStreams {
			close(ch)
			delete(a.winStreams, k)
		}
		a.winAck = map[string]chan uint64{}
	} else if ch, ok := a.winStreams[id]; ok {
		close(ch)
		delete(a.winStreams, id)
		delete(a.winAck, id)
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
// grayscale grid (box-averaged, so localized noise averages out); if no cell
// moved by winSigThreshold or more since the last SENT frame, the window is
// treated as unchanged and the frame is skipped — sparing the relay a frame
// message and its ack (each is a billed Durable Object request). The threshold
// is deliberately conservative: a real update (a typed character, a scroll, a
// cursor move) shifts at least one cell well past it, so we only ever skip
// visually identical frames and never drop a real change. An unchanged window
// sends NO frames at all (0 fps); winHeartbeat only governs a tiny liveness
// ping (see streamWindow) so the viewer knows the host is alive without a frame.
// winProbeFrame is the one exception: while a DataChannel is open but not yet
// confirmed to carry frames, we send a real frame this often even if nothing
// changed — otherwise a static window would never give the channel a frame to
// prove itself and P2P would never engage. Once confirmed, it's back to 0 fps.
const (
	winSigN         = 32
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
)

// frameSignature reduces a JPEG frame to a coarse grayscale grid by
// box-averaging. It reads the luma (Y) plane directly for JPEG's native YCbCr,
// avoiding a per-pixel RGBA conversion. ok is false when the frame can't be
// decoded, in which case the caller treats the frame as changed (always sends).
func frameSignature(jpegBytes []byte) (sig [winSigN * winSigN]byte, ok bool) {
	img, err := jpeg.Decode(bytes.NewReader(jpegBytes))
	if err != nil {
		return sig, false
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w == 0 || h == 0 {
		return sig, false
	}
	var sum [winSigN * winSigN]uint64
	var cnt [winSigN * winSigN]uint32
	if yc, isYCbCr := img.(*image.YCbCr); isYCbCr {
		for y := 0; y < h; y++ {
			cy := y * winSigN / h
			for x := 0; x < w; x++ {
				idx := cy*winSigN + x*winSigN/w
				sum[idx] += uint64(yc.Y[yc.YOffset(b.Min.X+x, b.Min.Y+y)])
				cnt[idx]++
			}
		}
	} else {
		for y := 0; y < h; y++ {
			cy := y * winSigN / h
			for x := 0; x < w; x++ {
				r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
				gray := (r*299 + g*587 + bl*114) / 1000 >> 8
				idx := cy*winSigN + x*winSigN/w
				sum[idx] += uint64(gray)
				cnt[idx]++
			}
		}
	}
	for i := range sig {
		if cnt[i] > 0 {
			sig[i] = byte(sum[i] / uint64(cnt[i]))
		}
	}
	return sig, true
}

// sigDiffers reports whether any grid cell changed by at least winSigThreshold.
func sigDiffers(a, b [winSigN * winSigN]byte) bool {
	for i := range a {
		d := int(a[i]) - int(b[i])
		if d < 0 {
			d = -d
		}
		if d >= winSigThreshold {
			return true
		}
	}
	return false
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

func (a *Agent) streamWindow(w winInfo, stop <-chan struct{}, ack <-chan uint64) {
	b := a.windows()
	defer func() {
		a.winMu.Lock()
		if ch, ok := a.winStreams[w.ID]; ok && ch == stop {
			delete(a.winStreams, w.ID)
			delete(a.winAck, w.ID)
		}
		delete(a.winMenu, w.ID) // don't leak the right-click region-capture entry
		// A stream that exits on its own — window closed, viewer went silent —
		// bypasses stopWindowStream, so it must release the keep-awake inhibitor
		// itself when it's the last one; otherwise a window closing under mirror
		// pins the host display awake (never idle-locks) forever. Only release if
		// we actually removed the last stream (a replaced id leaves the map non-
		// empty, so the check below is false and the new stream keeps the hold).
		var release func()
		if len(a.winStreams) == 0 && a.winAwake != nil {
			release, a.winAwake = a.winAwake, nil
		}
		a.winMu.Unlock()
		if release != nil {
			release()
		}
	}()
	var seq, acked uint64 // last frame sent, and highest the viewer confirmed
	var fails int         // consecutive capture failures while the window exists
	var lastSig [winSigN * winSigN]byte
	var haveSig bool
	var lastSent time.Time      // when we last sent a frame OR heartbeat (paces both)
	var lastAck time.Time       // when the viewer last genuinely acked a frame
	var gotAnyAck bool          // has this viewer ever acked (arms the idle stop)?
	lastLiveCheck := time.Now() // last time we verified a static window still exists
	for {
		conn := a.liveConn()
		if conn == nil {
			return
		}
		// Hold once maxFramesInFlight frames are outstanding (bounded by the
		// timeout), so we never outrun the viewer by more than that — latency
		// stays bounded instead of accumulating on a slow link.
		for seq-acked >= maxFramesInFlight {
			select {
			case s := <-ack:
				if s > acked {
					acked = s
				}
				lastAck, gotAnyAck = time.Now(), true
			case <-time.After(ackWaitTimeout):
				// A viewer that was acking has gone silent — it vanished without
				// a clean close and the relay hasn't reported count==0 yet. Stop
				// rather than stream to nobody (see streamAckIdleTimeout).
				if gotAnyAck && time.Since(lastAck) > streamAckIdleTimeout {
					return
				}
				acked = seq // assume delivered; keep the stream moving
			case <-stop:
				return
			}
		}
		start := time.Now()
		// While a right-click's context menu is up, capture the window's screen
		// region so the menu (a separate OS window overlapping this one) is in the
		// frame. The region matches the window's bounds, so frame size and click
		// mapping are unchanged; an expired entry falls back to per-window capture.
		a.winMu.Lock()
		menu, menuActive := a.winMenu[w.ID]
		if menuActive && time.Now().After(menu.until) {
			delete(a.winMenu, w.ID)
			menuActive = false
		}
		a.winMu.Unlock()
		var img []byte
		var err error
		if menuActive {
			img, err = b.captureRegion(menu.x, menu.y, menu.w, menu.h)
		} else {
			img, err = b.capture(w)
		}
		if err == nil && len(img) > 0 {
			fails = 0
			// An unchanged window sends NO frame at all (0 fps) — only a tiny
			// heartbeat every winHeartbeat so the viewer knows the host is alive
			// and doesn't flash "Waiting for host…". A frame goes out only when the
			// picture actually changes (or the first frame / an undecodable capture
			// we can't compare against).
			sig, ok := frameSignature(img)
			changed := !ok || !haveSig || sigDiffers(lastSig, sig)
			// A closed window keeps capturing its LAST frame for a while (macOS
			// retains the backing store) instead of erroring, so a static picture
			// alone can't tell "idle" from "gone" — the closed-check below only
			// fires on a capture failure that may never come. While nothing is
			// changing, periodically confirm the window is still listed (the same
			// on-screen list the picker uses); if it isn't, it was closed/minimized
			// — drop the pane instead of freezing on the last frame forever.
			if changed {
				lastLiveCheck = time.Now()
			} else if time.Since(lastLiveCheck) >= winLiveCheck {
				lastLiveCheck = time.Now()
				if !b.exists(w.ID) {
					a.sendWindowClosed(conn, w.ID)
					return
				}
			}
			confirmed, probing := a.rtcSinks()
			a.viewerSizeMu.Lock()
			vc := a.viewerCount
			a.viewerSizeMu.Unlock()
			// Send a real frame when the picture changed, OR periodically while a
			// channel is open-but-unconfirmed so a static window can still prove
			// the channel (else P2P would never engage on an idle window).
			probeFrame := len(confirmed) == 0 && len(probing) > 0 && time.Since(lastSent) >= winProbeFrame
			if changed || probeFrame {
				seq++
				frame := struct {
					ID  string `json:"id"`
					W   int    `json:"w"`
					H   int    `json:"h"`
					Seq uint64 `json:"seq"`
					Img string `json:"img"` // base64 JPEG
				}{ID: w.ID, W: w.W, H: w.H, Seq: seq, Img: base64.StdEncoding.EncodeToString(img)}
				// Verify-before-switch: frames go over a DataChannel ONLY once it's
				// confirmed to deliver them (the viewer acked one over it). Until
				// then WS carries frames and any open channel is probed in parallel,
				// so a channel that connects but can't carry frames (cellular MTU)
				// never causes a stall. The demotion below only runs while we're
				// actively sending, so an idle window (no acks expected) can't be
				// mistaken for a broken channel; if acks dry up mid-stream, demote.
				if gotAnyAck && time.Since(lastAck) > streamAckIdleTimeout/2 {
					a.unconfirmRTC()
					confirmed, probing = a.rtcSinks()
				}
				if raw, mErr := json.Marshal(frame); mErr == nil {
					for _, dc := range confirmed {
						_ = dc.Send(raw)
					}
					// Also carry the frame over WS whenever a viewer isn't on a
					// confirmed DataChannel (a WS-only viewer, or none confirmed
					// yet). Sending ONLY to confirmed DCs froze every other viewer.
					// WS is skipped only when every viewer is confirmed on P2P, so
					// an all-P2P set still keeps frames off the billed relay.
					if wsSinkNeeded(vc, len(confirmed)) {
						a.sendWindowMsg(conn, protocol.TypeWindowFrame, frame)
					}
					for _, dc := range probing {
						_ = dc.Send(raw) // probe: prove it can carry a frame
					}
				}
				lastSig, haveSig = sig, ok
				lastSent = time.Now()
			} else if time.Since(lastSent) >= winHeartbeat {
				// Idle: no frame, just a few-byte liveness ping over the current
				// transport. Not acked and doesn't confirm a channel (only a real
				// frame's ack can) — it just keeps the viewer's overlay quiet.
				hb := struct {
					ID string `json:"id"`
					HB bool   `json:"hb"`
				}{ID: w.ID, HB: true}
				if raw, mErr := json.Marshal(hb); mErr == nil {
					for _, dc := range confirmed {
						_ = dc.Send(raw)
					}
					if wsSinkNeeded(vc, len(confirmed)) {
						a.sendWindowMsg(conn, protocol.TypeWindowFrame, hb)
					}
					for _, dc := range probing {
						_ = dc.Send(raw)
					}
				}
				lastSent = time.Now()
			}
		} else if !b.exists(w.ID) {
			// Capture failed and the window is gone from the list — it was closed
			// on the host. Tell the viewer to drop its pane, then stop.
			a.sendWindowClosed(conn, w.ID)
			return
		} else {
			// The window's still there but we're getting no pixels. Rather than
			// leave a silent blank/frozen pane, tell the viewer why (once it's
			// clearly persistent, then occasionally in case a frame was missed).
			// The usual macOS cause is missing Screen Recording permission.
			fails++
			if fails == 4 || fails%64 == 0 {
				msg := b.permissionHint()
				if msg == "" {
					if err != nil {
						msg = "Couldn't capture this window: " + err.Error()
					} else {
						msg = "Couldn't capture this window."
					}
				}
				a.sendWindowMsg(conn, protocol.TypeWindowFrame, struct {
					ID    string `json:"id"`
					Error string `json:"error"`
				}{ID: w.ID, Error: msg})
			}
		}
		// Floor the period so a fast capture + instant ack can't peg a core.
		if rem := windowFrameInterval - time.Since(start); rem > 0 {
			select {
			case <-stop:
				return
			case <-time.After(rem):
			}
		}
	}
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
