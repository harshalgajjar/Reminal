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
// On browser.sock every message is one line of JSON. The host says
// {"t":"host"} first and then carries messages both ways; a session sends one
// request {"t":"req","session":…,"tool":…,"args":…} and reads one answer.
// A request is taken only from the process that is that session: its peer
// pid (SO_PEERCRED, LOCAL_PEERPID) must be the session's own, so another
// program of the same user cannot use a consent the person gave a session.

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

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

// BrowserReq is a session's request, and what the hub hands the host (with
// its own id).
type BrowserReq struct {
	T       string          `json:"t"`
	ID      uint64          `json:"id,omitempty"`
	Session string          `json:"session"`
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
var errNoBrowser = errors.New("the reminal Chrome extension is not connected: run `reminal integrate chrome`, load the extension, and open Chrome")

type browserHub struct {
	mu      sync.Mutex
	host    *browserConn
	nextID  uint64
	waiting map[uint64]chan BrowserResp
	seen    map[string]bool // sessions that have asked, told when they end

	// tests: whether peer pid may make a request for session (nil: the
	// session's own process, by its active record).
	allow func(peer int, session string) bool
}

type browserConn struct {
	c  net.Conn
	mu sync.Mutex // one writer at a time
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
	return &browserHub{waiting: map[uint64]chan BrowserResp{}, seen: map[string]bool{}}
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

// serveHost makes conn the host (one at a time: a newer one replaces the
// older) and hands its answers to the requests waiting for them.
func (h *browserHub) serveHost(conn net.Conn, r *bufio.Reader) {
	bc := &browserConn{c: conn}
	h.mu.Lock()
	old := h.host
	h.host = bc
	h.mu.Unlock()
	if old != nil {
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
			continue
		}
		h.mu.Lock()
		ch := h.waiting[resp.ID]
		delete(h.waiting, resp.ID)
		h.mu.Unlock()
		if ch != nil {
			ch <- resp
		}
	}
	h.mu.Lock()
	if h.host == bc {
		h.host = nil
	}
	// Requests the gone host held: answered now, not at their deadline.
	for id, ch := range h.waiting {
		delete(h.waiting, id)
		ch <- BrowserResp{ID: id, Error: "the Chrome extension disconnected"}
	}
	h.mu.Unlock()
	_ = conn.Close()
}

// request checks who is asking, passes the request to the host and waits for
// its answer.
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
	h.mu.Lock()
	host := h.host
	if host == nil {
		h.mu.Unlock()
		return BrowserResp{Error: errNoBrowser.Error()}
	}
	h.nextID++
	id := h.nextID
	ch := make(chan BrowserResp, 1)
	h.waiting[id] = ch
	h.seen[m.Session] = true
	h.mu.Unlock()

	m.T, m.ID = "req", id
	if err := host.send(m); err != nil {
		h.mu.Lock()
		delete(h.waiting, id)
		h.mu.Unlock()
		return BrowserResp{Error: "the Chrome extension did not take the request: " + err.Error()}
	}
	select {
	case resp := <-ch:
		resp.ID = 0
		return resp
	case <-time.After(BrowserWait):
		h.mu.Lock()
		delete(h.waiting, id)
		h.mu.Unlock()
		return BrowserResp{Error: "Chrome did not answer in time"}
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
		h.mu.Lock()
		var ended []string
		for id := range h.seen {
			if rec, err := session.ReadActiveByID(id); err != nil || rec.PID <= 0 {
				ended = append(ended, id)
			}
		}
		host := h.host
		h.mu.Unlock()
		for _, id := range ended {
			if host == nil || host.send(map[string]string{"t": "ended", "session": id}) == nil {
				h.mu.Lock()
				delete(h.seen, id)
				h.mu.Unlock()
			}
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
	_ = conn.SetDeadline(time.Now().Add(BrowserWait + 5*time.Second))
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
