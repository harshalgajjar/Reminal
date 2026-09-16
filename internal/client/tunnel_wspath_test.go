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
)

// TestTunnelWSOpenPreservesEncodedPath guards 9f83992: a proxied WebSocket whose
// URL contains an encoded slash (%2F) — common in file browsers and APIs that put
// an id or path into a segment — must reach the backend with the %2F intact.
// dialTunnelWSBackend builds the dial URL with RawPath: ref.EscapedPath(); Path
// alone is the DECODED form, so before the fix a literal %2F arrived as a real
// "/", a different route, silently.
func TestTunnelWSOpenPreservesEncodedPath(t *testing.T) {
	const wantPath = "/api/items%2Fsub/get"

	gotPath := make(chan string, 1)
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case gotPath <- r.URL.EscapedPath():
		default:
		}
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		// Hold the socket briefly so the agent's dial fully succeeds.
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, _, _ = c.ReadMessage()
	}))
	defer backend.Close()

	// Stand-in relay: the agent writes stream control frames here; just drain them.
	relayDone := make(chan struct{})
	relayUp := websocket.Upgrader{}
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := relayUp.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				close(relayDone)
				return
			}
		}
	}))
	defer relay.Close()

	agentConn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(relay.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial relay: %v", err)
	}
	defer agentConn.Close()

	tun := &Tunnel{
		port:       mustPort(t, backend.URL),
		httpClient: &http.Client{},
		wsStreams:  make(map[string]*wsStream),
	}
	payload, _ := json.Marshal(map[string]any{
		"stream_id": "S1",
		"url":       wantPath,
		"headers":   map[string]string{},
	})
	tun.handleTunnelWSOpen(agentConn, string(payload))

	select {
	case got := <-gotPath:
		if got != wantPath {
			t.Fatalf("backend received path %q, want %q (encoded slash not preserved)", got, wantPath)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("backend never received the WebSocket upgrade")
	}
}
