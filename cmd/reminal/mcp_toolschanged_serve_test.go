// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !windows

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The whole server, a client on pipes: the browser is set up, then taken
// away, while the agent runs. The client is told once each time, never before
// it finished initialize, and the watch ends when the client closes stdin.
func TestServerTellsARunningClientItsToolsChanged(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(bootVersionEnv, "")
	t.Setenv(reloadedEnv, "")
	var here atomic.Bool
	oldHere, oldTicks := toolsHere, mcpToolsTicks
	ticks := make(chan time.Time)
	watchDone := make(chan struct{})
	toolsHere = here.Load
	mcpToolsTicks = func() (<-chan time.Time, func()) { return ticks, func() {} }
	mcpToolsWatchDone = func() { close(watchDone) }
	t.Cleanup(func() {
		toolsHere, mcpToolsTicks, mcpToolsWatchDone = oldHere, oldTicks, nil
		mcpIdle = maybeReexec
		toolsListServed.Store(false)
		cacheReuseWarned.Store(false)
	})

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan struct{})
	go func() { _ = serveMCP(inR, outW); _ = outW.Close(); close(served) }()

	lines := make(chan map[string]any, 16)
	go func() {
		sc := bufio.NewScanner(outR)
		sc.Buffer(make([]byte, 0, 1<<20), 8<<20)
		for sc.Scan() {
			var m map[string]any
			if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
				m = map[string]any{"broken": sc.Text()}
			}
			lines <- m
		}
		close(lines)
	}()
	next := func() map[string]any {
		t.Helper()
		select {
		case m, ok := <-lines:
			if !ok {
				t.Fatal("the server closed its output")
			}
			if b, bad := m["broken"]; bad {
				t.Fatalf("not a JSON line: %v", b)
			}
			return m
		case <-time.After(5 * time.Second):
			t.Fatal("nothing from the server")
		}
		return nil
	}
	id := 0
	ask := func(method string) map[string]any {
		t.Helper()
		id++
		fmt.Fprintf(inW, `{"jsonrpc":"2.0","id":%d,"method":%q}`+"\n", id, method)
		m := next()
		if got, _ := m["id"].(float64); int(got) != id {
			t.Fatalf("asked %s (id %d), the next line was %v", method, id, m)
		}
		return m
	}
	// look has the watch look at the list, and returns once it has: a tick
	// is taken only when the look before it is over.
	look := func() {
		t.Helper()
		for i := 0; i < 2; i++ {
			select {
			case ticks <- time.Time{}:
			case <-time.After(5 * time.Second):
				t.Fatal("the watch is not taking ticks")
			}
		}
	}
	// quiet: the next thing the server writes is the answer to a ping, so
	// nothing was sent before it.
	quiet := func(when string) {
		t.Helper()
		id++
		fmt.Fprintf(inW, `{"jsonrpc":"2.0","id":%d,"method":"ping"}`+"\n", id)
		if m := next(); m["method"] != nil {
			t.Fatalf("%s: the server sent %v", when, m["method"])
		}
	}
	told := func(when string) {
		t.Helper()
		if m := next(); m["method"] != "notifications/tools/list_changed" {
			t.Fatalf("%s: want the tools changed notice, got %v", when, m)
		}
	}
	hasBrowser := func(m map[string]any) bool {
		raw, _ := json.Marshal(m["result"])
		return strings.Contains(string(raw), `"browser_navigate"`)
	}

	// Before initialize: the list changes and nothing is said.
	here.Store(true)
	look()
	quiet("before initialize")
	here.Store(false)

	hello := ask("initialize")
	caps, _ := json.Marshal(hello["result"])
	if !strings.Contains(string(caps), `"listChanged":true`) {
		t.Fatalf("initialize does not declare listChanged: %s", caps)
	}
	fmt.Fprintln(inW, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	quiet("after initialize")
	look()
	quiet("no change since initialize")

	if hasBrowser(ask("tools/list")) {
		t.Fatal("browser tools listed with no browser set up")
	}
	look()
	quiet("no change since tools/list")

	// `reminal integrate` sets a browser up while the agent runs.
	here.Store(true)
	look()
	told("browser set up")
	look()
	look()
	quiet("told once already")

	if !hasBrowser(ask("tools/list")) {
		t.Fatal("the fresh list has no browser tools")
	}
	look()
	quiet("the client has the new list")

	// And it is taken away again.
	here.Store(false)
	look()
	told("browser taken away")
	look()
	quiet("told once already")

	_ = inW.Close()
	for _, c := range []struct {
		ch   chan struct{}
		what string
	}{{served, "the server did not stop"}, {watchDone, "the watch did not stop"}} {
		select {
		case <-c.ch:
		case <-time.After(5 * time.Second):
			t.Fatalf("stdin closed: %s", c.what)
		}
	}
}

// A server that has just swapped onto a new binary announces it once, as
// before, and is then ready to announce later changes without waiting for an
// initialize the client will not repeat.
func TestReloadedServerStillTellsLaterChanges(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(bootVersionEnv, "")
	t.Setenv(reloadedEnv, "1")
	var here atomic.Bool
	oldHere, oldTicks := toolsHere, mcpToolsTicks
	ticks := make(chan time.Time)
	toolsHere = here.Load
	mcpToolsTicks = func() (<-chan time.Time, func()) { return ticks, func() {} }
	t.Cleanup(func() {
		toolsHere, mcpToolsTicks = oldHere, oldTicks
		mcpIdle = maybeReexec
	})
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan struct{})
	go func() { _ = serveMCP(inR, outW); _ = outW.Close(); close(served) }()
	lines := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(outR)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	next := func() string {
		t.Helper()
		select {
		case l := <-lines:
			return l
		case <-time.After(5 * time.Second):
			t.Fatal("nothing from the server")
		}
		return ""
	}
	look := func() {
		t.Helper()
		for i := 0; i < 2; i++ {
			select {
			case ticks <- time.Time{}:
			case <-time.After(5 * time.Second):
				t.Fatal("the watch is not taking ticks")
			}
		}
	}
	const notice = `"notifications/tools/list_changed"`
	if l := next(); !strings.Contains(l, notice) {
		t.Fatalf("a reloaded server opens with the notice, got %s", l)
	}
	look()
	fmt.Fprintln(inW, `{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	if l := next(); strings.Contains(l, notice) {
		t.Fatal("the reload was announced twice")
	}
	here.Store(true)
	look()
	if l := next(); !strings.Contains(l, notice) {
		t.Fatalf("a change after a reload was not announced, got %s", l)
	}
	_ = inW.Close()
	select {
	case <-served:
	case <-time.After(5 * time.Second):
		t.Fatal("the server did not stop")
	}
}
