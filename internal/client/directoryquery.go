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
	// Restart asks the host to hot-restart every session on it. It rides the
	// query rather than taking a message type of its own so it needs no relay
	// forwarding change — and dir_query is already reserved to the machine
	// channel (dirChannelOnly), so it is owner-only by construction.
	Restart bool `json:"restart,omitempty"`
	// OpenID names one exposed port whose public link and gate PIN the caller
	// wants back (DirResponse.OpenURL/OpenPIN). Asked per click rather than
	// carried by every listing, so the PIN never reaches the viewer's cache —
	// see protocol.DirResponse.OpenURL.
	OpenID string `json:"open_id,omitempty"`
}

func (r dirQueryReq) empty() bool {
	return strings.TrimSpace(r.Pattern) == "" &&
		strings.TrimSpace(r.Transcript) == "" &&
		strings.TrimSpace(r.KeysID) == "" &&
		strings.TrimSpace(r.Keys) == "" &&
		strings.TrimSpace(r.OpenID) == "" &&
		!r.Restart
}

// dialDirectoryOwner connects to a machine's directory channel and proves
// ownership (the same own_init the PIN-free connect sends, transcript-bound to
// the directory id), returning the live connection and the established
// encryption box. The caller owns the connection and MUST Close it. Shared by
// queryDirectory (list/search), SpawnOnMachine (new_session), and KillOnMachine
// (dir_kill).
func dialDirectoryOwner(machinePub ed25519.PublicKey, timeout time.Duration) (*websocket.Conn, *crypto.Box, error) {
	if len(machinePub) != ed25519.PublicKeySize {
		return nil, nil, fmt.Errorf("machine key must be %d bytes", ed25519.PublicKeySize)
	}
	deviceKey, err := loadOrCreateDeviceKey()
	if err != nil {
		return nil, nil, err
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
		return nil, nil, ErrDirUnreachable
	}
	// The relay is untrusted, and `reminal machines` opens one of these per owned
	// machine in parallel — cap the read so a malicious relay can't OOM us with an
	// oversized frame (it just fails to that machine). Matches the relay's own cap.
	conn.SetReadLimit(maxDirMessageBytes)
	_ = conn.SetReadDeadline(time.Now().Add(timeout))

	// Join the channel. A machine that isn't hosting → relay replies "not ready"
	// → treat as unreachable/offline.
	if err := writeDir(conn, protocol.Message{Type: protocol.TypeAuth}); err != nil {
		conn.Close()
		return nil, nil, err
	}
	if err := waitAuthOK(conn); err != nil {
		conn.Close()
		return nil, nil, ErrDirUnreachable
	}

	// Prove ownership: the identical own_init the PIN-free connect sends, but the
	// transcript binds the directory id.
	exHex, exID, err := crypto.NewExID()
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	eph, err := crypto.NewEphemeralKey()
	if err != nil {
		conn.Close()
		return nil, nil, err
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
		conn.Close()
		return nil, nil, err
	}

	box, err := readOwnerResp(conn, dirID, exHex, exID, viewerEph, devicePub, machinePub, eph)
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	return conn, box, nil
}

// queryDirectory is QueryDirectory with optional search / one-session dump.
func queryDirectory(machinePub ed25519.PublicKey, timeout time.Duration, req dirQueryReq) (protocol.DirResponse, error) {
	var zero protocol.DirResponse
	conn, box, err := dialDirectoryOwner(machinePub, timeout)
	if err != nil {
		return zero, err
	}
	defer conn.Close()

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

// SpawnOnMachine asks an owned machine to start a fresh detached session over
// its directory channel — the CLI counterpart to the web Machines panel's
// "+ New session on this host" — and returns the new session's credentials.
func SpawnOnMachine(machinePub ed25519.PublicKey, name, cwd string, timeout time.Duration) (*SpawnedSession, error) {
	conn, box, err := dialDirectoryOwner(machinePub, timeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	reqID, _, err := crypto.NewExID()
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(struct {
		Name  string `json:"name"`
		Cwd   string `json:"cwd"`
		ReqID string `json:"req_id"`
	}{name, cwd, reqID})
	if err != nil {
		return nil, err
	}
	enc, err := box.Encrypt(body)
	if err != nil {
		return nil, err
	}
	if err := writeDir(conn, protocol.Message{Type: protocol.TypeNewSession, Data: enc}); err != nil {
		return nil, err
	}
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	for {
		var msg protocol.Message
		if err := readDir(conn, &msg); err != nil {
			return nil, err
		}
		if msg.Type != protocol.TypeNewSession {
			continue
		}
		plain, err := box.Decrypt(msg.Data)
		if err != nil {
			return nil, fmt.Errorf("directory: decrypt failed")
		}
		var resp struct {
			ReqID string `json:"req_id"`
			ID    string `json:"id"`
			PIN   string `json:"pin"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal(plain, &resp); err != nil {
			return nil, fmt.Errorf("directory: bad response")
		}
		if resp.ReqID != "" && resp.ReqID != reqID {
			continue // a stale reply to some other request on this channel
		}
		if resp.Error != "" {
			return nil, fmt.Errorf("%s", resp.Error)
		}
		if resp.ID == "" {
			return nil, fmt.Errorf("the machine did not start a session")
		}
		return &SpawnedSession{ID: resp.ID, PIN: resp.PIN}, nil
	}
}

// KillOnMachine terminates a session on an owned machine over its directory
// channel — the CLI counterpart to the Machines panel's kill button.
func KillOnMachine(machinePub ed25519.PublicKey, sessionID string, timeout time.Duration) error {
	conn, box, err := dialDirectoryOwner(machinePub, timeout)
	if err != nil {
		return err
	}
	defer conn.Close()

	reqID, _, err := crypto.NewExID()
	if err != nil {
		return err
	}
	body, err := json.Marshal(struct {
		ID    string `json:"id"`
		ReqID string `json:"req_id"`
	}{sessionID, reqID})
	if err != nil {
		return err
	}
	enc, err := box.Encrypt(body)
	if err != nil {
		return err
	}
	if err := writeDir(conn, protocol.Message{Type: protocol.TypeDirKill, Data: enc}); err != nil {
		return err
	}
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	for {
		var msg protocol.Message
		if err := readDir(conn, &msg); err != nil {
			return err
		}
		if msg.Type != protocol.TypeDirKill {
			continue
		}
		plain, err := box.Decrypt(msg.Data)
		if err != nil {
			return fmt.Errorf("directory: decrypt failed")
		}
		var ack struct {
			ReqID string `json:"req_id"`
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal(plain, &ack); err != nil {
			return fmt.Errorf("directory: bad response")
		}
		if ack.ReqID != "" && ack.ReqID != reqID {
			continue
		}
		if ack.Error != "" {
			return fmt.Errorf("%s", ack.Error)
		}
		if !ack.OK {
			return fmt.Errorf("the machine did not confirm the kill")
		}
		return nil
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
