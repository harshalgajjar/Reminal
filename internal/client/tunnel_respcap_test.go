// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"reminal/internal/protocol"
)

// TestTunnelRespHeadChunkUnderCap guards against a backend with large response
// headers overflowing the DO per-message cap. The first tunnel_resp message
// co-locates status+headers with a body chunk; a full-size body beside big
// headers used to exceed the ~1 MiB cap and drop the whole control socket
// (taking every in-flight request and proxied WebSocket with it). The head
// chunk's body is now shrunk so every message stays under the cap, and the body
// still reassembles byte-for-byte.
func TestTunnelRespHeadChunkUnderCap(t *testing.T) {
	// A backend with ~240 KiB of headers (one big value + several fat cookies)
	// and a body spanning several chunks.
	bigHeaderVal := strings.Repeat("x", 200*1024)
	bodyLen := tunnelChunkBytes*2 + 123456
	body := make([]byte, bodyLen)
	for i := range body {
		body[i] = byte('A' + i%26)
	}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Big", bigHeaderVal)
		for i := 0; i < 10; i++ {
			w.Header().Add("Set-Cookie", fmt.Sprintf("c%d=%s", i, strings.Repeat("y", 4096)))
		}
		w.WriteHeader(200)
		_, _ = w.Write(body)
	}))
	defer backend.Close()

	captured := runCapturedTunnelReq(t, mustPort(t, backend.URL), "GET", "/")

	if len(captured) < 2 {
		t.Fatalf("head chunk should have been split into >=2 messages, got %d", len(captured))
	}
	assertRespMessages(t, captured, bodyLen, func(i int) byte { return byte('A' + i%26) })
}

// TestTunnelRespSmallHeadersSingleMessage: the common case (tiny headers, small
// body) must still be exactly one message — the fix must not add chunks or a
// head split for ordinary responses.
func TestTunnelRespSmallHeadersSingleMessage(t *testing.T) {
	body := []byte("hello, world")
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write(body)
	}))
	defer backend.Close()

	captured := runCapturedTunnelReq(t, mustPort(t, backend.URL), "GET", "/")
	if len(captured) != 1 {
		t.Fatalf("small response should be a single message, got %d", len(captured))
	}
	assertRespMessages(t, captured, len(body), func(i int) byte { return body[i] })
}

// runCapturedTunnelReq drives handleTunnelReq against a real backend port,
// capturing every tunnel_resp message the agent writes to a stand-in relay WS.
func runCapturedTunnelReq(t *testing.T, backendPort int, method, path string) []string {
	t.Helper()

	var mu sync.Mutex
	var msgs []string
	done := make(chan struct{})
	up := websocket.Upgrader{}
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			_, data, err := c.ReadMessage()
			if err != nil {
				close(done)
				return
			}
			mu.Lock()
			msgs = append(msgs, string(data))
			mu.Unlock()
		}
	}))
	defer relay.Close()

	wsURL := "ws" + strings.TrimPrefix(relay.URL, "http")
	agentConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial relay: %v", err)
	}

	tun := &Tunnel{port: backendPort, httpClient: &http.Client{}}
	payload, _ := json.Marshal(map[string]any{
		"req_id":  "R1",
		"method":  method,
		"url":     path,
		"headers": map[string]string{},
	})
	tun.handleTunnelReq(agentConn, string(payload), "", nil)

	// handleTunnelReq has written every message by the time it returns; closing
	// the client flushes them (TCP order guarantees they precede the close frame)
	// and ends the relay read loop.
	_ = agentConn.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("relay read loop did not finish")
	}

	mu.Lock()
	defer mu.Unlock()
	return append([]string(nil), msgs...)
}

// assertRespMessages checks the shared invariants: every message under the cap,
// exactly one carries status+headers, and the concatenated body matches wantAt.
func assertRespMessages(t *testing.T, captured []string, wantLen int, wantAt func(i int) byte) {
	t.Helper()
	var reassembled []byte
	headerMsgs := 0
	for i, raw := range captured {
		if len(raw) > maxTunnelMessageBytes {
			t.Fatalf("message %d is %d bytes, over the per-message cap %d", i, len(raw), maxTunnelMessageBytes)
		}
		var outer protocol.Message
		if err := json.Unmarshal([]byte(raw), &outer); err != nil {
			t.Fatalf("message %d outer JSON: %v", i, err)
		}
		if outer.Type != protocol.TypeTunnelResp {
			t.Fatalf("message %d has type %q, want tunnel_resp", i, outer.Type)
		}
		var inner struct {
			Body    string            `json:"body"`
			More    bool              `json:"more"`
			Status  int               `json:"status"`
			Headers map[string]string `json:"headers"`
			Error   string            `json:"error"`
		}
		if err := json.Unmarshal([]byte(outer.Data), &inner); err != nil {
			t.Fatalf("message %d inner JSON: %v", i, err)
		}
		if inner.Error != "" {
			t.Fatalf("message %d carried an error: %q", i, inner.Error)
		}
		if inner.Status != 0 || inner.Headers != nil {
			headerMsgs++
			if i != 0 {
				t.Fatalf("headers appeared on message %d, not the first", i)
			}
		}
		b, err := base64.StdEncoding.DecodeString(inner.Body)
		if err != nil {
			t.Fatalf("message %d body base64: %v", i, err)
		}
		reassembled = append(reassembled, b...)
	}
	if headerMsgs != 1 {
		t.Fatalf("exactly one message must carry headers, got %d", headerMsgs)
	}
	if len(reassembled) != wantLen {
		t.Fatalf("reassembled %d body bytes, want %d", len(reassembled), wantLen)
	}
	for i := range reassembled {
		if reassembled[i] != wantAt(i) {
			t.Fatalf("body corrupted at offset %d", i)
		}
	}
}
