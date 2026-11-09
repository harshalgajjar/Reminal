// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"reminal/internal/protocol"
)

// Element-level control of a mirrored window, through agent-desktop
// (github.com/vercel-labs/agent-desktop). Pixels say what a window looks like;
// the OS accessibility tree says what is in it: this button, that text field.
// agent-desktop reads that tree on macOS, Windows and Linux and acts on one
// element without moving the pointer or raising the window. With it a viewer
// can:
//
//   - tap the mirrored pixels and press the control underneath ("tap"), so a
//     phone's fat finger lands on the nearest real button and the host's own
//     pointer and focus stay where they were;
//   - list a window's controls as native buttons and fields ("tree");
//   - act on one control directly, e.g. fill a text field in one go ("act").
//
// Where a window has no tree (a game, a canvas, a remote desktop), a tap comes
// back marked Pixel and the viewer sends an ordinary pixel click instead.
//
// On macOS this runs in the daemon, like pixel input (mirrorServeInput): an
// agent-desktop started there runs under reminal's own Accessibility grant,
// where one started from a session would need its terminal's.

// axReq is one viewer request, the plaintext of a window_ax message.
type axReq struct {
	ID     string  `json:"id"`  // window id, as in window_input
	Op     string  `json:"op"`  // "tree", "tap" or "act"
	Seq    int     `json:"seq"` // echoed back so the viewer can match replies
	X      float64 `json:"x"`   // tap point, a fraction of the window
	Y      float64 `json:"y"`
	Ref    string  `json:"ref"`    // act: the element, as a tree or tap gave it
	Action string  `json:"action"` // act: one of axActions
	Value  string  `json:"value"`  // act: the text for set-value
	Role   string  `json:"role"`   // find: the element to locate
	Name   string  `json:"name"`
}

// axEl is one actionable element, placed in fractions of its window so the
// viewer draws it over the frame at any size.
type axEl struct {
	Ref     string   `json:"ref"`
	Role    string   `json:"role"`
	Name    string   `json:"name,omitempty"`
	Value   string   `json:"value,omitempty"`
	States  []string `json:"states,omitempty"`
	X       float64  `json:"x"`
	Y       float64  `json:"y"`
	W       float64  `json:"w"`
	H       float64  `json:"h"`
	Tap     string   `json:"tap"` // what a tap on it does: click, toggle or edit
	Secret  bool     `json:"secret,omitempty"`
	Enabled bool     `json:"enabled"`
}

// axReply answers one axReq.
type axReply struct {
	ID    string `json:"id"`
	Op    string `json:"op"`
	Seq   int    `json:"seq"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	Els   []axEl `json:"els,omitempty"`   // tree
	Hit   *axEl  `json:"hit,omitempty"`   // tap: the element under the point
	Did   string `json:"did,omitempty"`   // tap/act: what was done to it
	Pixel bool   `json:"pixel,omitempty"` // tap: nothing there, send a pixel click
	Ms    int    `json:"ms"`
}

// axActions are the agent-desktop commands a viewer may run on one ref, and
// whether each takes the request's Value as its last argument. Anything else
// is refused: a viewer names a control and a verb, never a command line.
var axActions = map[string]bool{
	"click": false, "double-click": false, "right-click": false,
	"toggle": false, "check": false, "uncheck": false,
	"expand": false, "collapse": false, "focus": false,
	"clear": false, "scroll-to": false,
	"set-value": true, "select": true,
}

// axSnapTTL is how long a window's snapshot answers taps before it is taken
// again. A tap acts on what the person saw a moment ago; anything the window
// changed since comes back as a stale ref, which retakes it once.
const axSnapTTL = 4 * time.Second

// axCeiling bounds one agent-desktop run. The first snapshot of an app is the
// slow one (about 2s on a VM, while macOS wakes the app's tree up).
const axCeiling = 15 * time.Second

type axSnap struct {
	at  time.Time
	win winInfo
	els []axEl
}

// axDesktop holds the per-window snapshot cache. One per process: the daemon
// serves every viewer of the machine from it.
type axDesktop struct {
	mu    sync.Mutex
	snaps map[string]axSnap
	// electron records, per app, whether its tree needs agent-desktop's
	// --force-electron-a11y to show (true) or was tried without gain (false).
	electron map[string]bool
}

var desktopAX axDesktop

// axBinary finds agent-desktop: $REMINAL_AGENT_DESKTOP, then PATH, then the
// places a package manager puts it. A daemon started by launchd or systemd
// has a bare PATH, so PATH alone would miss an npm or Homebrew install.
func axBinary() string {
	if p := os.Getenv("REMINAL_AGENT_DESKTOP"); p != "" {
		return p
	}
	name := "agent-desktop"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	home, _ := os.UserHomeDir()
	for _, dir := range []string{
		filepath.Join(home, ".reminal", "bin"),
		"/opt/homebrew/bin", "/usr/local/bin",
		filepath.Join(home, ".npm-global", "bin"),
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, ".bun", "bin"),
		filepath.Join(os.Getenv("APPDATA"), "npm"),
	} {
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// axAvailable reports whether element-level control can be offered here.
func axAvailable() bool { return axBinary() != "" }

// axEnvelope is agent-desktop's one-envelope-per-command output.
type axEnvelope struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// axRun runs one agent-desktop command and decodes its envelope. A failed
// command still prints an envelope (and exits non-zero), so the envelope, not
// the exit status, says what happened.
func axRun(args ...string) (axEnvelope, error) {
	bin := axBinary()
	if bin == "" {
		return axEnvelope{}, errors.New("agent-desktop is not installed on this machine")
	}
	ctx, cancel := context.WithTimeout(context.Background(), axCeiling)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	runErr := cmd.Run()
	var env axEnvelope
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		if runErr != nil {
			return env, runErr
		}
		return env, errors.New("agent-desktop: unreadable output")
	}
	if !env.OK {
		if env.Error != nil {
			return env, &axError{code: env.Error.Code, msg: env.Error.Message}
		}
		return env, errors.New("agent-desktop: command failed")
	}
	return env, nil
}

type axError struct{ code, msg string }

func (e *axError) Error() string {
	if e.msg != "" {
		return e.msg
	}
	return e.code
}

// axStale reports whether err means the window changed under a ref.
func axStale(err error) bool {
	var ae *axError
	return errors.As(err, &ae) && (ae.code == "STALE_REF" || ae.code == "AMBIGUOUS_TARGET" || ae.code == "ELEMENT_NOT_FOUND")
}

// axScope names a window to agent-desktop. Its window ids are the numbers
// reminal already uses, "w-" in front: the CGWindowNumber on macOS and the
// HWND on Windows. Naming the window beats naming its app: a Windows store
// app's window belongs to ApplicationFrameHost, the process reminal lists,
// while agent-desktop files it under the app inside (CalculatorApp.exe), so
// an app-name scope there finds no window and the controls never appear.
// Elsewhere the app name scopes it and the bounds pick the window.
func axScope(w winInfo) []string {
	if n := axWindowNumber(w.ID); n != "" {
		return []string{"--window-id", "w-" + n}
	}
	return []string{"--app", w.App}
}

// axWindowNumber is the number in a window id agent-desktop also knows, or
// "" (a display, a Linux X window id, a test's name).
func axWindowNumber(id string) string { return axWindowNumberOn(runtime.GOOS, id) }

func axWindowNumberOn(goos, id string) string {
	switch goos {
	case "windows":
		id = strings.TrimPrefix(id, "hwnd:")
	case "darwin":
	default:
		return ""
	}
	if id == "" || isDisplayID(id) || strings.Trim(id, "0123456789") != "" {
		return ""
	}
	return id
}

// axNode is the part of agent-desktop's snapshot tree this reads.
type axNode struct {
	Ref      string   `json:"ref_id"`
	Role     string   `json:"role"`
	Name     string   `json:"name"`
	Value    string   `json:"value"`
	Desc     string   `json:"description"`
	States   []string `json:"states"`
	Actions  []string `json:"available_actions"`
	Bounds   *axRect  `json:"bounds"`
	Children []axNode `json:"children"`
}

type axRect struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"width"`
	H float64 `json:"height"`
}

// axTapKind says what a tap on an element of this role should do: text
// widgets are edited (the viewer asks for the text and sets it whole),
// checkboxes and switches toggle, and everything else is clicked.
func axTapKind(role string) string {
	switch role {
	case "textfield", "textarea", "searchfield", "combobox", "securetextfield", "spinbutton":
		return "edit"
	case "checkbox", "switch", "toggle":
		return "toggle"
	}
	return "click"
}

// axFlatten walks a snapshot tree into window-relative elements, keeping the
// ones that have a ref and a box that overlaps the window.
func axFlatten(n axNode, w winInfo, out []axEl) []axEl {
	if n.Ref != "" && n.Bounds != nil && n.Bounds.W > 0 && n.Bounds.H > 0 && w.W > 0 && w.H > 0 {
		e := axEl{
			Ref: n.Ref, Role: n.Role, Name: n.Name, Value: n.Value, States: n.States,
			X: (n.Bounds.X - float64(w.X)) / float64(w.W), Y: (n.Bounds.Y - float64(w.Y)) / float64(w.H),
			W: n.Bounds.W / float64(w.W), H: n.Bounds.H / float64(w.H),
			Tap: axTapKind(n.Role), Enabled: true,
		}
		if e.Name == "" {
			e.Name = n.Desc
		}
		for _, s := range n.States {
			if s == "disabled" {
				e.Enabled = false
			}
		}
		if n.Role == "securetextfield" || hasState(n.States, "protected") {
			e.Secret, e.Value = true, ""
		}
		if e.X < 1 && e.Y < 1 && e.X+e.W > 0 && e.Y+e.H > 0 {
			out = append(out, e)
		}
	}
	for _, c := range n.Children {
		out = axFlatten(c, w, out)
	}
	return out
}

func hasState(states []string, s string) bool {
	for _, x := range states {
		if x == s {
			return true
		}
	}
	return false
}

// axHit returns the smallest enabled element containing (fx, fy): the most
// specific control under a finger, not the panel around it.
func axHit(els []axEl, fx, fy float64) *axEl {
	var best *axEl
	for i := range els {
		e := &els[i]
		if !e.Enabled || fx < e.X || fy < e.Y || fx > e.X+e.W || fy > e.Y+e.H {
			continue
		}
		if best == nil || e.W*e.H < best.W*best.H {
			best = e
		}
	}
	return best
}

// snapshot returns w's elements, from the cache when it is fresh and the
// window has not moved (fresh=false), or newly read.
func (d *axDesktop) snapshot(w winInfo, fresh bool) ([]axEl, error) {
	d.mu.Lock()
	s, ok := d.snaps[w.ID]
	d.mu.Unlock()
	if ok && !fresh && time.Since(s.at) < axSnapTTL && s.win.X == w.X && s.win.Y == w.Y && s.win.W == w.W && s.win.H == w.H {
		return s.els, nil
	}
	force, known := d.electronFor(w)
	els, err := axRead(w, force)
	if err != nil {
		return nil, err
	}
	// Next to nothing came back: an Electron app (Slack, VS Code, Figma's
	// shell) keeps its web UI out of the tree until a client asks for it.
	// Ask once per app, and remember the answer either way.
	if !force && !known && len(els) <= axSparse && axForceSupported() {
		if more, err := axRead(w, true); err == nil && len(more) > len(els) {
			els, force = more, true
		}
		d.mu.Lock()
		if d.electron == nil {
			d.electron = map[string]bool{}
		}
		d.electron[w.App] = force
		d.mu.Unlock()
	}
	d.mu.Lock()
	if d.snaps == nil {
		d.snaps = map[string]axSnap{}
	}
	d.snaps[w.ID] = axSnap{at: time.Now(), win: w, els: els}
	d.mu.Unlock()
	return els, nil
}

// forget drops w's snapshot after an action changed the window.
func (d *axDesktop) forget(id string) {
	d.mu.Lock()
	delete(d.snaps, id)
	d.mu.Unlock()
}

// serve answers one viewer request against backend b.
func (d *axDesktop) serve(b windowBackend, req axReq) (rep axReply) {
	start := time.Now()
	rep = axReply{ID: req.ID, Op: req.Op, Seq: req.Seq}
	defer func() { rep.Ms = int(time.Since(start) / time.Millisecond) }()
	w, err := findWindow(b, req.ID)
	if err != nil {
		rep.Error = err.Error()
		return rep
	}
	switch req.Op {
	case "tree":
		els, err := d.snapshot(w, true)
		if err != nil {
			rep.Error = err.Error()
			return rep
		}
		rep.OK, rep.Els = true, els
	case "tap":
		d.tap(w, req, &rep)
	case "find":
		// Locate an element another program named (see watchAgentDesktop).
		for attempt := 0; attempt < 2 && rep.Hit == nil; attempt++ {
			els, err := d.snapshot(w, attempt > 0)
			if err != nil {
				rep.Error = err.Error()
				return rep
			}
			for i := range els {
				if els[i].Role == req.Role && els[i].Name == req.Name {
					h := els[i]
					rep.Hit = &h
					break
				}
			}
		}
		rep.OK = rep.Hit != nil
	case "act":
		if err := d.act(w, req.Ref, req.Action, req.Value); err != nil {
			rep.Error = err.Error()
			return rep
		}
		rep.OK, rep.Did = true, req.Action
	default:
		rep.Error = "unknown op"
	}
	return rep
}

// tap presses the control under (req.X, req.Y). An edit control is only
// reported: the viewer collects the text and comes back with set-value.
func (d *axDesktop) tap(w winInfo, req axReq, rep *axReply) {
	for attempt := 0; attempt < 2; attempt++ {
		els, err := d.snapshot(w, attempt > 0)
		if err != nil {
			// No tree here at all (or no agent-desktop): pixels still work.
			rep.OK, rep.Pixel, rep.Error = true, true, err.Error()
			return
		}
		hit := axHit(els, req.X, req.Y)
		if hit == nil {
			rep.OK, rep.Pixel = true, true
			return
		}
		h := *hit
		rep.Hit = &h
		if h.Tap == "edit" {
			rep.OK, rep.Did = true, "edit"
			return
		}
		err = d.act(w, h.Ref, h.Tap, "")
		if err == nil {
			rep.OK, rep.Did = true, h.Tap
			return
		}
		if !axStale(err) {
			rep.Error = err.Error()
			return
		}
	}
	rep.OK, rep.Pixel = true, true
}

// act runs one whitelisted action on ref.
func (d *axDesktop) act(w winInfo, ref, action, value string) error {
	takesValue, ok := axActions[action]
	if !ok {
		return errors.New("unsupported action")
	}
	if !strings.HasPrefix(ref, "@") || strings.ContainsAny(ref, " \t\n") {
		return errors.New("bad ref")
	}
	args := []string{action, ref}
	if takesValue {
		// After "--" a value that starts with "-" is text, not a flag.
		args = append(args, "--", value)
	}
	_, err := axRun(args...)
	d.forget(w.ID)
	return err
}

// handleWindowAX answers one window_ax request. On macOS the daemon serves it
// (mirrorServeAX), so agent-desktop runs under reminal's Accessibility grant;
// elsewhere this agent's own window backend does.
func (a *Agent) handleWindowAX(conn *websocket.Conn, encData string) {
	plaintext, err := a.box.Decrypt(encData)
	if err != nil {
		return
	}
	var req axReq
	if json.Unmarshal(plaintext, &req) != nil || req.ID == "" {
		return
	}
	a.markInput()
	rep := a.axServe(req)
	if rep.Did != "" && rep.Did != "edit" {
		a.noteWindowFlush(req.ID) // ship the frame that shows the result
	}
	a.sendWindowMsg(conn, protocol.TypeWindowAX, rep)
}

// axSparse is how few controls make a tree look empty: a window's own frame
// buttons and little else.
const axSparse = 6

// axForceSupported reports whether agent-desktop's --force-electron-a11y means
// anything here. It sets macOS's AXManualAccessibility on the app.
var axForceSupported = func() bool { return runtime.GOOS == "darwin" }

// electronFor says whether w's app gets --force-electron-a11y, and whether that
// is already known (from its bundle, or from an earlier sparse snapshot).
func (d *axDesktop) electronFor(w winInfo) (force, known bool) {
	if !axForceSupported() {
		return false, true
	}
	if axIsElectron(w.AppPath) {
		return true, true
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	force, known = d.electron[w.App]
	return force, known
}

// axIsElectron reports whether an app bundle is built on Electron.
func axIsElectron(appPath string) bool {
	if appPath == "" {
		return false
	}
	st, err := os.Stat(filepath.Join(appPath, "Contents", "Frameworks", "Electron Framework.framework"))
	return err == nil && st.IsDir()
}

// axRead takes one snapshot of w and places its controls in the window.
func axRead(w winInfo, forceElectron bool) ([]axEl, error) {
	args := append([]string{"snapshot", "-i", "--include-bounds", "--compact"}, axScope(w)...)
	if forceElectron {
		args = append(args, "--force-electron-a11y")
	}
	env, err := axRun(args...)
	if err != nil {
		return nil, err
	}
	var data struct {
		Tree axNode `json:"tree"`
	}
	if err := json.Unmarshal(env.Data, &data); err != nil {
		return nil, err
	}
	return axFlatten(data.Tree, w, nil), nil
}
