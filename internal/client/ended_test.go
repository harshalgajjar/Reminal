// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"

	"reminal/internal/session"
)

// Each way a session ends has its own words, the first that holds of: its
// own error, put to sleep, a signal, its terminal gone, asked to quit, its
// shell ending by itself.
func TestEachWayASessionEndsHasItsWords(t *testing.T) {
	mk := func(set func(a *Agent)) *Agent {
		a := &Agent{sessionID: "ENDS1234", hostEscape: make(chan struct{})}
		set(a)
		return a
	}
	cases := []struct {
		name string
		a    *Agent
		err  error
		want string
	}{
		{"its shell ended", mk(func(a *Agent) {}), nil, "its shell ended"},
		{"Run's error", mk(func(a *Agent) { a.endSignal.Store("terminated") }), errors.New("start shell: no such file"), "error: start shell: no such file"},
		{"put to sleep", mk(func(a *Agent) { a.sleeping.Store(true); a.requestShutdown() }), nil, "asleep (put to sleep)"},
		{"a signal", mk(func(a *Agent) { a.endSignal.Store("terminated"); a.hungUp.Store(true) }), nil, "signal: terminated received"},
		{"its terminal gone", mk(func(a *Agent) { a.hungUp.Store(true) }), nil, "hung up: its terminal went away"},
		{"asked to quit", mk(func(a *Agent) { a.requestShutdown() }), nil, "quit: asked to stop"},
		{"quit after reminal stop", mk(func(a *Agent) { a.paused.Store(true); a.requestShutdown() }), nil, "quit: asked to stop, after sharing was stopped"},
	}
	for _, c := range cases {
		if got := c.a.endReason(c.err); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// However many ways out are taken, a session leaves one "ended" line, and
// its crash file goes when nothing died into it.
func TestASessionLeavesOneEndedLine(t *testing.T) {
	isolateHome(t)
	crash, err := session.CrashLogPath("ONCE1234")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(crash)
	a := &Agent{sessionID: "ONCE1234", version: "0.0.0-test", hostEscape: make(chan struct{})}
	a.keepCrashOutput()
	// This test's own process stays as it was once the test is done.
	t.Cleanup(func() { _ = debug.SetCrashOutput(nil, debug.CrashOptions{}) })
	if b, err := os.ReadFile(crash); err != nil || !strings.HasSuffix(string(b), ": started, v0.0.0-test\n") {
		t.Fatalf("crash file %q (%v): want this process's line", b, err)
	}
	a.requestShutdown()
	a.logEnded(nil)
	a.logEndedAs("forced out: a second signal (%s) while shutting down", "interrupt")
	a.logEnded(errors.New("later"))
	b, err := os.ReadFile(filepath.Join(dir, "events.log"))
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf(" ONCE1234 ended: quit: asked to stop (pid %d)\n", os.Getpid())
	if got := string(b); strings.Count(got, " ended: ") != 1 || !strings.HasSuffix(got, want) {
		t.Errorf("events %q: want the one line %q", got, want)
	}
	if _, err := os.Stat(crash); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a crash file nothing died into was kept (%v)", err)
	}
}

// A panic a session recovers from is said in its lines too, with where its
// stack is; a process that serves no session says nothing there.
func TestARecoveredPanicIsSaidInTheSessionsLines(t *testing.T) {
	isolateHome(t)
	crash, err := session.CrashLogPath("RCVR1234")
	if err != nil {
		t.Fatal(err)
	}
	events := filepath.Join(filepath.Dir(crash), "events.log")
	was := servedSession.Load()
	t.Cleanup(func() {
		if was != nil {
			servedSession.Store(was)
		} else {
			servedSession.Store("")
		}
	})
	servedSession.Store("")
	if err := guardPanic("a pump", func() error { panic("before any session") }); err == nil {
		t.Fatal("the panic was not turned into an error")
	}
	if b, _ := os.ReadFile(events); strings.Contains(string(b), "recovered from a panic") {
		t.Errorf("events %q: a process serving no session wrote a session's line", b)
	}
	servedSession.Store("RCVR1234")
	if err := guardPanic("a pump", func() error { panic("the pump's own panic") }); err == nil {
		t.Fatal("the panic was not turned into an error")
	}
	b, _ := os.ReadFile(events)
	want := fmt.Sprintf(" RCVR1234 recovered from a panic in a pump: the pump's own panic (pid %d); its stack is in panic.log\n", os.Getpid())
	if !strings.HasSuffix(string(b), want) {
		t.Errorf("events %q: want %q", b, want)
	}
}
