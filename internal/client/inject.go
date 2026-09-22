// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"reminal/internal/config"
	"reminal/internal/protocol"
	"reminal/internal/session"
)

const maxInjectBytes = 8 << 10

// EnterSettle is how long to wait between the text and the Return that submits
// it. See PrepareInjectKeysSplit for why they cannot travel together.
const EnterSettle = 250 * time.Millisecond

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

// PrepareInjectKeysSplit is PrepareInjectKeys for callers that can deliver in
// two parts: the text, then the Return on its own.
//
// A shell runs a line the moment it sees \r anywhere in the stream, so gluing
// the Return to the text works there and always did. A full-screen app does not
// read the stream that way. It classifies each chunk it reads, and a chunk
// carrying a long run of characters is pasted content, not typing -- so a \r
// riding along at the end is filed as a newline inside that paste and lands in
// the composer instead of submitting it. That is why a long briefing sent to
// another agent sits in its input box unsent while a short command runs fine:
// the short one arrives small enough to read as typing.
//
// Sending the Return separately settles it. The text is a paste, the Return is
// its own small chunk a moment later, and every reader -- shell or full-screen
// app -- treats that as the key being pressed.
//
// tail is nil when the caller asked for no Return, or when the text already
// ends in one (the Return is then part of what the caller wrote, and splitting
// it off would change their bytes).
func PrepareInjectKeysSplit(s string, enter bool) (body, tail []byte, err error) {
	if enter && s != "" && !strings.HasSuffix(s, "\n") && !strings.HasSuffix(s, "\r") {
		b, err := PrepareInjectKeys(s, false)
		if err != nil {
			return nil, nil, err
		}
		return b, []byte{'\r'}, nil
	}
	b, err := PrepareInjectKeys(s, enter)
	if err != nil {
		return nil, nil, err
	}
	return b, nil, nil
}

func (a *Agent) injectKeys(data []byte) error {
	if len(data) == 0 {
		return fmt.Errorf("keys is empty")
	}
	if a == nil || a.term == nil {
		return fmt.Errorf("no pty")
	}
	_, err := a.term.Write(a.wrapPaste(data))
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

// Bracketed paste (DECSET 2004): the foreground program asking that pasted text
// arrive wrapped in markers, so it can insert the block whole instead of
// interpreting it as hundreds of keystrokes.
var (
	pasteOn    = []byte("\x1b[?2004h")
	pasteOff   = []byte("\x1b[?2004l")
	pasteStart = []byte("\x1b[200~")
	pasteEnd   = []byte("\x1b[201~")
)

// sniffBracketedPaste watches the PTY for the program turning the mode on and
// off. Read from the output stream rather than the emulator because the
// emulator does not surface it, and a carry covers a sequence split across two
// reads.
func (a *Agent) sniffBracketedPaste(chunk []byte) {
	const carry = 8
	buf := chunk
	if len(a.pasteCarry) > 0 {
		buf = append(append([]byte(nil), a.pasteCarry...), chunk...)
	}
	if i, j := bytes.LastIndex(buf, pasteOn), bytes.LastIndex(buf, pasteOff); i >= 0 || j >= 0 {
		a.bracketedPaste.Store(i > j)
	}
	if len(chunk) > carry {
		a.pasteCarry = append(a.pasteCarry[:0], chunk[len(chunk)-carry:]...)
	} else {
		a.pasteCarry = append(a.pasteCarry[:0], chunk...)
	}
}

// wrapPaste marks text as pasted when the program asked for that.
//
// Without it a long injected message reaches an agent TUI as a burst of
// individual keypresses, and one of them redrew its input a character at a
// time — the message arrived down the screen, one letter per line. With the
// markers the program inserts the block in one go, which is what a human
// pasting does. Control-only payloads (a bare Enter, a ^C) are keystrokes, not
// paste, and are never wrapped.
// pasteMinLen is the shortest payload treated as pasted text. A single "y" or
// "1" answering a menu is a KEYPRESS — a single-key menu ignores pasted text
// by design, so wrapping it would re-break exactly the approval path that the
// split Enter fixed. Anything a human would actually paste is longer.
const pasteMinLen = 8

func (a *Agent) wrapPaste(data []byte) []byte {
	if !a.bracketedPaste.Load() || len(data) < pasteMinLen {
		return data
	}
	printable := false
	for _, c := range data {
		if c >= 0x20 && c != 0x7f {
			printable = true
			break
		}
	}
	if !printable {
		return data
	}
	out := make([]byte, 0, len(data)+len(pasteStart)+len(pasteEnd))
	out = append(out, pasteStart...)
	out = append(out, data...)
	return append(out, pasteEnd...)
}
