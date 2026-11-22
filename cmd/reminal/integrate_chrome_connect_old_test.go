// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build linux || darwin

package main

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"reminal/internal/client"
)

// oldHub stands in for the hub of a daemon from before it could be asked the
// browser's state: it takes a connection, reads the question, and hangs up
// without a word, as a hub does on a message it does not know.
func oldHub(t *testing.T) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ro")
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
			_, _ = bufio.NewReader(c).ReadBytes('\n')
			c.Close()
		}
	}()
}

// With a daemon from before it could be asked, the offer to open the page
// is still made, and then nothing is waited for: one line says the reminal in
// the background is an older one and how to bring the new one, with no guess
// at why the extension is not connected.
func TestBrowserConnectDoesNotWaitOnADaemonThatCannotSay(t *testing.T) {
	oldHub(t)
	r := newConnectRig(t, "darwin", hubNever)
	r.d.status = client.QueryBrowserStatus
	bc := newBrowserConnectWith(r.d, "/home/u", chromeOnly, false, false)
	if bc.already() {
		t.Fatal("counted as already connected")
	}
	r.typeLine("")
	if took := r.finish(bc); took > 2*time.Second {
		t.Errorf("waited %v", took)
	}
	out := r.out.String()
	if !strings.Contains(out, "Open the extensions page in Google Chrome now? [Y/n] ") || len(r.commands()) != 2 {
		t.Errorf("the offer (ran %v):\n%s", r.commands(), out)
	}
	if !strings.HasSuffix(out, "\n"+oldDaemonLine) {
		t.Errorf("did not end on the line for an older daemon:\n%s", out)
	}
	for _, not := range []string{"Waiting", "Not connected yet", "most likely", "already connected", "daemon is not running", "restart", "daemon --install"} {
		if strings.Contains(out, not) {
			t.Errorf("said %q:\n%s", not, out)
		}
	}
}

// A hub that stops answering in the middle of the wait ends it the same way.
func TestBrowserConnectWaitEndsWhenTheDaemonCannotSay(t *testing.T) {
	r := newConnectRig(t, "linux", hubNever)
	bc := newBrowserConnectWith(r.d, "/home/u", chromeOnly, false, false)
	r.typeLine("n")
	go func() {
		time.Sleep(40 * time.Millisecond)
		r.set(hubOld)
	}()
	if took := r.finish(bc); took > 4*time.Second {
		t.Errorf("took %v", took)
	}
	if out := r.out.String(); !strings.HasSuffix(out, oldDaemonLine) || strings.Contains(out, "Not connected yet") {
		t.Errorf("after the hub stopped answering:\n%s", out)
	}
}

// Through `reminal integrate` itself with such a daemon: the steps are
// given (the extension is not taken for connected), then the question, then
// the line, at once. --check says the state in its sentence.
func TestIntegrateWithADaemonThatCannotSay(t *testing.T) {
	home := t.TempDir()
	chrome := chromeBrowserTable()[0]
	if err := os.MkdirAll(filepath.Join(home, chrome.Dir), 0o755); err != nil {
		t.Fatal(err)
	}
	oldHub(t)

	// The "y" answers integrate's own question, and the terminal is then at
	// its end; as over SSH, no page is offered, so the run goes on to the
	// hub's state (the offer with such a daemon is the test above).
	t.Setenv("SSH_CONNECTION", "10.0.0.1 1 10.0.0.2 22")
	ran := atTerminal(t, hubNever, "y\n")
	browserStatusNow = client.QueryBrowserStatus
	start := time.Now()
	out := runIntegrateIn(t, home)
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("waited %v", took)
	}
	steps := strings.Index(out, "Load unpacked")
	if steps < 0 || strings.Index(out, "Not waiting for the extension") < steps || len(*ran) != 0 {
		t.Errorf("the steps, then the line (ran %v):\n%s", *ran, out)
	}
	if !strings.HasSuffix(out, "\n"+oldDaemonLine) {
		t.Errorf("did not end on the line for an older daemon:\n%s", out)
	}
	for _, not := range []string{"Waiting", "Not connected yet", "most likely", "already connected"} {
		if strings.Contains(out, not) {
			t.Errorf("said %q:\n%s", not, out)
		}
	}

	check := runIntegrateIn(t, home, "--check")
	want := "The reminal running in the background on this computer is an older one that cannot say whether the browser is connected: it is replaced by the new one within a minute of an upgrade, then run this again."
	if !strings.Contains(check, want) || strings.Contains(check, "extension is not connected") {
		t.Errorf("--check with an older daemon:\n%s", check)
	}
}
