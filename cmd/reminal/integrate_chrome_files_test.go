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
	"sync"
	"testing"
)

// answeringHub stands in for the daemon's hub: it answers the state
// question with what the test last gave it.
type answeringHub struct {
	mu     sync.Mutex
	answer string
}

func (h *answeringHub) say(answer string) {
	h.mu.Lock()
	h.answer = answer
	h.mu.Unlock()
}

func startAnsweringHub(t *testing.T, answer string) *answeringHub {
	t.Helper()
	h := &answeringHub{answer: answer}
	dir, err := os.MkdirTemp("/tmp", "rf")
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
			h.mu.Lock()
			answer := h.answer
			h.mu.Unlock()
			_, _ = c.Write([]byte(answer + "\n"))
			c.Close()
		}
	}()
	return h
}

// `reminal integrate --check` with the extension's folder gone, empty, or
// someone else's says the files are gone and that `reminal integrate` puts
// them back, whether or not a browser connected before, and never that a
// window is to be opened as the one thing to do. With either folder intact
// it says what it said before; a connected extension is said as connected
// with no folder at all.
func TestIntegrateCheckSaysTheExtensionsFilesAreGone(t *testing.T) {
	wideTerminal(t)
	home := t.TempDir()
	chrome := chromeBrowserTable()[0]
	if err := os.MkdirAll(filepath.Join(home, chrome.Dir), 0o755); err != nil {
		t.Fatal(err)
	}
	const (
		never     = `{"t":"state","connected":false}`
		was       = `{"t":"state","connected":false,"family":"chromium","last":"2026-11-22T10:00:00Z"}`
		connected = `{"t":"state","connected":true,"family":"chromium","last":"2026-11-22T10:00:00Z"}`

		saysGone  = "The files of reminal's browser extension are no longer on this computer: run `reminal integrate` to put them back, then open a browser window; if the extension still does not connect, reload it on the browser's extensions page (chrome://extensions)."
		saysNever = "is not connected, and has not connected before"
		saysWas   = "is not connected now, though Chromium was connected before: the browser is closed or has no window open"
	)
	hub := startAnsweringHub(t, never)
	runIntegrateIn(t, home, "-y")
	now, before := chromeExtensionDir(home), chromeExtensionOldDir(home)
	if !chromeExtensionHere(home) {
		t.Fatal("a setup did not write the extension's folder")
	}
	check := func(name, answer, want string) {
		t.Helper()
		hub.say(answer)
		out := runIntegrateIn(t, home, "--check")
		if !strings.Contains(out, want) {
			t.Errorf("%s: --check does not say %q:\n%s", name, want, out)
		}
		for _, other := range []string{saysGone, saysNever, saysWas} {
			if other != want && strings.Contains(out, other) {
				t.Errorf("%s: --check also says %q:\n%s", name, other, out)
			}
		}
	}
	empty := func(dir string) {
		t.Helper()
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
	}

	check("the folder intact, never connected", never, saysNever)
	check("the folder intact, connected before", was, saysWas)

	// The folder deleted by hand.
	empty(now)
	check("the folder deleted, connected before", was, saysGone)
	check("the folder deleted, never connected", never, saysGone)
	check("the folder deleted, connected", connected, "Chromium is connected: agents in your reminal sessions")

	// There and empty.
	if err := os.MkdirAll(now, 0o755); err != nil {
		t.Fatal(err)
	}
	check("the folder empty, connected before", was, saysGone)

	// Someone else's folder of that name.
	if err := os.WriteFile(filepath.Join(now, "notes.txt"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	check("someone else's folder, connected before", was, saysGone)
	check("someone else's folder, never connected", never, saysGone)

	// The folder of before intact, the one of now missing: the files are
	// here for the browser that loaded them from there.
	empty(now)
	if err := writeChromeExtension(before); err != nil {
		t.Fatal(err)
	}
	check("the folder of before intact, connected before", was, saysWas)
	check("the folder of before intact, never connected", never, saysNever)

	// And `reminal integrate` is what puts them back.
	empty(before)
	check("both gone", was, saysGone)
	runIntegrateIn(t, home, "-y")
	check("after integrate again", was, saysWas)
}
