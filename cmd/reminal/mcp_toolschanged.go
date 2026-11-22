// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"encoding/json"
	"io"
	"strings"
	"sync"
	"time"
)

// The tools this server lists are not fixed for the life of the process: the
// browser tools appear once `reminal integrate` has set a browser up, and go
// again when that is undone. An agent that was already running kept the list
// it asked for at startup and never saw them. The protocol has a message for
// exactly this, notifications/tools/list_changed, and the server already
// declares it may send it. This file sends it when the list a client would
// get now is not the one it was last given.
//
// The server only acts when a request line arrives, and a change like this
// arrives with no request at all, so the check runs on a timer of its own.
// That makes two writers to stdout, which is why every message out goes
// through mcpOut.

// mcpOut is the one way out to the client. A reply and a notification can be
// written at the same moment from two goroutines; the lock keeps each JSON
// line whole. The encoder hands a whole line to the writer in one Write.
type mcpOut struct {
	mu  sync.Mutex
	enc *json.Encoder
}

func newMCPOut(w io.Writer) *mcpOut { return &mcpOut{enc: json.NewEncoder(w)} }

func (o *mcpOut) send(m map[string]any) {
	o.mu.Lock()
	defer o.mu.Unlock()
	_ = o.enc.Encode(m)
}

// toolsHere says whether the browser tools are listed. A variable so a test
// can turn it without a browser.
var toolsHere = browserToolsHere

// toolsWatchInterval is how often the list is compared with the one the
// client has. A change comes from a person running a command, so seconds are
// soon enough, and one look costs a few file stats.
var toolsWatchInterval = 5 * time.Second

// mcpToolsTicks gives the times at which the list is looked at, and a way to
// stop them. A variable so a test can say when.
var mcpToolsTicks = func() (<-chan time.Time, func()) {
	t := time.NewTicker(toolsWatchInterval)
	return t.C, t.Stop
}

// mcpToolsWatchDone, when set, is called once the watch has stopped. Only a
// test sets it.
var mcpToolsWatchDone func()

// toolsKey names a tool list: the names in it, in order. Two lists with the
// same key offer the same tools. What a tool says about itself is fixed when
// the binary is built, so the names are all that can differ while it runs.
func toolsKey(tools []map[string]any) string {
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		n, _ := t["name"].(string)
		names = append(names, n)
	}
	return strings.Join(names, "\n")
}

// toolsWatch remembers which list the client was last given, or last told
// about, and tells it when the list it would get now is a different one.
type toolsWatch struct {
	mu    sync.Mutex
	send  func(map[string]any)
	key   func() string // the list a client would get now
	ready bool          // the client is past initialize
	told  string        // the list the client has, or has been told to fetch
}

// clientReady marks the client as past initialize. Nothing is sent before
// that: a notification ahead of the handshake is not allowed, and a client
// that has not listed yet has nothing out of date. Changes count from here
// until the client lists.
func (w *toolsWatch) clientReady() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.ready {
		return
	}
	w.ready, w.told = true, w.key()
}

// listed records the list just handed to the client in answer to tools/list.
// A client that asks for the list is past initialize, whether or not it said
// so.
func (w *toolsWatch) listed(key string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ready, w.told = true, key
}

// check sends one notification when the list has changed since the client
// was given it or told of it. The lock is held across the look and the send
// so an answer to tools/list cannot land between the two and be announced as
// a change.
func (w *toolsWatch) check() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.ready {
		return
	}
	now := w.key()
	if now == w.told {
		return
	}
	// Told once per change: the client has been asked to fetch this list,
	// and asking again every few seconds would not make one that ignores
	// the message listen.
	w.told = now
	w.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/tools/list_changed"})
}

// run looks at each tick until stop is closed.
func (w *toolsWatch) run(stop <-chan struct{}, ticks <-chan time.Time) {
	for {
		select {
		case <-stop:
			return
		case <-ticks:
			// Both can be ready at once; stopping wins.
			select {
			case <-stop:
				return
			default:
			}
			w.check()
		}
	}
}
