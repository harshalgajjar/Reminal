// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

// The browser hub. An agent in a reminal session drives the person's own
// Chrome through reminal's extension, entirely on this machine:
//
//	agent → `reminal mcp` (browser_* tools) → the session's control socket
//	      → this hub (the daemon's ~/.reminal/browser.sock)
//	      → `reminal browser-host` (Chrome's native messaging host)
//	      → the extension (extensions/chrome), which asks the person first.
//
// On browser.sock every message is one line of JSON. The bridge (the host)
// says {"t":"host"} first and then carries messages both ways; a session
// sends one request {"t":"req","session":…,"run":…,"tool":…,"args":…} and
// reads one answer. A request is taken only from the process that is that
// session: its peer pid (SO_PEERCRED, LOCAL_PEERPID, SIO_AF_UNIX_GETPEERPID) must be the session's
// own, so another program of the same user cannot use a consent the person
// gave a session. A bridge is taken only from reminal's own browser-host,
// started by a browser of Chrome's family. {"t":"state"} is answered to any
// process of the person's (the socket is theirs alone): whether a browser is
// connected, and nothing a consent guards (browserstate.go).

import (
	"bufio"
	"encoding/json"
	"errors"
	"log"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"reminal/internal/proc"
	"reminal/internal/session"
)

// BrowserWait is how long a browser request may take, end to end: the
// person's answer to the consent window (up to a minute), then the action.
const BrowserWait = 90 * time.Second

// browserMaxLine bounds one message on browser.sock: a full-page screenshot,
// base64, with room to spare.
const browserMaxLine = 64 << 20

// BrowserSockPath is the daemon's browser socket.
func BrowserSockPath() (string, error) {
	if p := os.Getenv("REMINAL_BROWSER_SOCK"); p != "" {
		return p, nil // a development daemon beside the installed one
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".reminal", "browser.sock"), nil
}

// BrowserHostSockPath is the socket as reminal's helper (the browser's
// native messaging host) reaches the hub: inside a snap browser, the one in
// that snap's own folder, the only one it may reach.
func BrowserHostSockPath() (string, error) {
	if os.Getenv("REMINAL_BROWSER_SOCK") == "" {
		if p := browserHostSockInSnap(); p != "" {
			return p, nil
		}
	}
	return BrowserSockPath()
}

// BrowserHostHome is the person's home folder as the helper finds it: a snap
// gives its programs a home of its own, and says the real one beside it.
func BrowserHostHome() (string, error) {
	if browserHostSockInSnap() != "" {
		if h := os.Getenv("SNAP_REAL_HOME"); h != "" {
			return h, nil
		}
	}
	return os.UserHomeDir()
}

// snapEnsure opens the hub's second socket when a snap browser was set up
// (integrate wrote its note in ~/.reminal), and closes it when that setup
// was taken away. It is served as the first is: the same handle, with the
// same checks of who is at the other end. A daemon on a socket of its
// own (REMINAL_BROWSER_SOCK, development) leaves it to the installed one.
func (h *browserHub) snapEnsure() {
	if os.Getenv("REMINAL_BROWSER_SOCK") != "" {
		return
	}
	// Off the caller, and one at a time: the hub's first socket and a state
	// asked of it never wait on the snap's folder. A look asked for while
	// one is under way is not lost: the one under way looks again.
	h.snapAgain.Store(true)
	go func() {
		for h.snapAgain.Load() {
			if !h.snapMu.TryLock() {
				return
			}
			h.snapAgain.Store(false)
			h.snapLook()
			h.snapMu.Unlock()
		}
	}()
}

// snapLook is one look at the snap's folder, with snapMu held.
func (h *browserHub) snapLook() {
	if h.snapHold != nil {
		defer h.snapHold() // after it has looked, still holding the lock
	}
	home := h.snapHome
	if home == "" || h.snapDone {
		return
	}
	if h.snapLn != nil {
		if snapBrowserGone(home) {
			h.snapCloseLocked()
		}
		return
	}
	ln, dir, err := snapBrowserListen(home)
	if err != nil {
		return
	}
	h.snapLn, h.snapDir = ln, dir
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return // closed
			}
			go h.handleFrom(conn, true)
		}
	}()
}

func (h *browserHub) snapClose() {
	h.snapMu.Lock()
	defer h.snapMu.Unlock()
	h.snapDone = true
	h.snapCloseLocked()
}

func (h *browserHub) snapCloseLocked() {
	if h.snapLn == nil {
		return
	}
	_ = h.snapLn.Close()
	snapBrowserUnlisten(h.snapDir)
	h.snapLn, h.snapDir = nil, nil
}

// BrowserReq is a session's request, and what the hub hands the bridge (with
// its own id). Run names the session's run (when its process started), so the
// extension's consent belongs to that run alone.
type BrowserReq struct {
	T       string          `json:"t"`
	ID      uint64          `json:"id,omitempty"`
	Session string          `json:"session"`
	Run     string          `json:"run,omitempty"`
	Name    string          `json:"name,omitempty"`
	Machine string          `json:"machine,omitempty"`
	Tool    string          `json:"tool"`
	Args    json.RawMessage `json:"args,omitempty"`
}

// BrowserResp is the extension's answer.
type BrowserResp struct {
	T      string          `json:"t,omitempty"`
	ID     uint64          `json:"id,omitempty"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// errNoBrowser is what a request gets while no extension is connected: which
// of the states it is (browserstate.go), and what to do next.
func (h *browserHub) errNoBrowser() error {
	return errors.New(h.status().Sentence(browserSetUpHere()))
}

type browserHub struct {
	mu     sync.Mutex
	host   *browserConn
	family string // the browser family of host, for the state (browserstate.go)
	// standby: the bridges of other browsers running with the extension at
	// the same time, kept connected and sent nothing (serveHost). The first
	// of them becomes the bridge when the one in use goes.
	standby []*browserConn
	nextID  uint64
	waiting map[uint64]waiter
	// seen: the sessions that have asked, by the pid of their process (the
	// peer of their request), told when that process is gone. Not by their
	// record: a live session's record can be missing for a while (judged
	// stale after a clock jump, say), and an end told then took its tabs
	// away mid-work.
	seen map[string]int

	// The second socket, in a snap browser's own folder (browsersnap_linux.go),
	// served while a setup is there: its listener and the folder it is in.
	snapMu  sync.Mutex
	snapLn  net.Listener
	snapDir *os.File
	// snapHome: the home folder as it was when the hub began to serve, the
	// one whose snap folder it looks at from then on. snapDone: the hub has
	// stopped; a look still on its way opens nothing.
	snapHome string
	snapDone bool
	// snapAgain: a look at the snap's folder was asked for while one was
	// under way; the one under way looks once more before it is done.
	snapAgain atomic.Bool
	// tests: the wait of browserSnapGrace, when not 0 (read with mu held).
	snapGrace time.Duration
	// tests: called by a look when it is done, while it still holds snapMu.
	snapHold func()

	// tests: whether peer pid may make a request for session (nil: the
	// session's own process, by its active record); whether peer may be the
	// bridge (nil: reminal's browser-host under a browser).
	allow       func(peer int, session string) bool
	allowBridge func(peer int) bool
	// tests: the browser a bridge from peer belongs to (nil: all one).
	testBrowser func(peer int) int
	noWait      bool   // tests: no wait for a bridge
	testFamily  string // tests: the family of a bridge allowBridge lets in
}

// waiter is a request waiting for its answer, and the bridge it went to.
type waiter struct {
	ch  chan BrowserResp
	via *browserConn
}

type browserConn struct {
	c   net.Conn
	pid int
	// browser: the pid of the browser that started this bridge (0: not
	// known). snap: it came in on the snap browser's socket. family: its
	// browser's, for the state.
	browser int
	snap    bool
	family  string
	mu      sync.Mutex // one writer at a time
}

func (b *browserConn) send(v any) error {
	line, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	_ = b.c.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, err = b.c.Write(append(line, '\n'))
	return err
}

func newBrowserHub() *browserHub {
	return &browserHub{waiting: map[uint64]waiter{}, seen: map[string]int{}}
}

// ServeBrowser serves browser.sock until stop closes.
func ServeBrowser(stop <-chan struct{}) {
	serveBrowserOn(newBrowserHub(), stop)
}

func serveBrowserOn(h *browserHub, stop <-chan struct{}) {
	sock, err := BrowserSockPath()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		return
	}
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return
	}
	_ = os.Chmod(sock, 0o600)
	go func() {
		<-stop
		_ = ln.Close()
		_ = os.Remove(sock)
		h.snapClose()
	}()
	go h.watchEnded(stop)
	if home, err := os.UserHomeDir(); err == nil {
		h.snapMu.Lock()
		h.snapHome = home
		h.snapMu.Unlock()
	}
	h.snapEnsure()
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-stop:
				return
			default:
			}
			time.Sleep(50 * time.Millisecond)
			continue
		}
		go h.handle(conn)
	}
}

func (h *browserHub) handle(conn net.Conn) { h.handleFrom(conn, false) }

// handleFrom serves one connection; snap: it came in on the snap browser's
// socket.
func (h *browserHub) handleFrom(conn net.Conn, snap bool) {
	defer func() {
		if rec := recover(); rec != nil {
			recoverLog("browser hub", rec)
		}
	}()
	r := bufio.NewReaderSize(conn, 64<<10)
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	first, err := readBrowserLine(r)
	if err != nil {
		conn.Close()
		return
	}
	var m BrowserReq
	if json.Unmarshal(first, &m) != nil {
		conn.Close()
		return
	}
	switch m.T {
	case "host":
		h.serveHost(conn, r, snap)
	case "req":
		defer conn.Close()
		resp := h.request(conn, m)
		_ = (&browserConn{c: conn}).send(resp)
	case "state":
		// Whether a browser is connected (browserstate.go): asked by
		// `reminal integrate`, which is no session.
		defer conn.Close()
		// Asked by a setup just made (or just taken away): the moment the
		// snap browser's socket is opened (or closed).
		h.snapEnsure()
		_ = (&browserConn{c: conn}).send(h.status().wire())
	default:
		conn.Close()
	}
}

// readBrowserLine reads one line of at most browserMaxLine bytes.
func readBrowserLine(r *bufio.Reader) ([]byte, error) {
	var out []byte
	for {
		chunk, err := r.ReadSlice('\n')
		out = append(out, chunk...)
		if len(out) > browserMaxLine {
			return nil, errors.New("message too long")
		}
		if err == nil {
			return out[:len(out)-1], nil
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return nil, err
		}
	}
}

// serveHost takes conn in as a bridge, if it is reminal's browser-host
// started by a browser. There is one bridge in use at a time:
//
//   - with none, this one is it;
//   - one from the same browser as the bridge in use takes over from it (the
//     extension reconnected: the older one's browser port is gone), said in
//     the daemon's log; the requests the older one held are failed;
//   - one from another browser does not: it is kept connected on standby and
//     sent nothing, and becomes the bridge when the one in use goes. Two
//     browsers running with the extension at once took the bridge from each
//     other without end before, and a session's tabs with it;
//   - except that a bridge on the hub's own socket takes over from one on
//     the snap browser's socket, which then stands by. One from the snap's
//     socket never takes a bridge over.
func (h *browserHub) serveHost(conn net.Conn, r *bufio.Reader, snap bool) {
	peer, err := peerPID(conn)
	// The browser above the host says which family it is, kept for the
	// state (browserstate.go).
	okay, family, above := false, "", 0
	if err == nil && h.allowBridge != nil {
		okay, family = h.allowBridge(peer), h.testFamily
		if h.testBrowser != nil {
			above = h.testBrowser(peer)
		}
	} else if err == nil {
		var name string
		name, above = browserBridgeAbove(peer)
		okay, family = name != "", browserFamily(name)
	}
	if err != nil || !okay {
		if err != nil {
			log.Printf("browser: refused a bridge: cannot tell which process it is: %v", err)
		} else {
			log.Printf("browser: refused a bridge from pid %d: not reminal's browser-host under a browser", peer)
		}
		_ = (&browserConn{c: conn}).send(map[string]string{"t": "refused", "error": "not reminal's browser host"})
		_ = conn.Close()
		return
	}
	bc := &browserConn{c: conn, pid: peer, browser: above, snap: snap, family: family}
	// What a later "not connected" is told apart by: one connected before.
	// Written first, so the state never says connected with no time for it.
	writeBrowserMarker(family, time.Now())
	h.mu.Lock()
	old := h.host
	var replaced *browserConn
	switch {
	case old == nil:
		h.host, h.family = bc, family
	case old.browser == bc.browser && old.snap == bc.snap:
		h.host, h.family = bc, family
		log.Printf("browser: the bridge from pid %d replaces the one from pid %d", peer, old.pid)
		replaced = old
	case old.snap && !bc.snap:
		h.host, h.family = bc, family
		replaced = h.standBy(old)
		log.Printf("browser: the bridge from pid %d is in use now; the snap browser's (pid %d) stands by", peer, old.pid)
	default:
		replaced = h.standBy(bc)
		log.Printf("browser: another browser's bridge (pid %d) stands by; the one from pid %d stays in use", peer, old.pid)
	}
	h.mu.Unlock()
	if replaced != nil {
		_ = replaced.c.Close()
	}
	_ = conn.SetReadDeadline(time.Time{})
	for {
		line, err := readBrowserLine(r)
		if err != nil {
			break
		}
		var resp BrowserResp
		if json.Unmarshal(line, &resp) != nil || resp.ID == 0 {
			// Not an answer: the extension saying which sessions it holds
			// tabs for, as it connects.
			var have struct {
				T        string   `json:"t"`
				Sessions []string `json:"sessions"`
			}
			if json.Unmarshal(line, &have) == nil && have.T == "have" {
				go h.haveSessions(have.Sessions)
			}
			continue
		}
		h.mu.Lock()
		w, ok := h.waiting[resp.ID]
		mine := ok && w.via == bc // an answer only from the bridge it went to
		if mine {
			delete(h.waiting, resp.ID)
		}
		h.mu.Unlock()
		if mine {
			w.ch <- resp
		}
	}
	h.mu.Lock()
	for i, sb := range h.standby {
		if sb == bc {
			h.standby = append(h.standby[:i], h.standby[i+1:]...)
			break
		}
	}
	if h.host == bc {
		h.host, h.family = nil, ""
		// The next browser's bridge. When the one that went was on the
		// hub's own socket and only the snap browser's stands by, that one
		// waits a moment first: a browser's extension reconnects often, and
		// its bridge coming back is the one to use (the requests of that
		// moment wait for a bridge, as they do with none).
		if !h.promoteLocked(bc.snap) {
			grace := browserSnapGrace
			if h.snapGrace > 0 {
				grace = h.snapGrace
			}
			time.AfterFunc(grace, func() {
				h.mu.Lock()
				h.promoteLocked(true)
				h.mu.Unlock()
			})
		}
	}
	// The requests this bridge held: answered now, not at their deadline.
	for id, w := range h.waiting {
		if w.via == bc {
			delete(h.waiting, id)
			w.ch <- BrowserResp{ID: id, Error: "the browser extension disconnected"}
		}
	}
	h.mu.Unlock()
	_ = conn.Close()
}

// browserStandbyMax is how many bridges stand by at most: one more and the
// oldest is let go.
const browserStandbyMax = 4

// browserSnapGrace is how long the snap browser's bridge on standby waits
// before it is used in place of one on the hub's own socket that went.
const browserSnapGrace = 5 * time.Second

// standBy puts bc on standby, with h.mu held. Over browserStandbyMax the
// oldest is taken off and returned, for the caller to close.
func (h *browserHub) standBy(bc *browserConn) (dropped *browserConn) {
	h.standby = append(h.standby, bc)
	if len(h.standby) > browserStandbyMax {
		dropped = h.standby[0]
		h.standby = append([]*browserConn(nil), h.standby[1:]...)
	}
	return dropped
}

// promoteLocked makes a bridge on standby the one in use, when none is, with
// h.mu held: one on the hub's own socket first; the snap browser's only
// when snapToo. It reports whether nothing is left to wait for (a bridge is
// in use, or none stands by).
func (h *browserHub) promoteLocked(snapToo bool) bool {
	if h.host != nil || len(h.standby) == 0 {
		return true
	}
	next := -1
	for i, sb := range h.standby {
		if !sb.snap {
			next = i
			break
		}
	}
	if next < 0 {
		if !snapToo {
			return false
		}
		next = 0
	}
	h.host, h.family = h.standby[next], h.standby[next].family
	h.standby = append(h.standby[:next], h.standby[next+1:]...)
	log.Printf("browser: the bridge from pid %d is in use now", h.host.pid)
	return true
}

// bridges are the bridge in use and those on standby.
func (h *browserHub) bridges() []*browserConn {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []*browserConn
	if h.host != nil {
		out = append(out, h.host)
	}
	return append(out, h.standby...)
}

// browserBridgeOK: pid is reminal (this daemon's own binary, or one named
// reminal) run as the browser host, with a browser of Chrome's family above
// it (the one that started it for its extension).
func browserBridgeOK(pid int) bool { return browserBridgeBrowser(pid) != "" }

// browserBridgeBrowser is that check, answering with the program name of the
// browser found above the host ("" when pid is not the host under one).
func browserBridgeBrowser(pid int) string {
	name, _ := browserBridgeAbove(pid)
	return name
}

// browserBridgeAbove is browserBridgeBrowser, with the pid of that browser.
func browserBridgeAbove(pid int) (string, int) {
	args := processArgs(pid)
	exe := browserExecPath(pid)
	if exe == "" && len(args) > 0 {
		exe = args[0]
	}
	self, _ := os.Executable()
	if r, err := filepath.EvalSymlinks(self); err == nil {
		self = r
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	if exe == "" || (exe != self && !strings.HasPrefix(strings.ToLower(filepath.Base(exe)), "reminal")) {
		return "", 0
	}
	asHost := false
	for _, a := range args {
		if a == "browser-host" || strings.HasPrefix(a, "chrome-extension://") {
			asHost = true
		}
	}
	if !asHost {
		return "", 0
	}
	p := pid
	for i := 0; i < 8; i++ {
		p = session.ParentPID(p)
		if p <= 1 {
			return "", 0
		}
		path := browserExecPath(p)
		name := filepath.Base(path)
		if name == "." || name == "" || name == "/" {
			if a := processArgs(p); len(a) > 0 {
				path = a[0]
				name = filepath.Base(path)
			}
		}
		// (Windows names its programs chrome.exe, msedge.exe.)
		if len(name) > 4 && strings.EqualFold(name[len(name)-4:], ".exe") {
			name = name[:len(name)-4]
		}
		if browserProcessName.MatchString(name) {
			return path, p
		}
	}
	return "", 0
}

// browserProcessName matches the program names of Chrome's family, Chrome for
// Testing among them (what browser automation downloads and runs).
var browserProcessName = regexp.MustCompile(`(?i)^(google chrome|google chrome for testing|chrome|chromium|chromium-browser|msedge|microsoft edge|brave|brave browser|brave-browser|arc|headless_shell)( (helper|beta|dev|canary).*)?$`)

// request checks who is asking, passes the request to the bridge and waits
// for its answer.
func (h *browserHub) request(conn net.Conn, m BrowserReq) BrowserResp {
	if m.Session == "" || m.Tool == "" {
		return BrowserResp{Error: "bad request"}
	}
	peer, err := peerPID(conn)
	if err != nil {
		return BrowserResp{Error: "cannot tell which process is asking: " + err.Error()}
	}
	allow := h.allow
	if allow == nil {
		allow = sessionOwnsPID
	}
	if !allow(peer, m.Session) {
		return BrowserResp{Error: "only session " + m.Session + " itself may ask for its browser use"}
	}
	// With no bridge just now, wait a while for one: the extension's worker
	// may be asleep, and wakes on its alarm (every 30 s) to connect again.
	host := h.waitForHost(h.hostWait())
	h.mu.Lock()
	if host == nil || h.host != host {
		host = h.host
	}
	if host == nil {
		h.mu.Unlock()
		return BrowserResp{Error: h.errNoBrowser().Error()}
	}
	// The snap browser reads only some folders: said here, where it is
	// known which browser the request goes to.
	if host.snap {
		if home, err := os.UserHomeDir(); err == nil {
			for _, path := range browserArgPaths(m.Tool, m.Args) {
				if !snapBrowserCanRead(home, path) {
					h.mu.Unlock()
					return BrowserResp{Error: path + ": the browser in use is Chromium installed as a snap, which reads files only in the home folder (not in a hidden folder). Put the file there, or ask the person to upload it."}
				}
			}
		}
	}
	h.nextID++
	id := h.nextID
	ch := make(chan BrowserResp, 1)
	h.waiting[id] = waiter{ch: ch, via: host}
	h.seen[m.Session] = peer
	h.mu.Unlock()

	m.T, m.ID = "req", id
	if err := host.send(m); err != nil {
		h.mu.Lock()
		delete(h.waiting, id)
		h.mu.Unlock()
		return BrowserResp{Error: "the browser extension did not take the request: " + err.Error()}
	}
	select {
	case resp := <-ch:
		resp.ID = 0
		return resp
	case <-time.After(BrowserWait):
		h.mu.Lock()
		delete(h.waiting, id)
		h.mu.Unlock()
		return BrowserResp{Error: "the browser did not answer in time"}
	}
}

// sessionOwnsPID: peer is the process of the live session named.
func sessionOwnsPID(peer int, id string) bool {
	rec, err := session.ReadActiveByID(id)
	return err == nil && rec.PID > 0 && rec.PID == peer
}

// watchEnded tells the extension when a session that used the browser is
// gone: its consent goes with it, and its tab group is shown as an ended
// session's for the person to close.
func (h *browserHub) watchEnded(stop <-chan struct{}) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		h.tellEnded()
	}
}

// tellEnded tells the extension of the sessions that have ended. Told only
// when it can be: while no extension is connected they are kept, to be told
// once one is.
func (h *browserHub) tellEnded() {
	h.mu.Lock()
	var ended []string
	for id, pid := range h.seen {
		if !proc.Alive(pid) {
			ended = append(ended, id)
		}
	}
	host := h.host
	standby := append([]*browserConn(nil), h.standby...)
	h.mu.Unlock()
	if host == nil {
		return
	}
	for _, id := range ended {
		// A browser on standby may hold tabs of the session too.
		for _, sb := range standby {
			_ = sb.send(map[string]string{"t": "ended", "session": id})
		}
		if host.send(map[string]string{"t": "ended", "session": id}) == nil {
			h.mu.Lock()
			delete(h.seen, id)
			h.mu.Unlock()
		}
	}
}

// AskBrowser sends one request to the hub and returns its answer: the
// session's side (control verb "browser").
func AskBrowser(req BrowserReq) (BrowserResp, error) {
	sock, err := BrowserSockPath()
	if err != nil {
		return BrowserResp{}, err
	}
	// Where the browser is set up, reminal's daemon not answering is most
	// often it starting again (after an upgrade it is away for a few
	// seconds): waited for, a little, before it is said to be not running.
	// Refusing at once told a person to fix what was not broken, and the
	// next request worked.
	setUp := browserSetUpHere()
	var conn net.Conn
	for deadline := time.Now().Add(browserDialWait); ; {
		if conn, err = net.DialTimeout("unix", sock, 2*time.Second); err == nil || !setUp || !time.Now().Before(deadline) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err != nil {
		return BrowserResp{}, errors.New(BrowserStatus{}.Sentence(setUp))
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(BrowserTotalWait + 5*time.Second))
	req.T = "req"
	if err := (&browserConn{c: conn}).send(req); err != nil {
		return BrowserResp{}, err
	}
	line, err := readBrowserLine(bufio.NewReaderSize(conn, 64<<10))
	if err != nil {
		return BrowserResp{}, err
	}
	var resp BrowserResp
	if err := json.Unmarshal(line, &resp); err != nil {
		return BrowserResp{}, err
	}
	return resp, nil
}

// browserDialWait is how long a request goes on trying to reach reminal's
// daemon before saying it is not running: longer than the daemon is away
// when it is started again (the service manager's own delay included). A
// var for tests.
var browserDialWait = 12 * time.Second

// browserHostWait is how long a request waits for a bridge when there is
// none: past the extension's alarm, which wakes a sleeping worker to connect.
const browserHostWait = 40 * time.Second

// BrowserTotalWait is the longest a request can take at the hub: waiting for
// a bridge, then for the answer.
const BrowserTotalWait = browserHostWait + BrowserWait

func (h *browserHub) hostWait() time.Duration {
	if h.noWait {
		return 0
	}
	return browserHostWait
}

// waitForHost returns the bridge, waiting up to d for one to connect.
func (h *browserHub) waitForHost(d time.Duration) *browserConn {
	deadline := time.Now().Add(d)
	for {
		h.mu.Lock()
		host := h.host
		h.mu.Unlock()
		if host != nil || !time.Now().Before(deadline) {
			return host
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// browserHaveRecheck is how long a session the extension holds tabs for,
// and that looks gone, is left before it is looked at again and the
// extension told (a live session's record can be missing for a moment).
var browserHaveRecheck = 15 * time.Second

var browserSessionID = regexp.MustCompile(`^[A-Z0-9]{4,32}$`)

// haveSessions takes the sessions the extension says it holds tabs for (as
// it connects, and again every half minute and when its panel opens, less
// the ones already told as ended). The hub knows the sessions that asked it
// since it started; a session that ended while the hub was not running
// (reminal was being updated), or before this hub's time, it never knew, and
// the extension would go on showing it as running. Each one named is taken up again if it
// is running (to be told of when it ends), and told as ended if, looked at
// twice, it is not.
func (h *browserHub) haveSessions(ids []string) {
	if len(ids) > 200 {
		ids = ids[:200]
	}
	alive := func(id string) int {
		rec, err := session.ReadActiveByID(id)
		if err == nil && rec.PID > 0 && proc.Alive(rec.PID) {
			return rec.PID
		}
		return 0
	}
	var gone []string
	for _, id := range ids {
		if !browserSessionID.MatchString(id) {
			continue
		}
		h.mu.Lock()
		_, known := h.seen[id]
		h.mu.Unlock()
		if known {
			continue
		}
		if pid := alive(id); pid != 0 {
			h.mu.Lock()
			h.seen[id] = pid
			h.mu.Unlock()
		} else {
			gone = append(gone, id)
		}
	}
	if len(gone) == 0 {
		return
	}
	time.Sleep(browserHaveRecheck)
	for _, id := range gone {
		if pid := alive(id); pid != 0 {
			h.mu.Lock()
			h.seen[id] = pid
			h.mu.Unlock()
			continue
		}
		if all := h.bridges(); len(all) > 0 {
			log.Printf("browser: session %s ended while reminal was not looking: its tabs are let go", id)
			for _, b := range all {
				_ = b.send(map[string]string{"t": "ended", "session": id})
			}
		}
	}
}

// browserArgPaths are the files a request asks the browser to read: the
// paths of a file_upload, alone or as a step of a batch.
func browserArgPaths(tool string, args json.RawMessage) []string {
	if tool != "file_upload" && tool != "batch" {
		return nil
	}
	var v any
	if json.Unmarshal(args, &v) != nil {
		return nil
	}
	var out []string
	var walk func(v any, depth int)
	walk = func(v any, depth int) {
		if depth > 6 {
			return
		}
		switch t := v.(type) {
		case map[string]any:
			for k, x := range t {
				if list, ok := x.([]any); ok && k == "paths" {
					for _, e := range list {
						if p, ok := e.(string); ok {
							out = append(out, p)
						}
					}
					continue
				}
				walk(x, depth+1)
			}
		case []any:
			for _, x := range t {
				walk(x, depth+1)
			}
		}
	}
	walk(v, 0)
	return out
}
