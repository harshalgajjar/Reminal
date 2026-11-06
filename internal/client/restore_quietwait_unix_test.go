// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !windows

package client

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"reminal/internal/crypto"
	"reminal/internal/pty"
)

// quietShell is a restored session whose shell is the script body (it
// prints its prompt, then echoes what is typed: cat on a terminal), its
// output pumped into the session as a real one's is.
func quietShell(t *testing.T, body string) *Agent {
	t.Helper()
	sh := filepath.Join(t.TempDir(), "sh")
	if err := os.WriteFile(sh, []byte("#!/bin/sh\n"+body+"\nexec cat\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	term, err := pty.Start(sh)
	if err != nil {
		t.Fatal(err)
	}
	key, err := crypto.NewSessionKey()
	if err != nil {
		t.Fatal(err)
	}
	box, err := crypto.NewBox(key)
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{box: box, buf: newScrollback(1 << 20), term: term, restoreRun: "echo resumed-42", screen: testScreen(t, 80, 24)}
	pumped := make(chan struct{})
	go func() { a.pumpPTY(); close(pumped) }()
	// Hung up first: a close alone does not end the read under way
	// (Session.Hangup), and the pump is this test's to end.
	t.Cleanup(func() { term.Hangup(); _ = term.Close(); <-pumped })
	return a
}

// shown is what the session has recorded, in order, as text.
func (a *Agent) shown(t *testing.T) string {
	t.Helper()
	a.buf.mu.Lock()
	entries := append([]scrollEntry(nil), a.buf.entries...)
	a.buf.mu.Unlock()
	var b strings.Builder
	for _, e := range entries {
		p, err := a.box.Decrypt(e.Data)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(p)
	}
	return b.String()
}

func waitShown(t *testing.T, a *Agent, s string, within time.Duration) time.Duration {
	t.Helper()
	start := time.Now()
	for time.Since(start) < within {
		if strings.Contains(a.shown(t), s) {
			return time.Since(start)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%q not shown within %v; shown: %q", s, within, a.shown(t))
	return 0
}

// The new shell's prompt is usually drawn while the restore waits for the
// session's first try at the relay, so it is already there when the resume
// looks for it: the resume is typed once the screen is quiet, not after the
// wait's 10 s deadline.
func TestARestoreTypesItsResumeSoonWhenThePromptIsAlreadyDrawn(t *testing.T) {
	a := quietShell(t, `printf 'ready$ '`)
	waitShown(t, a, "ready$ ", 5*time.Second)
	time.Sleep(500 * time.Millisecond) // drawn well before the restore looks
	a.signalRegistered()
	began := time.Now()
	a.restoreStart()
	if took := time.Since(began); took > 3*time.Second {
		t.Fatalf("the resume was typed %v after the restore began (the prompt was already drawn)", took.Round(10*time.Millisecond))
	}
	waitShown(t, a, "echo resumed-42", 2*time.Second)
}

// Control: a shell still starting (its profile running, nothing printed
// yet) is not typed into: the resume waits for its prompt.
func TestARestoreWaitsForTheShellsPromptBeforeTypingItsResume(t *testing.T) {
	a := quietShell(t, `sleep 1.5; printf 'ready$ '`)
	a.signalRegistered()
	a.restoreStart()
	waitShown(t, a, "echo resumed-42", 2*time.Second)
	out := a.shown(t)
	if p, r := strings.Index(out, "ready$ "), strings.Index(out, "echo resumed-42"); p < 0 || r < p {
		t.Fatalf("the resume was typed before the shell's prompt: %q", out)
	}
}

// A shell that prints a line, then goes on starting (an nvm, conda or pyenv
// set-up after "Last login: …") before it draws its prompt: quiet for a
// moment, but no prompt yet. The resume waits for the prompt.
func TestARestoreWaitsForThePromptPastALineAndAPause(t *testing.T) {
	a := quietShell(t, `printf 'Last login: Tue Oct 27 on ttys001\n'; sleep 1.5; printf 'ready$ '`)
	a.signalRegistered()
	a.restoreStart()
	waitShown(t, a, "echo resumed-42", 2*time.Second)
	out := a.shown(t)
	if p, r := strings.Index(out, "ready$ "), strings.Index(out, "echo resumed-42"); p < 0 || r < p {
		t.Fatalf("the resume was typed before the shell's prompt: %q", out)
	}
}

// What a person types while the resume waits for the prompt (a viewer who
// opened the session at once) is kept, and typed after the resume, never
// into the middle of it.
func TestWhatIsTypedWhileARestoreWaitsIsTypedAfterItsResume(t *testing.T) {
	a := quietShell(t, `sleep 1.5; printf 'ready$ '`)
	a.holdTyped() // as Run does for a restored session
	a.signalRegistered()
	go func() {
		time.Sleep(200 * time.Millisecond)
		_, _ = a.typeIn([]byte("zq-typed")) // a viewer's keys, as serveConn types them
	}()
	a.restoreStart()
	waitShown(t, a, "zq-typed", 2*time.Second)
	out := a.shown(t)
	// First on the shell's line, the resume; then what was typed (cat then
	// echoes the resume's line after them, as a shell would run it).
	r, h := strings.Index(out, "echo resumed-42"), strings.Index(out, "zq-typed")
	if r < 0 || h < r {
		t.Fatalf("what was typed meanwhile was not typed after the resume: %q", out)
	}
}

// Held no longer than typedHoldMax: a restored session whose relay is slow
// to answer its first try (the resume waits on it) does not keep a person's
// keys from the shell. Shortened here; it is 12 s.
func TestKeysHeldWhileTheRelayIsSlowAreTypedWithinTheHoldsBound(t *testing.T) {
	if typedHoldMax != 12*time.Second {
		t.Fatalf("typedHoldMax is %v", typedHoldMax)
	}
	old := typedHoldMax
	typedHoldMax = time.Second
	defer func() { typedHoldMax = old }()
	a := quietShell(t, `printf 'ready$ '`)
	a.holdTyped()
	go a.restoreStart() // its relay never answers here: the resume waits
	time.Sleep(200 * time.Millisecond)
	_, _ = a.typeIn([]byte("zq-typed"))
	took := waitShown(t, a, "zq-typed", 3*time.Second)
	if took > 2*time.Second {
		t.Fatalf("a held key reached the shell %v after it was typed", took)
	}
	if strings.Contains(a.shown(t), "echo resumed-42") {
		t.Fatal("the resume was typed without its relay's answer")
	}
	a.signalRegistered() // let it end
}

// An agent's keys (the keys control: MCP send_keys) sent while a restored
// session waits to type its resume are typed after it, not into it.
func TestAnAgentsKeysWhileARestoreWaitsAreTypedAfterItsResume(t *testing.T) {
	a := quietShell(t, `printf 'ready$ '`)
	a.holdTyped()
	a.signalRegistered()
	sent := make(chan error, 1)
	go func() {
		time.Sleep(200 * time.Millisecond)
		sent <- a.handleKeysControl(base64.StdEncoding.EncodeToString([]byte("zq-agent")))
	}()
	a.restoreStart()
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	waitShown(t, a, "zq-agent", 2*time.Second)
	out := a.shown(t)
	if r, k := strings.Index(out, "echo resumed-42"), strings.Index(out, "zq-agent"); r < 0 || k < r {
		t.Fatalf("the agent's keys were not typed after the resume: %q", out)
	}
}

// The keys verb, through the control socket as MCP send_keys and the CLI
// send it, is given long enough for a restored session's hold: its caller
// is not told it timed out while its keys are still to be typed (and so
// types them again).
func TestTheKeysVerbWaitsOutARestoresHoldThroughTheControlSocket(t *testing.T) {
	home, err := os.MkdirTemp("", "h") // short: a socket's path is
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	t.Setenv("HOME", home)
	old := typedHoldMax
	typedHoldMax = 4 * time.Second // past the 3 s the other verbs are given
	defer func() { typedHoldMax = old }()
	a := quietShell(t, `printf 'ready$ '`)
	stop := a.listenControl()
	defer stop()
	a.holdTyped()
	go a.restoreStart() // its relay never answers here: the hold lasts
	began := time.Now()
	if err := InjectAgentKeys(os.Getpid(), []byte("zq-agent")); err != nil {
		t.Fatalf("the keys verb failed after %v: %v", time.Since(began).Round(100*time.Millisecond), err)
	}
	waitShown(t, a, "zq-agent", 2*time.Second)
	if n := strings.Count(a.shown(t), "zq-agent"); n != 1 {
		t.Fatalf("its keys shown %d times", n)
	}
	a.signalRegistered() // let it end
}
