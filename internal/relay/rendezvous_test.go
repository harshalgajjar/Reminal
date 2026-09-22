// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package relay

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// The local relay pairs a copy with a paste by code, passes their frames
// untouched, and spends the code: one paste per offer, none after.
func TestRendezvousPairsOnceByCode(t *testing.T) {
	z := NewRendezvous()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /rv/{code}/{role}", func(w http.ResponseWriter, r *http.Request) {
		z.HandleWS(w, r, r.PathValue("code"), r.PathValue("role"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	dial := func(code, role string) *websocket.Conn {
		t.Helper()
		c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/rv/"+code+"/"+role, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		return c
	}
	read := func(c *websocket.Conn) (int, string) {
		t.Helper()
		_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
		mt, b, err := c.ReadMessage()
		if err != nil {
			return -1, err.Error()
		}
		return mt, string(b)
	}

	if _, got := read(dial("NOSOURCE", "paste")); !strings.Contains(got, "too old or invalid") {
		t.Fatalf("a paste with no source: %q", got)
	}
	src := dial("ABCD1234", "source")
	time.Sleep(50 * time.Millisecond)
	dst := dial("ABCD1234", "paste")
	time.Sleep(50 * time.Millisecond)
	_ = src.WriteMessage(websocket.TextMessage, []byte(`{"type":"kex","k":"abc"}`))
	if mt, got := read(dst); mt != websocket.TextMessage || got != `{"type":"kex","k":"abc"}` {
		t.Fatalf("source → paste: %d %q", mt, got)
	}
	_ = dst.WriteMessage(websocket.BinaryMessage, []byte{1, 2, 3})
	if mt, got := read(src); mt != websocket.BinaryMessage || got != "\x01\x02\x03" {
		t.Fatalf("paste → source: %d %q", mt, got)
	}
	if _, got := read(dial("ABCD1234", "paste")); !strings.Contains(got, "in progress") && !strings.Contains(got, "too old") {
		t.Fatalf("a second paste on a paired code: %q", got)
	}
	src.Close()
	time.Sleep(100 * time.Millisecond)
	if _, got := read(dial("ABCD1234", "source")); !strings.Contains(got, "too old or invalid") {
		t.Fatalf("a spent code was offered again: %q", got)
	}
}
