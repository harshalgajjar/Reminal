// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

// The browser's connection state, for a process that only wants to know it:
// `reminal integrate` (which waits for the extension to connect, and says so),
// `reminal integrate --check`, and the refusal a browser tool gets while no
// extension is connected. One of:
//
//	not set up        no browser here has reminal's host manifest
//	no daemon         the hub is not there to ask
//	no answer         the hub is there and does not know the question: a
//	                  daemon from before this, still running after an upgrade
//	never connected   set up, and no extension has connected yet
//	was connected     one connected before, and none is now
//	connected
//
// "Was connected" is told from "never" by a marker the hub writes as a bridge
// registers: ~/.reminal/browser-last.json, holding a time and the browser's
// family and nothing else (no profile, no account, no path, no version).
// Whether a window is open is not looked at: a browser that is closed and one
// left running with no window look the same from here, and the sentence says
// both.

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"reminal/internal/atomicfile"
)

// BrowserState is which of the states it is.
type BrowserState int

const (
	BrowserNotSetUp BrowserState = iota
	BrowserNoDaemon
	BrowserNeverConnected
	BrowserWasConnected
	BrowserConnected
	// BrowserNoAnswer: a daemon is running and its hub did not answer, so
	// whether an extension is connected is not known.
	BrowserNoAnswer
)

// BrowserStatus is what the hub and the marker say. The zero value is a hub
// that is not there.
type BrowserStatus struct {
	Daemon    bool      // the hub's socket took the connection
	NoAnswer  bool      // and the hub did not answer the question: Connected is not known
	Connected bool      // an extension is connected now
	Family    string    // of the browser connected now, or last: "chrome", "edge", ...
	Last      time.Time // when a bridge last registered; zero when none has
}

// BrowserSetUp reports whether a browser here has reminal's host manifest.
// The command sets it (it knows where each browser keeps its manifests); left
// nil, the browser counts as set up.
var BrowserSetUp func() bool

func browserSetUpHere() bool { return BrowserSetUp == nil || BrowserSetUp() }

// State is the state, given whether a browser here is set up.
func (s BrowserStatus) State(setUp bool) BrowserState {
	switch {
	case !setUp:
		return BrowserNotSetUp
	case !s.Daemon:
		return BrowserNoDaemon
	case s.NoAnswer:
		return BrowserNoAnswer
	case s.Connected:
		return BrowserConnected
	case !s.Last.IsZero():
		return BrowserWasConnected
	}
	return BrowserNeverConnected
}

// Sentence says the state in one sentence, with what to do next.
func (s BrowserStatus) Sentence(setUp bool) string {
	switch s.State(setUp) {
	case BrowserNotSetUp:
		return "The browser is not set up for reminal on this computer: run `reminal integrate`, then load reminal's extension in your browser."
	case BrowserNoDaemon:
		// A session starting is what starts the daemon (Agent.Run:
		// EnsureDaemonInstalled where its service is not installed; on
		// Windows runDirectoryHost too, where it is and the daemon is gone).
		return "reminal's daemon is not running on this computer, so the browser cannot be reached: starting a reminal session starts it (run `reminal new`), then try again."
	case BrowserNoAnswer:
		// No command to give: the older daemon sees its program replaced
		// within binaryWatchInterval and the new one takes its place by
		// itself on every system (watchBinaryAndExit).
		return "The reminal running in the background on this computer is an older one that cannot say whether the browser is connected: it is replaced by the new one within a minute of an upgrade, then run this again."
	case BrowserConnected:
		return BrowserFamilyName(s.Family) + " is connected: agents in your reminal sessions on this computer can use it."
	case BrowserWasConnected:
		who := "it"
		if browserFamilies[s.Family] != "" {
			who = browserFamilies[s.Family]
		}
		return "reminal's browser extension is not connected now, though " + who + " was connected before: the browser is closed or has no window open, so open a browser window (a browser left running with no window does not start the connection)."
	}
	return "reminal's browser extension is not connected, and has not connected before: load it in your browser (chrome://extensions, Developer mode, Load unpacked, then the reminal-extension folder that `reminal integrate` put in your home folder) and keep a browser window open."
}

// browserFamilies are the families a marker may name, and how each is said.
var browserFamilies = map[string]string{
	"chrome":   "Google Chrome",
	"edge":     "Microsoft Edge",
	"brave":    "Brave",
	"chromium": "Chromium",
	"arc":      "Arc",
}

// BrowserFamilyName is how a family is said to a person ("Your browser" for
// one not known).
func BrowserFamilyName(family string) string {
	if n := browserFamilies[family]; n != "" {
		return n
	}
	return "Your browser"
}

// browserFamily is the family of the browser whose program is at path (or
// named so), as browserBridgeBrowser found it. Read from the last few parts
// of the path only: a browser's own folders name it (Chromium's program is
// called chrome), and a folder further up may be named anything.
func browserFamily(path string) string {
	parts := strings.FieldsFunc(strings.ToLower(path), func(r rune) bool { return r == '/' || r == '\\' })
	if len(parts) == 0 {
		return ""
	}
	if len(parts) > 4 {
		parts = parts[len(parts)-4:]
	}
	tail := strings.Join(parts, "/")
	last := strings.TrimSuffix(parts[len(parts)-1], ".exe")
	switch {
	case strings.Contains(tail, "brave"):
		return "brave"
	case strings.Contains(tail, "edge"):
		return "edge"
	case strings.Contains(tail, "chromium"), last == "headless_shell":
		return "chromium"
	case last == "arc" || strings.HasPrefix(last, "arc ") || strings.Contains(tail, "arc.app"):
		return "arc"
	}
	return "chrome"
}

// browserMarker is the whole of browser-last.json.
type browserMarker struct {
	Time   time.Time `json:"time"`
	Family string    `json:"family"`
}

// browserMarkerPath is beside the hub's socket (so a development daemon
// keeps a marker of its own).
func browserMarkerPath() (string, error) {
	sock, err := BrowserSockPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(sock), "browser-last.json"), nil
}

// writeBrowserMarker notes that a browser of family connected at now.
func writeBrowserMarker(family string, now time.Time) {
	path, err := browserMarkerPath()
	if err != nil {
		return
	}
	if browserFamilies[family] == "" {
		family = ""
	}
	b, err := json.Marshal(browserMarker{Time: now.UTC().Truncate(time.Second), Family: family})
	if err != nil {
		return
	}
	_ = atomicfile.Write(path, append(b, '\n'), 0o600)
}

// readBrowserMarker is the marker, or false when there is none to read.
func readBrowserMarker() (browserMarker, bool) {
	path, err := browserMarkerPath()
	if err != nil {
		return browserMarker{}, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return browserMarker{}, false
	}
	var m browserMarker
	if json.Unmarshal(raw, &m) != nil || m.Time.IsZero() {
		return browserMarker{}, false
	}
	if browserFamilies[m.Family] == "" {
		m.Family = ""
	}
	return m, true
}

// status is the hub's own view: its bridge, and the marker.
func (h *browserHub) status() BrowserStatus {
	h.mu.Lock()
	connected, family := h.host != nil, h.family
	h.mu.Unlock()
	s := BrowserStatus{Daemon: true, Connected: connected}
	if m, ok := readBrowserMarker(); ok {
		s.Last, s.Family = m.Time, m.Family
	}
	if connected {
		s.Family = family
	}
	return s
}

// browserStateWire is the hub's answer to {"t":"state"} on browser.sock.
type browserStateWire struct {
	T         string `json:"t"`
	Connected bool   `json:"connected"`
	Family    string `json:"family,omitempty"`
	Last      string `json:"last,omitempty"`
}

func (s BrowserStatus) wire() browserStateWire {
	w := browserStateWire{T: "state", Connected: s.Connected, Family: s.Family}
	if !s.Last.IsZero() {
		w.Last = s.Last.UTC().Format(time.RFC3339)
	}
	return w
}

// browserStateWait bounds asking the hub its state: it answers at once.
const browserStateWait = 2 * time.Second

// QueryBrowserStatus asks the daemon's hub for the browser's state. With no
// hub to ask (its socket takes no connection), Daemon is false. A hub that
// takes the connection and gives no answer to the question (it closes, as a
// hub does on a message it does not know, or says nothing in time) is a
// daemon from before it knew the question: NoAnswer is set, whether an
// extension is connected is not known, and the marker is read from here.
func QueryBrowserStatus() BrowserStatus {
	sock, err := BrowserSockPath()
	if err != nil {
		return BrowserStatus{}
	}
	conn, err := net.DialTimeout("unix", sock, browserStateWait)
	if err != nil {
		return BrowserStatus{}
	}
	defer conn.Close()
	s := BrowserStatus{Daemon: true}
	_ = conn.SetDeadline(time.Now().Add(browserStateWait))
	var w browserStateWire
	asked := (&browserConn{c: conn}).send(map[string]string{"t": "state"}) == nil
	if asked {
		line, err := readBrowserLine(bufio.NewReaderSize(conn, 4<<10))
		asked = err == nil && json.Unmarshal(line, &w) == nil && w.T == "state"
	}
	if !asked {
		s.NoAnswer = true
		if m, ok := readBrowserMarker(); ok {
			s.Last, s.Family = m.Time, m.Family
		}
		return s
	}
	s.Connected = w.Connected
	if browserFamilies[w.Family] != "" {
		s.Family = w.Family
	}
	if t, err := time.Parse(time.RFC3339, w.Last); err == nil {
		s.Last = t
	}
	return s
}
