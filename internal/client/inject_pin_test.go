// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"reminal/internal/config"
	"reminal/internal/crypto"
	"reminal/internal/protocol"
	"reminal/internal/relay"
)

// Keys sent over a PIN go over one connection and one handshake, however
// many deliveries there are (a key pressed by name, the text, a Return): the
// relay paces PIN handshakes from one address, and one per delivery was
// refused as "busy" after a few calls. Through a real relay, to an agent that
// answers the handshake and counts what it is sent.
func TestKeysOverAPINGoOverOneHandshake(t *testing.T) {
	const sessionID, pin = "PINKEYS1", "135790"
	sessionKey, err := crypto.NewSessionKey()
	if err != nil {
		t.Fatal(err)
	}
	box, err := crypto.NewBox(sessionKey)
	if err != nil {
		t.Fatal(err)
	}
	// The relay, counting the viewer connections it is asked for.
	var viewerConns atomic.Int32
	srv := relay.NewServer()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) != 2 {
			http.Error(w, "bad path", http.StatusBadRequest)
			return
		}
		if parts[1] == string(protocol.RoleViewer) {
			viewerConns.Add(1)
		}
		srv.HandleSessionWS(w, r, parts[0], parts[1])
	}))
	defer ts.Close()
	t.Setenv("REMINAL_RELAY", "ws"+strings.TrimPrefix(ts.URL, "http"))

	// The agent: authenticates, answers each handshake, and keeps what it is
	// typed.
	var handshakes atomic.Int32
	typed := make(chan string, 8)
	ready := make(chan struct{})
	go func() {
		conn, _, err := websocket.DefaultDialer.Dial(config.SessionWS(sessionID, string(protocol.RoleAgent)), nil)
		if err != nil {
			t.Errorf("agent dial: %v", err)
			close(ready)
			return
		}
		defer conn.Close()
		_ = conn.WriteJSON(protocol.Message{Type: protocol.TypeAuth, Token: "AGENT-TOKEN"})
		var ok protocol.Message
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		if err := conn.ReadJSON(&ok); err != nil || ok.Type != protocol.TypeAuthOK {
			t.Errorf("agent auth: %v %q", err, ok.Type)
			close(ready)
			return
		}
		close(ready)
		for {
			_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
			var m protocol.Message
			if err := conn.ReadJSON(&m); err != nil {
				return
			}
			switch m.Type {
			case protocol.TypePakeInit:
				handshakes.Add(1)
				exID, err := crypto.ParseExID(m.ExID)
				if err != nil {
					continue
				}
				viewerElem, _ := base64.StdEncoding.DecodeString(m.Data)
				st, mine, err := crypto.NewCPace([]byte(pin), []byte(pakeChannel), pakeSID(sessionID, exID, false), false)
				if err != nil {
					continue
				}
				key, err := st.Finish(viewerElem)
				if err != nil {
					continue
				}
				wrapped, _ := crypto.WrapSessionKey(key, exID, sessionKey)
				_ = conn.WriteJSON(protocol.Message{
					Type: protocol.TypePakeResp,
					ExID: m.ExID,
					Data: base64.StdEncoding.EncodeToString(mine),
					Wrap: base64.StdEncoding.EncodeToString(wrapped),
				})
			case protocol.TypeData:
				if data, err := box.Decrypt(m.Data); err == nil {
					typed <- string(data)
				}
			}
		}
	}()
	<-ready

	sent, err := SendKeysPIN(sessionID, pin, []byte("\x15"), []byte("ls"), []byte("\r"))
	if err != nil || sent != 3 {
		t.Fatalf("three deliveries over a PIN: sent %d, %v", sent, err)
	}
	var got []string
	for len(got) < 3 {
		select {
		case s := <-typed:
			got = append(got, s)
		case <-time.After(5 * time.Second):
			t.Fatalf("the agent was typed %q, want three deliveries", got)
		}
	}
	if strings.Join(got, "|") != "\x15|ls|\r" {
		t.Errorf("typed %q in that order; want the key, the text, the Return", got)
	}
	if n := handshakes.Load(); n != 1 {
		t.Errorf("%d handshakes for three deliveries, want one", n)
	}
	if n := viewerConns.Load(); n != 1 {
		t.Errorf("%d viewer connections for three deliveries, want one", n)
	}
}

// writeDeliveries says how many deliveries were written before the first
// error, so the caller can say what landed.
func TestDeliveriesWrittenBeforeAnErrorAreCounted(t *testing.T) {
	var got []string
	fail := errors.New("gone")
	write := func(d []byte) error {
		if string(d) == "bad" {
			return fail
		}
		got = append(got, string(d))
		return nil
	}
	n, err := writeDeliveries(write, [][]byte{[]byte("a"), []byte("b"), []byte("bad"), []byte("d")}, 0)
	if n != 2 || !errors.Is(err, fail) {
		t.Fatalf("wrote %d, %v; want 2 and the error", n, err)
	}
	if strings.Join(got, ",") != "a,b" {
		t.Fatalf("written: %q", got)
	}
	if n, err := writeDeliveries(write, [][]byte{[]byte("x")}, 0); n != 1 || err != nil {
		t.Fatalf("one delivery: %d, %v", n, err)
	}
}
