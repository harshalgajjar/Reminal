// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !windows

package main

import (
	"os"
	"runtime"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// A client that never calls a tool still gets the new build: the reader's
// wait lapses, the server sees it holds nothing, and the idle hook runs. A
// half-written request is never counted as idle, and arrives whole.
func TestAnIdleReaderSwapsAndAHalfLineIsNotIdle(t *testing.T) {
	old := mcpIdleWait
	mcpIdleWait = 30 * time.Millisecond
	defer func() { mcpIdleWait = old; mcpIdle = maybeReexec }()

	var idle atomic.Int32
	mcpIdle = func() { idle.Add(1) }

	// A client's stdin is a blocking pipe that Go does not poll — the shape
	// mcpPollable exists for. os.Pipe's ends are already polled, so the pipe
	// is made raw, as a descriptor arrives over exec.
	var fds [2]int
	if err := syscall.Pipe(fds[:]); err != nil {
		t.Fatal(err)
	}
	r, w := os.NewFile(uintptr(fds[0]), "stdin"), os.NewFile(uintptr(fds[1]), "client")
	// The reader wraps the same descriptor in a second *os.File; if this one
	// is collected first its finalizer closes the descriptor under it, and a
	// later pipe reusing the number inherits the poller's stale registration.
	defer runtime.KeepAlive(r)
	lines := make(chan string, 8)
	done := make(chan struct{})
	go func() { mcpReadLines(r, func(l string) { lines <- l }); close(done) }()

	time.Sleep(120 * time.Millisecond)
	if idle.Load() == 0 {
		t.Fatal("a quiet client never let the server go idle")
	}

	// A request split across the deadline: not idle while it is held, whole
	// when it is delivered.
	_, _ = w.WriteString(`{"id":1,`)
	time.Sleep(70 * time.Millisecond) // past any wait that lapsed before the write landed
	before := idle.Load()
	time.Sleep(100 * time.Millisecond)
	if idle.Load() != before {
		t.Fatal("went idle while holding half a request")
	}
	_, _ = w.WriteString("\"method\":\"x\"}\n")
	select {
	case l := <-lines:
		if l != `{"id":1,"method":"x"}` {
			t.Fatalf("line = %q", l)
		}
	case <-time.After(time.Second):
		t.Fatal("the completed request never arrived")
	}
	// Answered, nothing buffered: idle right away, not after a deadline.
	time.Sleep(10 * time.Millisecond)
	if idle.Load() <= before {
		t.Fatal("did not go idle after answering")
	}

	_ = w.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reader did not stop when the client closed its end")
	}
}

// A re-exec'd image inherits a stdin Go already polls. It must keep reading
// after its first request, and keep going idle between requests.
func TestAnAlreadyPolledStdinKeepsServing(t *testing.T) {
	old := mcpIdleWait
	mcpIdleWait = 30 * time.Millisecond
	defer func() { mcpIdleWait = old; mcpIdle = maybeReexec }()
	var idle atomic.Int32
	mcpIdle = func() { idle.Add(1) }

	r, w, err := os.Pipe() // both ends already registered with the poller
	if err != nil {
		t.Fatal(err)
	}
	lines := make(chan string, 8)
	done := make(chan struct{})
	go func() { mcpReadLines(r, func(l string) { lines <- l }); close(done) }()

	for i, want := range []string{"one", "two", "three"} {
		time.Sleep(100 * time.Millisecond) // well past the deadline between requests
		_, _ = w.WriteString(want + "\n")
		select {
		case got := <-lines:
			if got != want {
				t.Fatalf("line %d = %q, want %q", i, got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("request %d (%q) was never read", i, want)
		}
	}
	n := idle.Load()
	time.Sleep(120 * time.Millisecond)
	if idle.Load() <= n {
		t.Fatal("stopped going idle after serving requests")
	}
	_ = w.Close()
	<-done
}

// A client that closes its end after a last request without a newline still
// gets that request handled — the old scanner did as much.
func TestALastLineWithoutNewlineIsStillARequest(t *testing.T) {
	old := mcpIdleWait
	mcpIdleWait = 30 * time.Millisecond
	defer func() { mcpIdleWait = old; mcpIdle = maybeReexec }()
	mcpIdle = func() {}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	lines := make(chan string, 2)
	done := make(chan struct{})
	go func() { mcpReadLines(r, func(l string) { lines <- l }); close(done) }()
	_, _ = w.WriteString("last")
	_ = w.Close()
	select {
	case l := <-lines:
		if l != "last" {
			t.Fatalf("got %q", l)
		}
	case <-time.After(time.Second):
		t.Fatal("the unterminated last line was dropped")
	}
	<-done
}
