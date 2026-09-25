// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !windows

package main

import (
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// A client that never calls a tool still gets the new build: the reader's
// wait lapses, the server sees it holds nothing, and the idle hook runs. A
// half-written request is never counted as idle, and arrives whole.
func TestAnIdleReaderSwapsAndAHalfLineIsNotIdle(t *testing.T) {
	old := binaryWatchInterval
	binaryWatchInterval = 30 * time.Millisecond
	defer func() { binaryWatchInterval = old; mcpIdle = maybeReexec }()

	var idle atomic.Int32
	mcpIdle = func() { idle.Add(1) }

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
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
	time.Sleep(20 * time.Millisecond)
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
