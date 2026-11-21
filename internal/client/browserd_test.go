// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build linux || darwin

package client

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// startHub serves a hub on a socket of the test's own, with allow deciding
// who may ask.
func startHub(t *testing.T, allow func(peer int, session string) bool) *browserHub {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "b.sock")
	if len(sock) > 100 { // a unix socket's path is short
		dir, err := os.MkdirTemp("/tmp", "rb")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(dir) })
		sock = filepath.Join(dir, "b.sock")
	}
	t.Setenv("REMINAL_BROWSER_SOCK", sock)
	h := newBrowserHub()
	h.allow = allow
	h.allowBridge = func(int) bool { return true } // the test process stands in for the browser host
	h.noWait = true
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go serveBrowserOn(h, stop)
	for i := 0; i < 100; i++ {
		if c, err := net.Dial("unix", sock); err == nil {
			c.Close()
			return h
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("hub did not start")
	return nil
}

// fakeHost joins the hub as the browser and answers every request with
// answer(req).
func fakeHost(t *testing.T, answer func(BrowserReq) BrowserResp) {
	t.Helper()
	sock, _ := BrowserSockPath()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if _, err := c.Write([]byte(`{"t":"host"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	go func() {
		r := bufio.NewReaderSize(c, 1<<16)
		for {
			line, err := readBrowserLine(r)
			if err != nil {
				return
			}
			var req BrowserReq
			if json.Unmarshal(line, &req) != nil || req.T != "req" {
				continue
			}
			resp := answer(req)
			resp.ID = req.ID
			_ = (&browserConn{c: c}).send(resp)
		}
	}()
	time.Sleep(50 * time.Millisecond) // the hub has taken it as the host
}

// A session's request reaches the browser with the session's name and the
// tool, and its answer comes back to that session.
func TestBrowserHubCarriesARequestAndItsAnswer(t *testing.T) {
	startHub(t, func(int, string) bool { return true })
	var got BrowserReq
	fakeHost(t, func(r BrowserReq) BrowserResp {
		got = r
		return BrowserResp{OK: true, Result: json.RawMessage(`"Clicked."`)}
	})
	resp, err := AskBrowser(BrowserReq{Session: "S1", Name: "build", Machine: "box", Tool: "click", Args: json.RawMessage(`{"ref":"ref_3"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.OK || string(resp.Result) != `"Clicked."` {
		t.Fatalf("answer: %+v", resp)
	}
	if got.Session != "S1" || got.Name != "build" || got.Machine != "box" || got.Tool != "click" || string(got.Args) != `{"ref":"ref_3"}` || got.ID == 0 {
		t.Fatalf("the browser got %+v", got)
	}
}

// Only the session's own process may ask in its name: another program of
// the same user, naming a session the person allowed, is refused, and the
// browser never hears of it.
func TestBrowserHubRefusesAProcessThatIsNotTheSession(t *testing.T) {
	var asked []int
	startHub(t, func(peer int, s string) bool { asked = append(asked, peer); return false })
	heard := false
	fakeHost(t, func(BrowserReq) BrowserResp { heard = true; return BrowserResp{OK: true} })
	resp, err := AskBrowser(BrowserReq{Session: "S1", Tool: "navigate"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.OK || !strings.Contains(resp.Error, "only session S1 itself") || heard {
		t.Fatalf("answer %+v, the browser heard it: %v", resp, heard)
	}
	if len(asked) != 1 || asked[0] != os.Getpid() {
		t.Fatalf("the peer read as %v, want this process (%d)", asked, os.Getpid())
	}
}

// The real check: a process is that session only if the session's active
// record names it.
func TestSessionOwnsPIDNeedsTheSessionsOwnRecord(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if sessionOwnsPID(os.Getpid(), "NOSUCH1") {
		t.Fatal("a session with no record owns this process")
	}
}

// With no browser connected, the session is told how to connect one.
func TestBrowserHubWithNoBrowserSaysHowToConnectOne(t *testing.T) {
	startHub(t, func(int, string) bool { return true })
	resp, err := AskBrowser(BrowserReq{Session: "S1", Tool: "tabs_context"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.OK || !strings.Contains(resp.Error, "`reminal integrate`") || strings.Contains(resp.Error, "integrate chrome") {
		t.Fatalf("answer: %+v", resp)
	}
}

// A full-size screenshot (tens of MB as base64) comes back whole, through
// the hub and through the session's control verb.
func TestABigScreenshotComesBackWhole(t *testing.T) {
	startHub(t, func(int, string) bool { return true })
	png := strings.Repeat("iVBORw0KGgo", 2_000_000) // 22 MB
	fakeHost(t, func(BrowserReq) BrowserResp {
		return BrowserResp{OK: true, Result: json.RawMessage(`{"image":"` + png + `","mimeType":"image/png"}`)}
	})
	a := &Agent{sessionID: "S1"}
	req, _ := json.Marshal(map[string]any{"tool": "screenshot", "args": map[string]any{}})
	out, err := a.handleBrowserControl(nil, base64.StdEncoding.EncodeToString(req))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(out)
	if err != nil {
		t.Fatal(err)
	}
	var resp BrowserResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	var shot struct{ Image string }
	if !resp.OK || json.Unmarshal(resp.Result, &shot) != nil || shot.Image != png {
		t.Fatalf("the screenshot came back as %d bytes, not %d (ok %v, %s)", len(shot.Image), len(png), resp.OK, resp.Error)
	}
}

// The browser going away answers what it held at once, not at the deadline.
func TestBrowserHubAnswersWhenTheBrowserGoes(t *testing.T) {
	startHub(t, func(int, string) bool { return true })
	sock, _ := BrowserSockPath()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = c.Write([]byte(`{"t":"host"}` + "\n"))
	go func() { // takes the request, then leaves without an answer
		_, _ = bufio.NewReader(c).ReadString('\n')
		c.Close()
	}()
	time.Sleep(50 * time.Millisecond)
	start := time.Now()
	resp, err := AskBrowser(BrowserReq{Session: "S1", Tool: "navigate"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.OK || !strings.Contains(resp.Error, "disconnected") || time.Since(start) > 5*time.Second {
		t.Fatalf("answer %+v after %v", resp, time.Since(start))
	}
}

// A message longer than the limit is refused, not read into memory.
func TestBrowserLineHasALimit(t *testing.T) {
	r := bufio.NewReaderSize(strings.NewReader(strings.Repeat("x", browserMaxLine+10)+"\n"), 1<<16)
	if _, err := readBrowserLine(r); err == nil {
		t.Fatal("an over-long line was read")
	}
}

// On the session's control socket, the browser verb is taken only from a
// process running in the session: one that is not is refused before the
// hub hears of it.
func TestTheBrowserVerbIsOnlyForProcessesInTheSession(t *testing.T) {
	startHub(t, func(int, string) bool { return true })
	heard := 0
	fakeHost(t, func(BrowserReq) BrowserResp { heard++; return BrowserResp{OK: true, Result: json.RawMessage(`"ok"`)} })
	dir, err := os.MkdirTemp("/tmp", "rc")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	ln, err := net.Listen("unix", filepath.Join(dir, "c.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	ask := func() error {
		c, err := net.Dial("unix", filepath.Join(dir, "c.sock"))
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		srv, err := ln.Accept()
		if err != nil {
			t.Fatal(err)
		}
		defer srv.Close()
		req, _ := json.Marshal(map[string]any{"tool": "tabs_context"})
		_, err = (&Agent{sessionID: "S1"}).handleBrowserControl(srv, base64.StdEncoding.EncodeToString(req))
		return err
	}
	was := browserCallerOK
	defer func() { browserCallerOK = was }()
	// The real check: with no session records here, this process is in no
	// session, so not in this one.
	t.Setenv("HOME", t.TempDir())
	if err := ask(); err == nil || !strings.Contains(err.Error(), "only a program running in this session") || heard != 0 {
		t.Fatalf("a process in no session: %v (heard %d)", err, heard)
	}
	browserCallerOK = func(int) bool { return true } // as if run in it
	if err := ask(); err != nil || heard != 1 {
		t.Fatalf("a process in the session: %v (heard %d)", err, heard)
	}
}

// The session names its run (when its process started): a new run, the
// same id, is another run to the extension, which asks again.
func TestTheRunReachesTheBrowser(t *testing.T) {
	startHub(t, func(int, string) bool { return true })
	var got []string
	fakeHost(t, func(r BrowserReq) BrowserResp {
		got = append(got, r.Run)
		return BrowserResp{OK: true, Result: json.RawMessage(`"ok"`)}
	})
	req, _ := json.Marshal(map[string]any{"tool": "tabs_context"})
	first := &Agent{sessionID: "S1", startedAt: time.Unix(1000, 0)}
	again := &Agent{sessionID: "S1", startedAt: time.Unix(2000, 0)}
	for _, a := range []*Agent{first, first, again} {
		if _, err := a.handleBrowserControl(nil, base64.StdEncoding.EncodeToString(req)); err != nil {
			t.Fatal(err)
		}
	}
	if len(got) != 3 || got[0] == "" || got[0] != got[1] || got[0] == got[2] {
		t.Fatalf("runs the browser saw: %q", got)
	}
}

// Only reminal's browser host, started by a browser, may be the bridge: any
// other program that connects is refused, and requests still say no
// browser is connected.
func TestTheBridgeMustBeReminalsHost(t *testing.T) {
	h := startHub(t, func(int, string) bool { return true })
	h.allowBridge = func(int) bool { return false }
	sock, _ := BrowserSockPath()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _ = c.Write([]byte(`{"t":"host"}` + "\n"))
	line, _ := bufio.NewReader(c).ReadString('\n')
	if !strings.Contains(line, `"refused"`) {
		t.Fatalf("a bridge from a program that is not the host: %q", line)
	}
	resp, _ := AskBrowser(BrowserReq{Session: "S1", Tool: "tabs_context"})
	if resp.OK || !strings.Contains(resp.Error, "not connected") {
		t.Fatalf("after it: %+v", resp)
	}
	// The real check: this test process is not reminal's browser host.
	if browserBridgeOK(os.Getpid()) {
		t.Fatal("the test process passes as the browser host")
	}
}

// A newer bridge takes over; a request the older one held fails, one sent to
// the newer is answered by it, and the older cannot answer for it.
func TestAHandoverFailsOnlyTheOlderBridgesRequests(t *testing.T) {
	startHub(t, func(int, string) bool { return true })
	sock, _ := BrowserSockPath()
	old, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	_, _ = old.Write([]byte(`{"t":"host"}` + "\n"))
	took := make(chan uint64, 1)
	go func() { // takes a request, never answers it
		line, _ := bufio.NewReader(old).ReadString('\n')
		var r BrowserReq
		_ = json.Unmarshal([]byte(line), &r)
		took <- r.ID
	}()
	time.Sleep(50 * time.Millisecond)
	held := make(chan BrowserResp, 1)
	go func() { r, _ := AskBrowser(BrowserReq{Session: "S1", Tool: "navigate"}); held <- r }()
	oldID := <-took
	fakeHost(t, func(r BrowserReq) BrowserResp {
		return BrowserResp{OK: true, Result: json.RawMessage(`"from the newer"`)}
	})
	select {
	case r := <-held:
		if r.OK || !strings.Contains(r.Error, "disconnected") {
			t.Fatalf("the older bridge's request: %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the older bridge's request was not failed at the handover")
	}
	_, _ = old.Write([]byte(`{"id":` + strconv.FormatUint(oldID+1, 10) + `,"ok":true,"result":"from the older"}` + "\n"))
	r, err := AskBrowser(BrowserReq{Session: "S1", Tool: "navigate"})
	if err != nil || !r.OK || string(r.Result) != `"from the newer"` {
		t.Fatalf("a request after the handover: %+v, %v", r, err)
	}
}

// The extension is told a session ended only when one is there to be told:
// with none connected, the session is kept, and told once one is.
func TestAnEndIsToldOnlyToABrowser(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no session records: every session has ended
	h := startHub(t, func(int, string) bool { return true })
	h.mu.Lock()
	h.seen["GONE1"] = exitedPID(t)
	h.seen["LIVE1"] = os.Getpid() // live, though its record is not here (HOME is empty)
	h.mu.Unlock()
	h.tellEnded()
	h.mu.Lock()
	_, kept := h.seen["GONE1"]
	h.mu.Unlock()
	if !kept {
		t.Fatal("an end was marked told with no browser to tell")
	}
	sock, _ := BrowserSockPath()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _ = c.Write([]byte(`{"t":"host"}` + "\n"))
	time.Sleep(50 * time.Millisecond)
	h.tellEnded()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, _ := bufio.NewReader(c).ReadString('\n')
	if !strings.Contains(line, `"ended"`) || !strings.Contains(line, "GONE1") {
		t.Fatalf("the browser was told %q", line)
	}
}

// exitedPID is the pid of a process that has run and ended.
func exitedPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

// A live session whose record is missing (a record judged stale after a
// clock jump is removed) is not told ended: its tabs are not taken away
// mid-work. Only its process ending ends it.
func TestALiveSessionWithoutItsRecordIsNotEnded(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := startHub(t, func(int, string) bool { return true })
	h.mu.Lock()
	h.seen["LIVE1"] = os.Getpid()
	h.mu.Unlock()
	var told []string
	fakeHost(t, func(BrowserReq) BrowserResp { return BrowserResp{OK: true} })
	sock, _ := BrowserSockPath()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _ = c.Write([]byte(`{"t":"host"}` + "\n"))
	time.Sleep(50 * time.Millisecond)
	h.tellEnded()
	_ = c.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	if line, _ := bufio.NewReader(c).ReadString('\n'); line != "" {
		told = append(told, line)
	}
	h.mu.Lock()
	_, kept := h.seen["LIVE1"]
	h.mu.Unlock()
	if len(told) > 0 || !kept {
		t.Fatalf("a live session was told ended: %q (kept %v)", told, kept)
	}
}

// With no bridge for a moment (the extension's worker asleep, its alarm about
// to wake it), a request waits for one rather than failing.
func TestARequestWaitsForTheBrowserToComeBack(t *testing.T) {
	h := startHub(t, func(int, string) bool { return true })
	h.mu.Lock()
	h.noWait = false
	h.mu.Unlock()
	go func() {
		time.Sleep(500 * time.Millisecond)
		fakeHost(t, func(BrowserReq) BrowserResp { return BrowserResp{OK: true, Result: json.RawMessage(`"back"`)} })
	}()
	resp, err := AskBrowser(BrowserReq{Session: "S1", Tool: "tabs_context"})
	if err != nil || !resp.OK || string(resp.Result) != `"back"` {
		t.Fatalf("%+v, %v", resp, err)
	}
}
