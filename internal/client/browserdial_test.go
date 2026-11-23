// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build linux || darwin

package client

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The other tests of this package want a request with no daemon refused at
// once; the wait for a daemon that is starting again has its own test here.
func init() { browserDialWait = 0 }

// A request made while reminal's daemon is away for a moment (it is started
// again after an upgrade) waits for it and is carried, where it used to be
// refused at once with advice to set up what was already set up.
func TestARequestWaitsForADaemonThatIsStartingAgain(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "rb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("REMINAL_BROWSER_SOCK", filepath.Join(dir, "b.sock"))
	was := browserDialWait
	browserDialWait = 5 * time.Second
	t.Cleanup(func() { browserDialWait = was })

	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go func() {
		time.Sleep(400 * time.Millisecond) // the daemon is away this long
		h := newBrowserHub()
		h.allow = func(int, string) bool { return true }
		h.allowBridge = func(int) bool { return true }
		h.noWait = true
		serveBrowserOn(h, stop)
	}()

	began := time.Now()
	resp, err := AskBrowser(BrowserReq{Session: "S1", Tool: "tabs_context"})
	took := time.Since(began)
	if err != nil {
		t.Fatalf("the request did not reach the daemon once it was back (after %v): %v", took, err)
	}
	if took < 300*time.Millisecond || took > 4*time.Second {
		t.Errorf("it took %v: it should wait for the daemon, and no longer", took)
	}
	// The hub answered (it has no browser in this test, and says so): what
	// matters is that the answer is the hub's, not "the daemon is not running".
	if strings.Contains(resp.Error, "daemon is not running") || resp.Error == "" {
		t.Errorf("the answer is not the hub's own: %q", resp.Error)
	}

	// The control: with no wait, the same moment is refused as before.
	browserDialWait = 0
	t.Setenv("REMINAL_BROWSER_SOCK", filepath.Join(dir, "none.sock"))
	if _, err := AskBrowser(BrowserReq{Session: "S1", Tool: "tabs_context"}); err == nil || !strings.Contains(err.Error(), "daemon is not running") {
		t.Errorf("with no wait and no daemon: %v", err)
	}
}
