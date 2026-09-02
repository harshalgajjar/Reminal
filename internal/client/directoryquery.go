// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/reminal/reminal/internal/config"
	"github.com/reminal/reminal/internal/crypto"
	"github.com/reminal/reminal/internal/protocol"
)

// ErrDirUnreachable means the machine's directory channel isn't being served —
// the machine is offline, or no session is running to host it. Callers render
// this as an offline machine rather than a hard failure.
var ErrDirUnreachable = errors.New("machine's directory channel is not reachable")

// DirectoryTimeout bounds a single machine's directory query end-to-end.
const DirectoryTimeout = 6 * time.Second

// maxDirMessageBytes caps a single WS frame read from the (untrusted) relay on a
// directory connection, matching the relay's own 1 MB limit. A legit dir_resp is
// a few KB even with hundreds of sessions; anything larger is hostile.
const maxDirMessageBytes = 1 << 20

// QueryDirectory reaches the machine identified by machinePub over its
// owner-derived directory channel, proves this device is an enrolled owner with
// the same signed handshake used for a PIN-free connect, and returns the
// machine's live sessions. The reply is end-to-end encrypted; the relay only
// ever sees opaque frames on an opaque channel id.
func QueryDirectory(machinePub ed25519.PublicKey, timeout time.Duration) (protocol.DirResponse, error) {
	return queryDirectory(machinePub, timeout, dirQueryReq{})
}

// dirQueryReq is the optional encrypted TypeDirQuery payload. Older hosts
// ignore Data and still return the session list.
type dirQueryReq struct {
	Pattern    string `json:"pattern,omitempty"`
	Transcript string `json:"transcript,omitempty"` // session id to dump
	KeysID     string `json:"keys_id,omitempty"`    // session id to type into
	Keys       string `json:"keys,omitempty"`       // base64 of PTY bytes
}

func (r dirQueryReq) empty() bool {
	return strings.TrimSpace(r.Pattern) == "" &&
		strings.TrimSpace(r.Transcript) == "" &&
		strings.TrimSpace(r.KeysID) == "" &&
		strings.TrimSpace(r.Keys) == ""
}

// queryDirectory is QueryDirectory with optional search / one-session dump.
func queryDirectory(machinePub ed25519.PublicKey, timeout time.Duration, req dirQueryReq) (protocol.DirResponse, error) {
	var zero protocol.DirResponse
	if len(machinePub) != ed25519.PublicKeySize {
		return zero, fmt.Errorf("machine key must be %d bytes", ed25519.PublicKeySize)
	}
	deviceKey, err := loadOrCreateDeviceKey()
	if err != nil {
		return zero, err
	}
	devicePub := deviceKey.Public().(ed25519.PublicKey)

	dirID := crypto.DeriveDirectoryID(machinePub)
	wsURL := config.SessionWS(dirID, string(protocol.RoleViewer))
	// Bound the DIAL by the same timeout, not just the reads: DefaultDialer's
	// 45s HandshakeTimeout would otherwise let one unreachable machine stall
	// `reminal machines` (which waits on every query) for ~45s instead of ~6s.
	dialer := *websocket.DefaultDialer
	dialer.HandshakeTimeout = timeout
	conn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		return zero, ErrDirUnreachable
	}
	defer conn.Close()
	// The relay is untrusted, and `reminal machines` opens one of these per owned
	// machine in parallel — cap the read so a malicious relay can't OOM us with an
	// oversized frame (it just fails to that machine). Matches the relay's own cap.
	conn.SetReadLimit(maxDirMessageBytes)
	_ = conn.SetReadDeadline(time.Now().Add(timeout))

	// Join the channel. A machine that isn't hosting → relay replies "not ready"
	// → treat as unreachable/offline.
	if err := writeDir(conn, protocol.Message{Type: protocol.TypeAuth}); err != nil {
		return zero, err
	}
	if err := waitAuthOK(conn); err != nil {
		return zero, ErrDirUnreachable
	}

	// Prove ownership: the identical own_init the PIN-free connect sends, but the
	// transcript binds the directory id.
	exHex, exID, err := crypto.NewExID()
	if err != nil {
		return zero, err
	}
	eph, err := crypto.NewEphemeralKey()
	if err != nil {
		return zero, err
	}
	viewerEph := eph.PublicKey().Bytes()
	sig := crypto.SignOwner(deviceKey, crypto.OwnerClientTranscript(dirID, viewerEph, devicePub))
	if err := writeDir(conn, protocol.Message{
		Type:      protocol.TypeOwnerInit,
		ExID:      exHex,
		Data:      base64.StdEncoding.EncodeToString(viewerEph),
		DevicePub: base64.StdEncoding.EncodeToString(devicePub),
		DeviceSig: base64.StdEncoding.EncodeToString(sig),
	}); err != nil {
		return zero, err
	}

	box, err := readOwnerResp(conn, dirID, exHex, exID, viewerEph, devicePub, machinePub, eph)
	if err != nil {
		return zero, err
	}

	// Ask, and read the encrypted session list. Search / dump ride as
	// encrypted Data so older hosts (which ignore Data) still answer.
	qmsg := protocol.Message{Type: protocol.TypeDirQuery}
	req.Pattern = strings.TrimSpace(req.Pattern)
	req.Transcript = strings.TrimSpace(req.Transcript)
	req.KeysID = strings.TrimSpace(req.KeysID)
	req.Keys = strings.TrimSpace(req.Keys)
	if !req.empty() {
		raw, err := json.Marshal(req)
		if err != nil {
			return zero, err
		}
		enc, err := box.Encrypt(raw)
		if err != nil {
			return zero, err
		}
		qmsg.Data = enc
	}
	if err := writeDir(conn, qmsg); err != nil {
		return zero, err
	}
	for {
		var msg protocol.Message
		if err := readDir(conn, &msg); err != nil {
			return zero, err
		}
		if msg.Type != protocol.TypeDirResp {
			continue
		}
		plain, err := box.Decrypt(msg.Data)
		if err != nil {
			return zero, fmt.Errorf("directory: decrypt failed")
		}
		var resp protocol.DirResponse
		if err := json.Unmarshal(plain, &resp); err != nil {
			return zero, fmt.Errorf("directory: bad response")
		}
		return resp, nil
	}
}

// readOwnerResp waits for the machine's own_resp, verifies its signature against
// the machine key we already know (stronger than the connect path's trust-on-
// first-use — here an impostor signing with any other key simply fails), and
// returns the established encryption box.
func readOwnerResp(conn *websocket.Conn, dirID, exHex string, exID, viewerEph, devicePub, machinePub []byte, eph *ecdh.PrivateKey) (*crypto.Box, error) {
	for {
		var msg protocol.Message
		if err := readDir(conn, &msg); err != nil {
			return nil, err
		}
		// The host BROADCASTS own_resp to every viewer on the channel, so when
		// more than one owner queries the same machine at once we'll also see the
		// others' replies. Skip any that isn't the answer to OUR own_init — using
		// someone else's ephemeral/signature would fail verification and wrongly
		// error out a legitimate query.
		if msg.Type != protocol.TypeOwnerResp || msg.ExID != exHex {
			continue
		}
		agentEph, err := base64.StdEncoding.DecodeString(msg.Data)
		if err != nil || len(agentEph) != crypto.PubKeyBytes {
			return nil, fmt.Errorf("directory: bad agent key")
		}
		machineSig, err := base64.StdEncoding.DecodeString(msg.MachineSig)
		if err != nil {
			return nil, fmt.Errorf("directory: bad machine signature")
		}
		// Verify against the machine key we already hold. Any other signer fails.
		if !crypto.VerifyOwner(ed25519.PublicKey(machinePub),
			crypto.OwnerServerTranscript(dirID, viewerEph, agentEph, devicePub, machinePub), machineSig) {
			return nil, fmt.Errorf("directory: machine signature invalid — refusing")
		}
		// The self-reported key must also be the one we expect (defence in depth;
		// the signature check above already binds it).
		if rep, err := base64.StdEncoding.DecodeString(msg.MachinePub); err != nil || !bytes.Equal(rep, machinePub) {
			return nil, fmt.Errorf("directory: unexpected machine identity")
		}
		peerKey, err := crypto.PeerPublicKey(agentEph)
		if err != nil {
			return nil, fmt.Errorf("directory: invalid agent key")
		}
		shared, err := eph.ECDH(peerKey)
		if err != nil {
			return nil, fmt.Errorf("directory: ecdh: %w", err)
		}
		wrapped, err := base64.StdEncoding.DecodeString(msg.Wrap)
		if err != nil {
			return nil, fmt.Errorf("directory: bad wrap")
		}
		sessionKey, err := crypto.UnwrapSessionKey(shared, exID, wrapped)
		if err != nil {
			return nil, fmt.Errorf("directory: session key unwrap failed")
		}
		return crypto.NewBox(sessionKey)
	}
}

func writeDir(conn *websocket.Conn, msg protocol.Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_ = conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
	return conn.WriteMessage(websocket.TextMessage, data)
}

func readDir(conn *websocket.Conn, out *protocol.Message) error {
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		if err := json.Unmarshal(raw, out); err != nil {
			continue
		}
		if out.Type == protocol.TypeError {
			return fmt.Errorf("%s", out.Error)
		}
		return nil
	}
}

func waitAuthOK(conn *websocket.Conn) error {
	for {
		var msg protocol.Message
		if err := readDir(conn, &msg); err != nil {
			return err
		}
		if msg.Type == protocol.TypeAuthOK {
			return nil
		}
	}
}

// parseDirQuery decrypts an optional TypeDirQuery payload. Empty / garbage
// returns a zero req so the host still answers with a plain session list.
func parseDirQuery(box *crypto.Box, data string) dirQueryReq {
	if box == nil || data == "" {
		return dirQueryReq{}
	}
	plain, err := box.Decrypt(data)
	if err != nil {
		return dirQueryReq{}
	}
	var req dirQueryReq
	if json.Unmarshal(plain, &req) != nil {
		return dirQueryReq{}
	}
	req.Pattern = strings.TrimSpace(req.Pattern)
	req.Transcript = strings.TrimSpace(req.Transcript)
	req.KeysID = strings.TrimSpace(req.KeysID)
	req.Keys = strings.TrimSpace(req.Keys)
	return req
}
