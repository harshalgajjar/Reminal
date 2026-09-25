// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// When the session ends, the agent says so with a close frame — not a bare
// TCP close, which the hosted relay only noticed on its next write and left
// viewers seeing the agent as connected until they typed or resized.
func TestAgentHangsUpWithACloseFrame(t *testing.T) {
	got := make(chan string, 1)
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		_, _, err = c.ReadMessage()
		if ce, ok := err.(*websocket.CloseError); ok {
			got <- fmt.Sprintf("%s/%d", ce.Text, ce.Code)
		} else {
			got <- "no close frame: " + err.Error()
		}
	}))
	defer srv.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	closeRelayConn(conn, "session ended")
	select {
	case s := <-got:
		if s != "session ended/1000" {
			t.Fatalf("the relay saw %q, want a normal close saying the session ended", s)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the relay never saw the agent hang up")
	}
}
