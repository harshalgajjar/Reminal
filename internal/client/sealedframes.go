// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"encoding/json"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"reminal/internal/crypto"
	"reminal/internal/protocol"
)

// Sealed frames, agent side (see crypto/frames.go for the format).
//
// Outgoing: every session message is written sealed. While a viewer that
// predates sealed frames is attached, it is also written as before, so that
// viewer still sees the session.
//
// Incoming: a sealed frame is accepted once, from a stream a handshake issued,
// in order, as the message type it was sealed as. An unsealed message is a
// viewer that predates sealed frames: it is accepted only if it decrypts with
// the session key, does not carry one of the agent's own nonces, and has not
// been accepted before. The session writes both forms from the moment a
// viewer's handshake does not ask for sealed frames, or such a message
// arrives, until the last viewer leaves.

// maxStreams bounds how many handshakes' streams are remembered. Past it the
// least recently used is forgotten, and a frame on it is refused, never
// accepted afresh.
const maxStreams = 4096

// legacySeen bounds how many earlier-form messages' nonces are remembered, so
// that one is accepted once.
const legacySeen = 1 << 18

type sealState struct {
	once sync.Once
	keys *crypto.FrameKeys

	outCtr uint64 // under Agent.writeMu

	mu         sync.Mutex
	streams    map[uint32]*streamUse
	tick       uint64
	nextStream uint32

	legacy atomic.Bool // a viewer that predates sealed frames is attached
	// legacyProven: an earlier-form message that needed the session key has
	// arrived since legacy was set. Until then the earlier form is written
	// only for legacyGrace after the handshake that asked for it: long enough
	// for an older reader to get its screen, and a bound on what a handshake
	// without the PIN can cause. legacyAt is when legacy was last set.
	legacyProven atomic.Bool
	legacyAt     atomic.Int64

	seenMu  sync.Mutex
	seen    map[[12]byte]struct{}
	seenLog [][12]byte
	seenPos int
}

type streamUse struct {
	last   uint64 // last counter accepted
	used   uint64 // tick of the last frame accepted, or of issue
	src    string // source tag of the handshake that issued it (may be "")
	proven bool   // its source has been marked proven
}

// legacyGrace is how long the earlier form is written after a handshake
// without sealed frames, unless a message proving the PIN arrives.
const legacyGrace = 30 * time.Second

// frameKeys returns this agent's frame keys (a session's, or the machine
// channel's), or nil for an agent without a session key.
func (a *Agent) frameKeys() *crypto.FrameKeys {
	if len(a.sessionKey) != 32 {
		return nil
	}
	a.seal.once.Do(func() {
		k, err := crypto.NewFrameKeys(a.sessionKey)
		if err != nil {
			log.Printf("sealed frames off: %v", err)
			return
		}
		a.seal.keys = k
		a.seal.streams = map[uint32]*streamUse{}
		a.seal.seen = map[[12]byte]struct{}{}
	})
	return a.seal.keys
}

// sealInfoFor issues a stream to a viewer that asked for sealed frames and
// returns its encrypted SealInfo for the handshake answer; "" otherwise.
func (a *Agent) sealInfoFor(frames int, key, exID []byte, src string) string {
	k := a.frameKeys()
	if k == nil || frames < 1 {
		return ""
	}
	a.seal.mu.Lock()
	a.seal.nextStream++
	stream := a.seal.nextStream
	a.seal.tick++
	a.seal.streams[stream] = &streamUse{used: a.seal.tick, src: src}
	if len(a.seal.streams) > maxStreams {
		var oldest uint32
		var at uint64 = ^uint64(0)
		for id, u := range a.seal.streams {
			if u.used < at {
				oldest, at = id, u.used
			}
		}
		delete(a.seal.streams, oldest)
	}
	a.seal.mu.Unlock()
	a.writeMu.Lock()
	at := a.seal.outCtr
	a.writeMu.Unlock()
	info, err := crypto.SealSealInfo(key, exID, crypto.SealInfo{Stream: stream, AgentAt: at})
	if err != nil {
		return ""
	}
	return info
}

// sealedFrame builds the sealed form of msg with counter ctr. Called with
// writeMu held, so counters go out in the order they are given.
func (a *Agent) sealedFrame(k *crypto.FrameKeys, msg protocol.Message, ctr uint64) ([]byte, error) {
	kind, payload := crypto.FrameEmpty, []byte(nil)
	if msg.Data != "" {
		if pt, err := a.box.Decrypt(msg.Data); err == nil {
			kind, payload = crypto.FrameBoxed, pt
		} else {
			kind, payload = crypto.FrameRaw, []byte(msg.Data)
		}
	}
	data, err := k.Seal(crypto.FromAgent, string(msg.Type), 0, ctr, msg.Seq, kind, payload)
	if err != nil {
		return nil, err
	}
	return json.Marshal(protocol.Message{Type: protocol.TypeSealed, Inner: msg.Type, Ctr: ctr, Seq: msg.Seq, Data: data})
}

// writeSessionMsg writes one sealable message: sealed, and as it is too while
// a viewer that predates sealed frames is attached.
func (a *Agent) writeSessionMsg(conn *websocket.Conn, k *crypto.FrameKeys, msg protocol.Message) error {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	a.seal.outCtr++
	sealed, err := a.sealedFrame(k, msg, a.seal.outCtr)
	if err != nil {
		return err
	}
	if err := a.writeRaw(conn, sealed); err != nil {
		return err
	}
	if !a.legacyWanted() {
		return nil
	}
	plain, err := a.legacyForm(k, msg)
	if err != nil {
		return err
	}
	return a.writeRaw(conn, plain)
}

// legacyForm is msg as a viewer from before sealed frames reads it. A payload
// encrypted with the session key is encrypted again under one of the agent's
// own nonces (FrameKeys.OwnNonce), so it can never be taken as input.
func (a *Agent) legacyForm(k *crypto.FrameKeys, msg protocol.Message) ([]byte, error) {
	if msg.Data != "" {
		if pt, err := a.box.Decrypt(msg.Data); err == nil {
			nonce, err := k.OwnNonce()
			if err != nil {
				return nil, err
			}
			if msg.Data, err = a.box.EncryptWithNonce(nonce, pt); err != nil {
				return nil, err
			}
		}
	}
	return json.Marshal(msg)
}

// seenBefore records an earlier-form message's nonce and reports whether it
// was already recorded.
func (a *Agent) seenBefore(nonce []byte) bool {
	var k [12]byte
	copy(k[:], nonce)
	a.seal.seenMu.Lock()
	defer a.seal.seenMu.Unlock()
	if _, ok := a.seal.seen[k]; ok {
		return true
	}
	if len(a.seal.seenLog) < legacySeen {
		a.seal.seenLog = append(a.seal.seenLog, k)
	} else {
		delete(a.seal.seen, a.seal.seenLog[a.seal.seenPos])
		a.seal.seenLog[a.seal.seenPos] = k
		a.seal.seenPos = (a.seal.seenPos + 1) % legacySeen
	}
	a.seal.seen[k] = struct{}{}
	return false
}

// handshakeWithout notes a viewer handshake that did not ask for sealed
// frames: a viewer from before them, which reads only the earlier form.
func (a *Agent) handshakeWithout(frames int) {
	if frames < 1 && a.frameKeys() != nil {
		a.seal.legacyAt.Store(time.Now().UnixNano())
		a.seal.legacy.Store(true)
	}
}

// legacyWanted reports whether the earlier form is written now: while a
// viewer that proved the PIN with it is attached, or for legacyGrace after a
// handshake without sealed frames.
func (a *Agent) legacyWanted() bool {
	if !a.seal.legacy.Load() {
		return false
	}
	if a.seal.legacyProven.Load() {
		return true
	}
	if time.Since(time.Unix(0, a.seal.legacyAt.Load())) < legacyGrace {
		return true
	}
	a.seal.legacy.Store(false)
	return false
}

// admit decides whether an incoming message is acted on, and unwraps a
// sealed one into the message it carries. legacyNow reports that this message
// is the first from a viewer that predates sealed frames.
func (a *Agent) admit(msg protocol.Message) (out protocol.Message, ok, legacyNow bool) {
	k := a.frameKeys()
	if k == nil {
		return msg, msg.Type != protocol.TypeSealed, false
	}
	if msg.Type == protocol.TypeSealed {
		m, ok := a.openSealed(k, msg)
		return m, ok, false
	}
	if !protocol.Sealable(msg.Type) {
		return msg, true, false
	}
	// Unsealed session message, from a viewer that predates sealed frames.
	if msg.Data == "" {
		// A bare request for something to be sent back (a list, a status)
		// changes nothing and is answered. Anything else bare is accepted
		// only while such a viewer is attached.
		if protocol.ReadOnlyRequest(msg.Type) {
			return msg, true, false
		}
		return msg, a.legacyWanted(), false
	}
	// Accepted once, only if it decrypts, and never one the agent wrote.
	nonce, _, err := a.box.DecryptNonce(msg.Data)
	if err != nil || k.IsOwnNonce(nonce) {
		return msg, false, false
	}
	// Window acknowledgements are frequent and change nothing; leaving them
	// out keeps the record of everything else long.
	if msg.Type != protocol.TypeWindowAck && a.seenBefore(nonce) {
		return msg, false, false
	}
	// Encrypted with the session key: a viewer that has the PIN is attached.
	a.seal.legacyProven.Store(true)
	return msg, true, !a.seal.legacy.Swap(true)
}

// openSealed checks a viewer's sealed frame against its stream and opens it.
func (a *Agent) openSealed(k *crypto.FrameKeys, msg protocol.Message) (protocol.Message, bool) {
	if !protocol.Sealable(msg.Inner) {
		return msg, false
	}
	a.seal.mu.Lock()
	u, known := a.seal.streams[msg.Stream]
	var last uint64
	if known {
		last = u.last
	}
	a.seal.mu.Unlock()
	if !known || msg.Ctr <= last {
		return msg, false
	}
	kind, payload, err := k.Open(crypto.FromViewer, string(msg.Inner), msg.Stream, msg.Ctr, msg.Seq, msg.Data)
	if err != nil {
		return msg, false
	}
	a.seal.mu.Lock()
	u, known = a.seal.streams[msg.Stream]
	if !known || msg.Ctr <= u.last {
		a.seal.mu.Unlock()
		return msg, false
	}
	a.seal.tick++
	u.last, u.used = msg.Ctr, a.seal.tick
	src, first := u.src, !u.proven
	u.proven = true
	a.seal.mu.Unlock()
	// Opened on the stream its handshake was issued: whoever holds that
	// stream had the PIN (or is an owner), so its source is proven. Once per
	// stream is enough.
	if first {
		a.markProvenSource(src)
	}
	out := protocol.Message{Type: msg.Inner, Seq: msg.Seq, Stream: msg.Stream}
	switch kind {
	case crypto.FrameBoxed:
		// The handlers decrypt with the session key; hand them what they expect.
		enc, err := a.box.Encrypt(payload)
		if err != nil {
			return msg, false
		}
		out.Data = enc
	case crypto.FrameRaw:
		out.Data = string(payload)
	}
	return out, true
}

// lastViewerLeft ends writing both forms: the viewers that needed the
// unsealed one are gone, and the next one says which it needs.
func (a *Agent) lastViewerLeft() {
	a.seal.legacy.Store(false)
	a.seal.legacyProven.Store(false)
}
