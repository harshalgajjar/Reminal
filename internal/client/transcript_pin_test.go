// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"encoding/base64"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/reminal/reminal/internal/config"
	"github.com/reminal/reminal/internal/crypto"
	"github.com/reminal/reminal/internal/protocol"
)

// TestReadTranscriptPIN drives ReadTranscriptPIN against a real in-process relay
// and a fake agent. The agent completes the genuine EKE (so the PIN is verified
// end to end and never crosses the wire), then answers the viewer's TypeResume
// with an encrypted snapshot frame. The read must return that scrollback, ANSI
// stripped — and it must NOT send any resize, since a viewer size would be min'd
// into the live PTY geometry and could shrink the real user's terminal.
func TestReadTranscriptPIN(t *testing.T) {
	isolateHome(t)
	startTestRelay(t)

	const (
		sessionID = "READPINX"
		pin       = "246810"
		wantLine  = "hello from the fake agent"
	)
	sessionKey := make([]byte, 32)
	for i := range sessionKey {
		sessionKey[i] = byte(i*7 + 3)
	}

	// The snapshot a joining viewer would receive: ANSI-wrapped, \r\n-separated,
	// exactly the shape buildSnapshot emits.
	snapshot := "\x1b[?25l\x1b[2J\x1b[3J\x1b[H\x1b[32m" + wantLine + "\x1b[0m\r\nsecond line\x1b[0m\r\n"

	var sawResize atomic.Bool
	agentReady := make(chan struct{})
	go runFakeAgent(t, sessionID, pin, sessionKey, snapshot, &sawResize, agentReady)
	<-agentReady

	text, truncated, err := ReadTranscriptPIN(sessionID, pin)
	if err != nil {
		t.Fatalf("ReadTranscriptPIN: %v", err)
	}
	if truncated {
		t.Errorf("small snapshot should not be truncated")
	}
	if !strings.Contains(text, wantLine) || !strings.Contains(text, "second line") {
		t.Fatalf("transcript missing expected lines: %q", text)
	}
	if strings.Contains(text, "\x1b") || strings.Contains(text, "\r") {
		t.Errorf("transcript still contains terminal chrome: %q", text)
	}
	if sawResize.Load() {
		t.Errorf("read sent a resize — it must never perturb the live PTY geometry")
	}

	// Wrong PIN must fail the EKE, not hang or return content.
	if _, _, err := ReadTranscriptPIN(sessionID, "999999"); err == nil {
		t.Errorf("wrong PIN should error")
	}
}

// runFakeAgent stands in for a real reminal agent on the relay: it authenticates,
// answers the viewer's EKE with the fixed sessionKey, and on TypeResume paints the
// snapshot as an encrypted TypeData frame. It records whether the viewer ever sent
// a resize.
func runFakeAgent(t *testing.T, sessionID, pin string, sessionKey []byte, snapshot string, sawResize *atomic.Bool, ready chan struct{}) {
	t.Helper()
	box, err := crypto.NewBox(sessionKey)
	if err != nil {
		t.Errorf("agent box: %v", err)
		close(ready)
		return
	}
	wsURL := config.SessionWS(sessionID, string(protocol.RoleAgent))
	conn, _, derr := websocket.DefaultDialer.Dial(wsURL, nil)
	if derr != nil {
		err = derr
		t.Errorf("agent dial: %v", err)
		close(ready)
		return
	}
	defer conn.Close()
	if err := conn.WriteJSON(protocol.Message{Type: protocol.TypeAuth, Token: "AGENT-TOKEN"}); err != nil {
		t.Errorf("agent auth: %v", err)
		close(ready)
		return
	}
	close(ready)

	for {
		conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		var m protocol.Message
		if err := conn.ReadJSON(&m); err != nil {
			return
		}
		switch m.Type {
		case protocol.TypeKexInit:
			exID, err := crypto.ParseExID(m.ExID)
			if err != nil {
				continue
			}
			blinded, _ := base64.StdEncoding.DecodeString(m.Data)
			viewerPub, err := crypto.UnblindPub(blinded, pin)
			if err != nil {
				continue
			}
			peer, err := crypto.PeerPublicKey(viewerPub)
			if err != nil {
				continue
			}
			eph, _ := crypto.NewEphemeralKey()
			shared, _ := eph.ECDH(peer)
			wrapped, _ := crypto.WrapSessionKey(shared, exID, sessionKey)
			blindedAgent, _ := crypto.BlindPub(eph.PublicKey().Bytes(), pin)
			_ = conn.WriteJSON(protocol.Message{
				Type: protocol.TypeKexResp,
				ExID: m.ExID,
				Data: base64.StdEncoding.EncodeToString(blindedAgent),
				Wrap: base64.StdEncoding.EncodeToString(wrapped),
			})
		case protocol.TypeResize:
			sawResize.Store(true)
		case protocol.TypeResume:
			enc, err := box.Encrypt([]byte(snapshot))
			if err != nil {
				t.Errorf("encrypt snapshot: %v", err)
				return
			}
			_ = conn.WriteJSON(protocol.Message{Type: protocol.TypeData, Data: enc, Seq: 1})
		}
	}
}
