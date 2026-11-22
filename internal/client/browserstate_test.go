// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build linux || darwin

package client

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// joinAsHost joins the hub as the browser's host and returns the connection,
// for a test that takes the browser away again itself.
func joinAsHost(t *testing.T) net.Conn {
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
	waitBrowserStatus(t, func(s BrowserStatus) bool { return s.Connected })
	return c
}

// waitBrowserStatus asks the hub until its answer is as want says.
func waitBrowserStatus(t *testing.T, want func(BrowserStatus) bool) BrowserStatus {
	t.Helper()
	var s BrowserStatus
	for i := 0; i < 200; i++ {
		if s = QueryBrowserStatus(); want(s) {
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the hub's state stayed %+v", s)
	return s
}

// Each state is said in a sentence of its own, with what to do next; the one
// for a browser that connected before says to have a window open.
func TestBrowserStateSentences(t *testing.T) {
	then := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name  string
		s     BrowserStatus
		setUp bool
		state BrowserState
		want  []string
	}{
		{"not set up", BrowserStatus{Daemon: true}, false, BrowserNotSetUp, []string{"not set up", "`reminal integrate`"}},
		{"no daemon", BrowserStatus{}, true, BrowserNoDaemon, []string{"daemon is not running", "`reminal daemon --install`"}},
		{"no answer", BrowserStatus{Daemon: true, NoAnswer: true, Last: then, Family: "edge"}, true, BrowserNoAnswer, []string{"an older one that cannot say whether the browser is connected", "`reminal restart --all`"}},
		{"no answer, nothing set up", BrowserStatus{Daemon: true, NoAnswer: true}, false, BrowserNotSetUp, []string{"not set up"}},
		{"never connected", BrowserStatus{Daemon: true}, true, BrowserNeverConnected, []string{"has not connected before", "Load unpacked", "`reminal integrate`", "window open"}},
		{"was connected", BrowserStatus{Daemon: true, Last: then, Family: "edge"}, true, BrowserWasConnected, []string{"not connected now", "Microsoft Edge was connected before", "open a browser window", "no window does not start"}},
		{"was connected, family not known", BrowserStatus{Daemon: true, Last: then}, true, BrowserWasConnected, []string{"though it was connected before"}},
		{"connected", BrowserStatus{Daemon: true, Connected: true, Family: "brave", Last: then}, true, BrowserConnected, []string{"Brave is connected"}},
	} {
		if got := c.s.State(c.setUp); got != c.state {
			t.Errorf("%s: state %d, want %d", c.name, got, c.state)
		}
		said := c.s.Sentence(c.setUp)
		for _, w := range c.want {
			if !strings.Contains(said, w) {
				t.Errorf("%s: %q does not say %q", c.name, said, w)
			}
		}
		if strings.Contains(said, "\n") || strings.Contains(said, "\u2014") {
			t.Errorf("%s: not one plain line: %q", c.name, said)
		}
	}
}

// The marker a connecting browser leaves holds a time and a family, and
// nothing else.
func TestBrowserMarkerHoldsOnlyATimeAndAFamily(t *testing.T) {
	h := startHub(t, func(int, string) bool { return true })
	h.testFamily = "chrome"
	before := time.Now().Add(-2 * time.Second)
	joinAsHost(t)
	path, err := browserMarkerPath()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no marker: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
	if len(m) != 2 || m["family"] != "chrome" {
		t.Fatalf("the marker holds %s", raw)
	}
	when, _ := m["time"].(string)
	at, err := time.Parse(time.RFC3339, when)
	if err != nil || at.Before(before) || at.After(time.Now().Add(2*time.Second)) {
		t.Fatalf("the marker's time: %q (%v)", when, err)
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("the marker's mode: %v %v", st, err)
	}
	// A family that is none of the known ones is not written down.
	writeBrowserMarker("/Users/someone/Applications/Other.app", time.Now())
	raw, _ = os.ReadFile(path)
	if strings.Contains(string(raw), "someone") {
		t.Fatalf("the marker took a path: %s", raw)
	}
}

// The hub says its state to a process that asks: never connected, connected
// (and which family), then connected before once the browser is gone. A
// request refused in each says which it is.
func TestBrowserStatusFollowsTheBridge(t *testing.T) {
	h := startHub(t, func(int, string) bool { return true })
	h.testFamily = "edge"
	s := QueryBrowserStatus()
	if !s.Daemon || s.Connected || !s.Last.IsZero() || s.State(true) != BrowserNeverConnected {
		t.Fatalf("before any browser: %+v", s)
	}
	resp, err := AskBrowser(BrowserReq{Session: "S1", Tool: "tabs_context"})
	if err != nil || resp.OK || !strings.Contains(resp.Error, "has not connected before") {
		t.Fatalf("refused before any browser: %+v %v", resp, err)
	}

	c := joinAsHost(t)
	s = QueryBrowserStatus()
	if !s.Connected || s.Family != "edge" || s.Last.IsZero() || s.State(true) != BrowserConnected {
		t.Fatalf("with a browser: %+v", s)
	}

	c.Close()
	s = waitBrowserStatus(t, func(s BrowserStatus) bool { return !s.Connected })
	if !s.Daemon || s.Family != "edge" || s.Last.IsZero() || s.State(true) != BrowserWasConnected {
		t.Fatalf("after the browser went: %+v", s)
	}
	resp, err = AskBrowser(BrowserReq{Session: "S1", Tool: "tabs_context"})
	if err != nil || resp.OK || !strings.Contains(resp.Error, "Microsoft Edge was connected before") || !strings.Contains(resp.Error, "open a browser window") {
		t.Fatalf("refused after the browser went: %+v %v", resp, err)
	}

	// With no browser set up here, that is what it says.
	BrowserSetUp = func() bool { return false }
	t.Cleanup(func() { BrowserSetUp = nil })
	resp, err = AskBrowser(BrowserReq{Session: "S1", Tool: "tabs_context"})
	if err != nil || resp.OK || !strings.Contains(resp.Error, "not set up") {
		t.Fatalf("refused with nothing set up: %+v %v", resp, err)
	}
}

// With no daemon the state is that, and a request says so.
func TestBrowserStatusWithNoDaemon(t *testing.T) {
	t.Setenv("REMINAL_BROWSER_SOCK", filepath.Join(t.TempDir(), "none.sock"))
	s := QueryBrowserStatus()
	if s.Daemon || s.Connected || s.State(true) != BrowserNoDaemon {
		t.Fatalf("with no daemon: %+v", s)
	}
	_, err := AskBrowser(BrowserReq{Session: "S1", Tool: "tabs_context"})
	if err == nil || !strings.Contains(err.Error(), "daemon is not running") {
		t.Fatalf("a request with no daemon: %v", err)
	}
}

// A daemon from before it knew the question hangs up on it, as a hub does
// on a message it does not know: it is there, it did not answer, so the
// state is that and not a guess that no browser is connected. The marker is
// read from the file. The same for a hub that hangs up before reading.
func TestBrowserStatusFromADaemonThatDoesNotAnswer(t *testing.T) {
	for _, reads := range []bool{true, false} {
		dir, err := os.MkdirTemp("/tmp", "rb")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(dir) })
		sock := filepath.Join(dir, "b.sock")
		t.Setenv("REMINAL_BROWSER_SOCK", sock)
		ln, err := net.Listen("unix", sock)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ln.Close() })
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				if reads {
					_, _ = bufio.NewReader(c).ReadBytes('\n')
				}
				c.Close()
			}
		}()
		writeBrowserMarker("arc", time.Now())
		s := QueryBrowserStatus()
		if !s.Daemon || !s.NoAnswer || s.Connected || s.Family != "arc" || s.State(true) != BrowserNoAnswer {
			t.Fatalf("from a daemon that hangs up (after reading: %v): %+v", reads, s)
		}
		said := s.Sentence(true)
		if !strings.Contains(said, "an older one that cannot say whether the browser is connected") || strings.Contains(said, "not connected") {
			t.Fatalf("its sentence: %q", said)
		}
	}
}

// A hub that answers is never taken for one that does not.
func TestBrowserStatusFromAHubThatAnswersIsNotNoAnswer(t *testing.T) {
	startHub(t, func(int, string) bool { return true })
	if s := QueryBrowserStatus(); !s.Daemon || s.NoAnswer {
		t.Fatalf("from a hub that answers: %+v", s)
	}
}

// The marker is written by a rename over its path: a symlink already there
// is replaced, and the file it pointed to is left as it was.
func TestBrowserMarkerReplacesASymlinkAtItsPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("REMINAL_BROWSER_SOCK", filepath.Join(dir, "b.sock"))
	other := filepath.Join(dir, "other.txt")
	if err := os.WriteFile(other, []byte("not the marker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path, err := browserMarkerPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, path); err != nil {
		t.Fatal(err)
	}
	writeBrowserMarker("chrome", time.Now())
	st, err := os.Lstat(path)
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0o600 {
		t.Fatalf("the marker's path after a write: %v %v", st, err)
	}
	if raw, err := os.ReadFile(other); err != nil || string(raw) != "not the marker\n" {
		t.Fatalf("the file the symlink pointed to: %q %v", raw, err)
	}
	if m, ok := readBrowserMarker(); !ok || m.Family != "chrome" {
		t.Fatalf("the marker: %+v %v", m, ok)
	}
}

// The family is read from the browser's own program, not from a folder
// further up that happens to carry a browser's name.
func TestBrowserFamilyOfAProgram(t *testing.T) {
	for _, c := range [][2]string{
		{"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", "chrome"},
		{"/Applications/Google Chrome.app/Contents/Frameworks/X/Helpers/Google Chrome Helper.app/Contents/MacOS/Google Chrome Helper", "chrome"},
		{"/opt/google/chrome/chrome", "chrome"},
		{`C:\Users\edgar\AppData\Local\Google\Chrome\Application\chrome.exe`, "chrome"},
		{"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge", "edge"},
		{`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`, "edge"},
		{"/opt/microsoft/msedge/msedge", "edge"},
		{"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser", "brave"},
		{"/opt/brave.com/brave/brave", "brave"},
		{"/usr/lib/chromium/chrome", "chromium"},
		{"/snap/chromium/3000/usr/lib/chromium-browser/chrome", "chromium"},
		{"/Applications/Chromium.app/Contents/MacOS/Chromium", "chromium"},
		{"/Applications/Arc.app/Contents/MacOS/Arc", "arc"},
		{"chrome", "chrome"},
		{"", ""},
	} {
		if got := browserFamily(c[0]); got != c[1] {
			t.Errorf("%q: %q, want %q", c[0], got, c[1])
		}
	}
}
