// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/reminal/reminal/internal/config"
	"github.com/reminal/reminal/internal/protocol"
	"github.com/reminal/reminal/internal/session"
)

const maxInjectBytes = 8 << 10

// PrepareInjectKeys turns an MCP/agent string into PTY bytes: \n becomes
// Enter (\r). enter appends \r if the payload does not already end with one.
func PrepareInjectKeys(s string, enter bool) ([]byte, error) {
	s = strings.ReplaceAll(s, "\n", "\r")
	if enter && !strings.HasSuffix(s, "\r") {
		s += "\r"
	}
	if s == "" {
		return nil, fmt.Errorf("keys is empty")
	}
	b := []byte(s)
	if len(b) > maxInjectBytes {
		return nil, fmt.Errorf("keys too long (max %d bytes)", maxInjectBytes)
	}
	return b, nil
}

func (a *Agent) injectKeys(data []byte) error {
	if len(data) == 0 {
		return fmt.Errorf("keys is empty")
	}
	if a == nil || a.term == nil {
		return fmt.Errorf("no pty")
	}
	_, err := a.term.Write(data)
	return err
}

func (a *Agent) handleKeysControl(b64 string) error {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return fmt.Errorf("keys encoding: %w", err)
	}
	if len(raw) > maxInjectBytes {
		return fmt.Errorf("keys too long (max %d bytes)", maxInjectBytes)
	}
	return a.injectKeys(raw)
}

// InjectAgentKeys writes bytes into a local agent's PTY via the control socket.
func InjectAgentKeys(pid int, data []byte) error {
	if len(data) == 0 {
		return fmt.Errorf("keys is empty")
	}
	if len(data) > maxInjectBytes {
		return fmt.Errorf("keys too long (max %d bytes)", maxInjectBytes)
	}
	_, err := sendControlToDeadline(pid, "keys "+base64.StdEncoding.EncodeToString(data), searchControlWait)
	return err
}

// SendKeysPIN types into any reminal session you have the id and PIN for,
// the same path `reminal connect` uses (EKE, then encrypted TypeData). No
// owner enrollment and no TTY. The connection closes after the keys are sent.
func SendKeysPIN(sessionID, pin string, data []byte) error {
	if len(data) == 0 {
		return fmt.Errorf("keys is empty")
	}
	if len(data) > maxInjectBytes {
		return fmt.Errorf("keys too long (max %d bytes)", maxInjectBytes)
	}
	v, err := NewViewer(sessionID, pin)
	if err != nil {
		return err
	}
	return v.injectOnce(data)
}

func (v *Viewer) injectOnce(data []byte) error {
	wsURL := config.SessionWS(v.sessionID, string(protocol.RoleViewer))
	dialer := *websocket.DefaultDialer
	dialer.HandshakeTimeout = DirectoryTimeout
	conn, resp, err := dialer.Dial(wsURL, nil)
	if err != nil {
		if resp != nil && resp.StatusCode == 429 {
			return &rateLimitedError{retryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
		}
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()
	conn.SetReadLimit(maxRelayMessageBytes)
	_ = conn.SetReadDeadline(time.Now().Add(kexTimeout))

	if err := v.authenticate(conn); err != nil {
		return err
	}
	if err := v.negotiateSessionKey(conn); err != nil {
		return err
	}
	enc, err := v.box.Encrypt(data)
	if err != nil {
		return err
	}
	if err := v.writeMsg(conn, protocol.Message{Type: protocol.TypeData, Data: enc}); err != nil {
		return err
	}
	// WriteMessage returns after the frame is on the socket; a tiny pause
	// lets the relay forward it before we drop the connection.
	time.Sleep(50 * time.Millisecond)
	return nil
}

// InjectKeysByID injects into a local session by id.
func InjectKeysByID(id string, data []byte) error {
	a, err := session.ReadActiveByID(id)
	if err != nil {
		return err
	}
	if a.IsPort() {
		return fmt.Errorf("session %s is a port forward — no terminal", a.ID)
	}
	return InjectAgentKeys(a.PID, data)
}

// SendRemoteKeys asks an owned machine to type into one of its sessions.
// ok is false when the host listed but did not inject (older reminal).
func SendRemoteKeys(machinePub ed25519.PublicKey, sessionID string, data []byte) (ok bool, err error) {
	if len(data) == 0 {
		return false, fmt.Errorf("keys is empty")
	}
	resp, err := queryDirectory(machinePub, DirectoryTimeout, dirQueryReq{
		KeysID: sessionID,
		Keys:   base64.StdEncoding.EncodeToString(data),
	})
	if err != nil {
		return false, err
	}
	if resp.KeysError != "" {
		return false, fmt.Errorf("%s", resp.KeysError)
	}
	return resp.KeysOK, nil
}

func applyLocalKeys(resp *protocol.DirResponse, sessionID, b64 string) {
	if resp == nil {
		return
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		resp.KeysError = "keys encoding"
		return
	}
	if err := InjectKeysByID(sessionID, raw); err != nil {
		resp.KeysError = err.Error()
		return
	}
	resp.KeysOK = true
}
