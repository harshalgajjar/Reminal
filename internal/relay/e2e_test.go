// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package relay

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/reminal/reminal/internal/crypto"
	"github.com/reminal/reminal/internal/protocol"
)

// TestEndToEndHandshakeNoPIN drives the REAL relay handler with a fake agent
// and viewer performing the genuine EKE. It proves the Level A change end to
// end: the viewer authenticates WITHOUT sending a PIN, yet a correct PIN still
// unwraps the session key over the wire (and a wrong PIN does not), because the
// PIN is verified entirely by the EKE — the relay never sees it.
func TestEndToEndHandshakeNoPIN(t *testing.T) {
	const (
		sessionID = "TESTSESS"
		pin       = "481920"
	)
	sessionKey := make([]byte, 32)
	for i := range sessionKey {
		sessionKey[i] = byte(i + 1)
	}

	s := NewServer()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// path: /<session>/<role>
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) != 2 {
			http.Error(w, "bad path", http.StatusBadRequest)
			return
		}
		s.HandleSessionWS(w, r, parts[0], parts[1])
	}))
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	dial := func(role string) *websocket.Conn {
		c, _, err := websocket.DefaultDialer.Dial(wsURL+"/"+sessionID+"/"+role, nil)
		if err != nil {
			t.Fatalf("dial %s: %v", role, err)
		}
		return c
	}
	readMsg := func(c *websocket.Conn) protocol.Message {
		c.SetReadDeadline(time.Now().Add(3 * time.Second))
		var m protocol.Message
		if err := c.ReadJSON(&m); err != nil {
			t.Fatalf("read: %v", err)
		}
		return m
	}

	// ---- Agent: authenticate with a token (no pin_hash), then answer kex_init.
	agent := dial("agent")
	defer agent.Close()
	if err := agent.WriteJSON(protocol.Message{Type: protocol.TypeAuth, Token: "AGENT-TOKEN"}); err != nil {
		t.Fatalf("agent auth write: %v", err)
	}
	if m := readMsg(agent); m.Type != protocol.TypeAuthOK {
		t.Fatalf("agent expected auth_ok, got %s (%s)", m.Type, m.Error)
	}
	agentErr := make(chan error, 1)
	go func() {
		for {
			var m protocol.Message
			if err := agent.ReadJSON(&m); err != nil {
				return
			}
			if m.Type != protocol.TypeKexInit {
				continue
			}
			exID, err := crypto.ParseExID(m.ExID)
			if err != nil {
				agentErr <- err
				return
			}
			blinded, _ := base64.StdEncoding.DecodeString(m.Data)
			viewerPub, err := crypto.UnblindPub(blinded, pin)
			if err != nil {
				agentErr <- err
				return
			}
			peer, err := crypto.PeerPublicKey(viewerPub)
			if err != nil {
				agentErr <- err
				return
			}
			eph, _ := crypto.NewEphemeralKey()
			shared, _ := eph.ECDH(peer)
			wrapped, _ := crypto.WrapSessionKey(shared, exID, sessionKey)
			blindedAgent, _ := crypto.BlindPub(eph.PublicKey().Bytes(), pin)
			_ = agent.WriteJSON(protocol.Message{
				Type: protocol.TypeKexResp,
				ExID: m.ExID,
				Data: base64.StdEncoding.EncodeToString(blindedAgent),
				Wrap: base64.StdEncoding.EncodeToString(wrapped),
			})
		}
	}()

	// viewerHandshake connects a viewer that sends NO pin, runs the EKE with the
	// given guess, and returns the unwrapped key (or an error).
	viewerHandshake := func(guess string) ([]byte, error) {
		v := dial("viewer")
		defer v.Close()
		// Note: no Pin field — Level A.
		if err := v.WriteJSON(protocol.Message{Type: protocol.TypeAuth}); err != nil {
			return nil, err
		}
		// Drain until auth_ok / connected, then send kex_init.
		for {
			m := readMsg(v)
			if m.Type == protocol.TypeError {
				t.Fatalf("viewer auth rejected: %s", m.Error)
			}
			if m.Type == protocol.TypeAuthOK || m.Type == protocol.TypeConnected {
				break
			}
		}
		exIDHex, exID, _ := crypto.NewExID()
		veph, _ := crypto.NewEphemeralKey()
		blindedViewer, _ := crypto.BlindPub(veph.PublicKey().Bytes(), guess)
		if err := v.WriteJSON(protocol.Message{
			Type: protocol.TypeKexInit,
			ExID: exIDHex,
			Data: base64.StdEncoding.EncodeToString(blindedViewer),
		}); err != nil {
			return nil, err
		}
		for {
			m := readMsg(v)
			if m.Type != protocol.TypeKexResp {
				continue
			}
			blindedAgent, _ := base64.StdEncoding.DecodeString(m.Data)
			agentPub, err := crypto.UnblindPub(blindedAgent, guess)
			if err != nil {
				return nil, err
			}
			peer, err := crypto.PeerPublicKey(agentPub)
			if err != nil {
				return nil, err
			}
			shared, _ := veph.ECDH(peer)
			wrapped, _ := base64.StdEncoding.DecodeString(m.Wrap)
			return crypto.UnwrapSessionKey(shared, exID, wrapped)
		}
	}

	// Correct PIN → recovers the exact session key, with no PIN on the wire.
	got, err := viewerHandshake(pin)
	if err != nil {
		t.Fatalf("correct-PIN handshake failed: %v", err)
	}
	if string(got) != string(sessionKey) {
		t.Fatalf("recovered key mismatch: got %x want %x", got, sessionKey)
	}

	// Wrong PIN → the EKE unwrap fails (this is the "PIN mismatch" the viewers
	// surface), even though the relay happily admitted the viewer.
	if _, err := viewerHandshake("000000"); err == nil {
		t.Fatalf("wrong-PIN handshake should fail the unwrap")
	}

	select {
	case err := <-agentErr:
		t.Fatalf("agent kex error: %v", err)
	default:
	}
}
