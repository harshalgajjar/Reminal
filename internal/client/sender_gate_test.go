// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/reminal/reminal/internal/protocol"
)

// senderHarness runs runSender against a real websocket pair and exposes the
// TypeData messages the "relay" receives. The agent is minimal on purpose:
// no screen (skips the snapshot branch), no crypto (runSender relays whatever
// bytes the scrollback holds without touching them).
type senderHarness struct {
	a         *Agent
	cursorCh  chan uint64
	data      chan string
	done      chan error
	srvConn   chan *websocket.Conn // relay side of the pair, for pushing messages at runReader
	agentConn *websocket.Conn
}

func startSenderHarness(t *testing.T, local bool) *senderHarness {
	t.Helper()
	h := &senderHarness{
		a:        &Agent{buf: newScrollback(scrollbackBytes)},
		cursorCh: make(chan uint64, 4),
		data:     make(chan string, 64),
		srvConn:  make(chan *websocket.Conn, 1),
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up := websocket.Upgrader{}
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		h.srvConn <- conn
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var msg protocol.Message
			if json.Unmarshal(raw, &msg) == nil && msg.Type == protocol.TypeData {
				h.data <- msg.Data
			}
		}
	}))
	t.Cleanup(ts.Close)

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial test relay: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	h.done = make(chan error, 1)
	go func() { h.done <- h.a.runSender(conn, h.cursorCh, stop, local) }()
	h.agentConn = conn
	return h
}

func (h *senderHarness) setViewers(n int) {
	h.a.viewerSizeMu.Lock()
	h.a.viewerCount = n
	h.a.viewerSizeMu.Unlock()
}

// expect waits for the next TypeData payloads in order.
func (h *senderHarness) expect(t *testing.T, want ...string) {
	t.Helper()
	for _, w := range want {
		select {
		case got := <-h.data:
			if got != w {
				t.Fatalf("relay received %q, want %q", got, w)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("relay never received %q", w)
		}
	}
}

// expectSilence asserts nothing reaches the relay for a beat.
func (h *senderHarness) expectSilence(t *testing.T) {
	t.Helper()
	select {
	case got := <-h.data:
		t.Fatalf("relay received %q while no viewer was attached", got)
	case <-time.After(150 * time.Millisecond):
	}
}

// TestSenderParksWithoutViewers pins the Durable-Object cost fix: terminal
// output must not cross the relay while no viewer is attached — every message
// the DO receives is a billable request, and it would forward them to no one.
func TestSenderParksWithoutViewers(t *testing.T) {
	h := startSenderHarness(t, false)

	// A resume cursor arrives but the viewer count is (still) zero — the
	// stale-cursor-after-disconnect case. The gate must stay shut.
	h.a.buf.Append("unwatched")
	h.cursorCh <- 0
	h.expectSilence(t)

	// A viewer attaches (TypeConnected sets the count, TypeResume the cursor):
	// everything buffered while parked replays from its cursor.
	h.setViewers(1)
	h.cursorCh <- 0
	h.expect(t, "unwatched")

	// Live streaming while watched.
	h.a.buf.Append("watched")
	h.expect(t, "watched")

	// Last viewer leaves; the next chunk must re-park the sender, not ship.
	h.setViewers(0)
	h.a.buf.Append("after-close")
	h.expectSilence(t)

	// It comes back: cursor picks up exactly where the viewer left off,
	// delivering what accumulated during the pause.
	h.setViewers(1)
	h.cursorCh <- h.a.buf.NextSeq() - 1
	h.expect(t, "after-close")

	select {
	case err := <-h.done:
		t.Fatalf("runSender exited early: %v", err)
	default:
	}
}

// TestResumeAloneReopensTheGate wires the REAL runReader to runSender and
// replays the agent-reconnect sequence: the viewer count starts at zero (each
// fresh connection resets it — the relay only pushes counts on viewer
// connect/disconnect, so a count carried over could be a ghost), and no
// TypeConnected ever arrives because the surviving viewer never re-auths. Its
// TypeResume must both reopen the gate and deliver the cursor — in that order,
// or the replay request is dropped while the gate is still shut.
func TestResumeAloneReopensTheGate(t *testing.T) {
	h := startSenderHarness(t, false)
	go func() { _ = h.a.runReader(h.agentConn, h.cursorCh) }()

	relay := <-h.srvConn
	seq := h.a.buf.Append("blipped-backlog")

	raw, err := json.Marshal(protocol.Message{Type: protocol.TypeResume, FromSeq: seq - 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := relay.WriteMessage(websocket.TextMessage, raw); err != nil {
		t.Fatalf("send resume: %v", err)
	}

	h.expect(t, "blipped-backlog")
	if n := h.a.currentViewerCount(); n != 1 {
		t.Errorf("viewer count after resume = %d, want 1", n)
	}

	// And live output keeps flowing on the reopened gate.
	h.a.buf.Append("live-again")
	h.expect(t, "live-again")
}

// TestLocalSenderStreamsWithoutViewers pins the local-attach exception: a
// same-machine attach (local=true) is its own audience with no billable relay
// hop, so its sender streams even though the relay viewer count is zero — the
// exact condition that parks the relay path in TestSenderParksWithoutViewers.
func TestLocalSenderStreamsWithoutViewers(t *testing.T) {
	h := startSenderHarness(t, true)

	// Viewer count is zero (no relay viewers). A resume from the local attach
	// opens the stream anyway, and buffered output flows.
	h.a.buf.Append("offline-hello")
	h.cursorCh <- 0
	h.expect(t, "offline-hello")

	// Live output keeps flowing with the count still at zero.
	h.a.buf.Append("still-here")
	h.expect(t, "still-here")
}
