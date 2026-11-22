// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// watchRig is a toolsWatch whose list is whatever the test says and whose
// messages are kept.
type watchRig struct {
	mu   sync.Mutex
	key  string
	sent []map[string]any
	w    *toolsWatch
}

func newWatchRig(key string) *watchRig {
	r := &watchRig{key: key}
	r.w = &toolsWatch{
		send: func(m map[string]any) { r.mu.Lock(); r.sent = append(r.sent, m); r.mu.Unlock() },
		key:  func() string { r.mu.Lock(); defer r.mu.Unlock(); return r.key },
	}
	return r
}

func (r *watchRig) set(key string) { r.mu.Lock(); r.key = key; r.mu.Unlock() }

func (r *watchRig) count() int { r.mu.Lock(); defer r.mu.Unlock(); return len(r.sent) }

// The client is told once for each change to the list it was given: never
// before it finished initialize, never twice for one change, and again when
// the list changes a second time.
func TestToolsChangedIsToldOncePerChange(t *testing.T) {
	r := newWatchRig("plain")

	// Before initialize nothing goes out, whatever the list does.
	r.set("plain+browser")
	r.w.check()
	r.set("plain")
	r.w.check()
	if n := r.count(); n != 0 {
		t.Fatalf("%d sent before the client finished initialize", n)
	}

	r.w.clientReady()
	r.w.check()
	if n := r.count(); n != 0 {
		t.Fatalf("%d sent with no change since initialize", n)
	}

	r.w.listed("plain")
	r.w.check()
	if n := r.count(); n != 0 {
		t.Fatalf("%d sent when the list is the one just served", n)
	}

	// The browser is set up while the agent runs.
	r.set("plain+browser")
	r.w.check()
	if n := r.count(); n != 1 {
		t.Fatalf("a change after tools/list sent %d, want 1", n)
	}
	if m := r.sent[0]; m["method"] != "notifications/tools/list_changed" || m["jsonrpc"] != "2.0" {
		t.Fatalf("sent %v", m)
	}
	if _, has := r.sent[0]["id"]; has {
		t.Fatal("a notification carries no id")
	}
	for i := 0; i < 5; i++ {
		r.w.check()
	}
	if n := r.count(); n != 1 {
		t.Fatalf("one change was told %d times", n)
	}

	// And taken away again: a second change, told once more.
	r.set("plain")
	r.w.check()
	r.w.check()
	if n := r.count(); n != 2 {
		t.Fatalf("after a second change %d sent, want 2", n)
	}

	// A client that fetched the list has it: nothing more to say.
	r.set("plain+browser")
	r.w.listed("plain+browser")
	r.w.check()
	if n := r.count(); n != 2 {
		t.Fatalf("told about a list the client had just fetched (%d sent)", n)
	}
}

// A client that lists without ever saying it finished initialize is still
// past it, and is told of a later change.
func TestToolsChangedAfterAListWithNoInitializedNotice(t *testing.T) {
	r := newWatchRig("a")
	r.w.listed("a")
	r.set("b")
	r.w.check()
	if n := r.count(); n != 1 {
		t.Fatalf("sent %d, want 1", n)
	}
}

// A list handed out just before a change is the old one, even if the change
// was already announced: the client is told again.
func TestToolsChangedListServedIsWhatCounts(t *testing.T) {
	r := newWatchRig("a")
	r.w.clientReady()
	r.set("b")
	r.w.check() // told about b
	r.w.listed("a")
	r.w.check()
	if n := r.count(); n != 2 {
		t.Fatalf("sent %d, want 2: the client holds the old list", n)
	}
}

// The watch looks on each tick and stops when told to.
func TestToolsWatchRunsOnTicksAndStops(t *testing.T) {
	r := newWatchRig("a")
	r.w.clientReady()
	stop := make(chan struct{})
	ticks := make(chan time.Time)
	done := make(chan struct{})
	go func() { r.w.run(stop, ticks); close(done) }()
	tick := func() {
		t.Helper()
		select {
		case ticks <- time.Time{}:
		case <-time.After(2 * time.Second):
			t.Fatal("the watch is not taking ticks")
		}
	}
	// A tick is taken only once the look before it is over, so after two
	// the first has been acted on.
	tick()
	tick()
	if n := r.count(); n != 0 {
		t.Fatalf("%d sent with no change", n)
	}
	r.set("b")
	tick()
	tick()
	if n := r.count(); n != 1 {
		t.Fatalf("%d sent after one change, want 1", n)
	}
	close(stop)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the watch did not stop")
	}
}

// chopWriter writes each call in two parts with a pause between, as a pipe
// may. Two writers that are not kept apart end up inside each other's lines.
type chopWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *chopWriter) Write(p []byte) (int, error) {
	half := len(p) / 2
	c.mu.Lock()
	c.buf.Write(p[:half])
	c.mu.Unlock()
	runtime.Gosched()
	c.mu.Lock()
	c.buf.Write(p[half:])
	c.mu.Unlock()
	return len(p), nil
}

// Replies and notifications written at the same time come out as whole JSON
// lines, none inside another.
func TestRepliesAndNotificationsAreWholeLines(t *testing.T) {
	cw := &chopWriter{}
	out := newMCPOut(cw)
	const writers, each = 8, 200
	var wg sync.WaitGroup
	for g := 0; g < writers; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				if g%2 == 0 {
					out.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/tools/list_changed"})
					continue
				}
				out.send(map[string]any{"jsonrpc": "2.0", "id": g*1000 + i,
					"result": map[string]any{"text": strings.Repeat(fmt.Sprint(g), 50+i)}})
			}
		}(g)
	}
	wg.Wait()
	lines := strings.Split(strings.TrimRight(cw.buf.String(), "\n"), "\n")
	if len(lines) != writers*each {
		t.Fatalf("%d lines, want %d", len(lines), writers*each)
	}
	notes := 0
	for i, l := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("line %d is not one JSON message: %v\n%.120s", i, err, l)
		}
		if m["method"] == "notifications/tools/list_changed" {
			notes++
		}
	}
	if notes != writers/2*each {
		t.Fatalf("%d notifications, want %d", notes, writers/2*each)
	}
}

// The key of a list is its tool names: the browser tools arriving or going
// changes it, and nothing else does while the server runs.
func TestToolsKeyFollowsTheBrowserTools(t *testing.T) {
	old := toolsHere
	t.Cleanup(func() { toolsHere = old })
	toolsHere = func() bool { return false }
	without := toolsKey(mcpToolList())
	if without != toolsKey(mcpToolList()) {
		t.Fatal("the same list has two keys")
	}
	toolsHere = func() bool { return true }
	with := toolsKey(mcpToolList())
	if with == without {
		t.Fatal("the browser tools arriving did not change the key")
	}
	if !strings.Contains(with, "browser_navigate") || strings.Contains(without, "browser_") {
		t.Fatal("the key does not name the tools listed")
	}
}
