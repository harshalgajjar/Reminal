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
// session: its peer pid (SO_PEERCRED, LOCAL_PEERPID) must be the session's
// own, so another program of the same user cannot use a consent the person
// gave a session. A bridge is taken only from reminal's own browser-host,
// started by a browser of Chrome's family.

import (
	"bufio"
	"encoding/json"
	"errors"
	"log"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
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

// errNoBrowser is what a request gets while no extension is connected.
var errNoBrowser = errors.New("reminal's browser extension is not connected: run `reminal integrate`, load the extension in your browser, and open it")

type browserHub struct {
	mu      sync.Mutex
	host    *browserConn
	nextID  uint64
	waiting map[uint64]waiter
	// seen: the sessions that have asked, by the pid of their process (the
	// peer of their request), told when that process is gone. Not by their
	// record: a live session's record can be missing for a while (judged
	// stale after a clock jump, say), and an end told then took its tabs
	// away mid-work.
	seen map[string]int

	// tests: whether peer pid may make a request for session (nil: the
	// session's own process, by its active record); whether peer may be the
	// bridge (nil: reminal's browser-host under a browser).
	allow       func(peer int, session string) bool
	allowBridge func(peer int) bool
	noWait      bool // tests: no wait for a bridge
}

// waiter is a request waiting for its answer, and the bridge it went to.
type waiter struct {
	ch  chan BrowserResp
	via *browserConn
}

type browserConn struct {
	c   net.Conn
	pid int
	mu  sync.Mutex // one writer at a time
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

// ServeBrowser serves browser.sock until stop closes. Not on Windows in
// this first version: a caller's pid cannot be read there the same way, and
// without it any program could act as a session the person allowed.
func ServeBrowser(stop <-chan struct{}) {
	if runtime.GOOS == "windows" {
		return
	}
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
	}()
	go h.watchEnded(stop)
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

func (h *browserHub) handle(conn net.Conn) {
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
		h.serveHost(conn, r)
	case "req":
		defer conn.Close()
		resp := h.request(conn, m)
		_ = (&browserConn{c: conn}).send(resp)
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

// serveHost makes conn the bridge, if it is reminal's browser-host started by
// a browser. A newer one takes over from an older (the extension reconnected:
// the older one's browser port is gone), and says so in the daemon's log; the
// requests the older one held are failed, the newer one's are not.
func (h *browserHub) serveHost(conn net.Conn, r *bufio.Reader) {
	peer, err := peerPID(conn)
	ok := h.allowBridge
	if ok == nil {
		ok = browserBridgeOK
	}
	if err != nil || !ok(peer) {
		log.Printf("browser: refused a bridge from pid %d: not reminal's browser-host under a browser", peer)
		_ = (&browserConn{c: conn}).send(map[string]string{"t": "refused", "error": "not reminal's browser host"})
		_ = conn.Close()
		return
	}
	bc := &browserConn{c: conn, pid: peer}
	h.mu.Lock()
	old := h.host
	h.host = bc
	h.mu.Unlock()
	if old != nil {
		log.Printf("browser: the bridge from pid %d replaces the one from pid %d", peer, old.pid)
		_ = old.c.Close()
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
	if h.host == bc {
		h.host = nil
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

// browserBridgeOK: pid is reminal (this daemon's own binary, or one named
// reminal) run as the browser host, with a browser of Chrome's family above
// it (the one that started it for its extension).
func browserBridgeOK(pid int) bool {
	args := processArgs(pid)
	exe := processExecPath(pid)
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
		return false
	}
	asHost := false
	for _, a := range args {
		if a == "browser-host" || strings.HasPrefix(a, "chrome-extension://") {
			asHost = true
		}
	}
	if !asHost {
		return false
	}
	p := pid
	for i := 0; i < 8; i++ {
		p = session.ParentPID(p)
		if p <= 1 {
			return false
		}
		name := filepath.Base(processExecPath(p))
		if name == "." || name == "" || name == "/" {
			if a := processArgs(p); len(a) > 0 {
				name = filepath.Base(a[0])
			}
		}
		if browserProcessName.MatchString(name) {
			return true
		}
	}
	return false
}

// browserProcessName matches the program names of Chrome's family.
var browserProcessName = regexp.MustCompile(`(?i)^(google chrome|chrome|chromium|chromium-browser|msedge|microsoft edge|brave|brave browser|brave-browser|arc|headless_shell)( (helper|beta|dev|canary).*)?$`)

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
		return BrowserResp{Error: errNoBrowser.Error()}
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
// gone: its consent and its tab group go with it.
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
	h.mu.Unlock()
	if host == nil {
		return
	}
	for _, id := range ended {
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
	conn, err := net.DialTimeout("unix", sock, 2*time.Second)
	if err != nil {
		return BrowserResp{}, errors.New("reminal's daemon is not serving the browser (is `reminal daemon` running?)")
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
// it connects). The hub knows the sessions that asked it since it started;
// a session that ended while the hub was not running (reminal was being
// updated), or before this hub's time, it never knew, and the extension
// would keep its tab group for ever. Each one named is taken up again if it
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
		h.mu.Lock()
		host := h.host
		h.mu.Unlock()
		if host != nil {
			log.Printf("browser: session %s ended while reminal was not looking: its tabs are let go", id)
			_ = host.send(map[string]string{"t": "ended", "session": id})
		}
	}
}
