// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/reminal/reminal/internal/config"
	"github.com/reminal/reminal/internal/crypto"
	"github.com/reminal/reminal/internal/proc"
	"github.com/reminal/reminal/internal/protocol"
	"github.com/reminal/reminal/internal/session"
)

// The directory host is a machine's presence responder for `reminal machines`.
// It registers the machine's owner-derived directory channel on the relay and,
// once an owner proves ownership there with the same own_init/own_resp handshake
// used for a PIN-free connect, answers "what sessions are you running?" with an
// end-to-end-encrypted list — so the relay never learns what's running, and only
// an enrolled owner can read the reply.
//
// Every agent runs one host goroutine, but exactly one serves per machine: a
// machine-local flock (see dirhostlock.go) elects the single host, and the
// others stand by and take over promptly when it exits. We do NOT lean on the
// relay's own election, because it differs by relay — the production relay
// supersedes a same-credential agent while the local relay rejects it, so
// without the lock sibling sessions would ping-pong the channel and the machine
// would flap online/offline. Whichever session holds the lock answers from the
// shared local session registry (the same one `reminal list` reads), so it
// doesn't matter which won. A machine with no owners never opens the channel.

const (
	// With single-host locking there's no sibling to fight, so a dropped
	// connection is always a genuine network blip — reclaim fast. Cap the backoff
	// low (not tens of seconds): after a wake-from-sleep or a network flap we want
	// the machine's presence back within seconds, so the phone stops showing it
	// gray. A failed dial is a cheap TCP attempt, so polling every few seconds
	// while the network is still down costs little.
	dirHostRetry    = 2 * time.Second
	dirHostRetryMax = 8 * time.Second
	// How often a standing-by agent re-checks to take over the lock, and how
	// often an unowned machine re-checks for newly-enrolled owners.
	dirHostLockPoll     = 3 * time.Second
	dirHostOwnerRecheck = 60 * time.Second
)

// runDirectoryHost keeps this machine's directory channel served for as long as
// stop is open. Safe to run on every agent; a no-op while unowned. isDaemon is
// true only for the standalone background-host daemon (see RunDaemon); false for
// session-embedded hosts.
func runDirectoryHost(stop <-chan struct{}, isDaemon bool) {
	for {
		if stopped(stop) {
			return
		}
		// When the always-on background host (daemon) is installed, IT is the
		// canonical directory host — sessions must not compete for the single-host
		// lock. Otherwise a session that grabbed the lock and later stopped serving
		// (e.g. it survived a logout with a dead relay connection) would hold the
		// lock while the daemon stands by — nobody serves and the machine shows
		// offline. Deferring here removes that deadlock: only the daemon serves when
		// it's installed; sessions serve only as a fallback when there's no daemon.
		if !isDaemon && DaemonServiceInstalled() {
			// Deferring only makes sense if the daemon actually EXISTS. On
			// Windows nothing supervises it (the Run key fires at logon
			// only), so a killed daemon plus this deferral would leave the
			// machine offline in `reminal machines` forever — resurrect it
			// instead. No-op on launchd/systemd platforms, whose service
			// managers own the daemon's lifecycle.
			if !daemonAlive() {
				if exe, err := os.Executable(); err == nil {
					respawnDaemonAfterUpgrade(exe)
				}
			}
			if sleepOrStop(stop, dirHostOwnerRecheck) {
				return
			}
			continue
		}
		// Nothing to serve until someone owns this machine. Re-check periodically
		// so enrolling an owner after the agent started still lights the channel up.
		if of, err := loadOwners(); err != nil || of == nil || len(of.Owners) == 0 {
			if sleepOrStop(stop, dirHostOwnerRecheck) {
				return
			}
			continue
		}
		// Become this machine's sole directory host, or stand by. Polling (rather
		// than a blocking lock) lets us keep re-checking owners and honour stop.
		lock, ok := tryLockDirHost()
		if !ok {
			if sleepOrStop(stop, dirHostLockPoll) {
				return
			}
			continue
		}
		serveDirectoryLocked(stop, isDaemon)
		unlockDirHost(lock)
	}
}

// serveDirectoryLocked serves the directory channel while this agent holds the
// single-host lock, reconnecting quickly after a dropped relay connection (no
// sibling can steal the channel, so a drop is always a genuine blip). It returns
// — releasing the lock to a standing-by sibling — when stop fires or this
// machine stops being owned.
func serveDirectoryLocked(stop <-chan struct{}, isDaemon bool) {
	retry := dirHostRetry
	for {
		if stopped(stop) {
			return
		}
		// A daemon was installed while we (a session) held the lock — yield it so
		// the daemon takes over as the canonical host.
		if !isDaemon && DaemonServiceInstalled() {
			return
		}
		if of, err := loadOwners(); err != nil || of == nil || len(of.Owners) == 0 {
			return // every owner revoked — stop hosting and release the lock
		}
		machineKey, err := loadOrCreateMachineKey()
		if err != nil {
			if sleepOrStop(stop, retry) {
				return
			}
			continue
		}
		served := serveDirectoryOnce(stop, machineKey)
		if served {
			retry = dirHostRetry // we held the channel; reset backoff
		} else {
			retry *= 2
			if retry > dirHostRetryMax {
				retry = dirHostRetryMax
			}
		}
		if sleepOrStop(stop, retry) {
			return
		}
	}
}

// serveDirectoryOnce dials the channel, wins-or-loses the election, and (if it
// wins) serves until the connection drops or stop fires. Returns whether it
// actually became the host, so the caller can distinguish "lost the election"
// (back off longer) from "was hosting, then disconnected" (retry promptly).
// safeGoDir runs a directory-message handler in a goroutine, recovering panics so
// an untrusted relay message can't crash the always-on daemon. The handlers are
// spawned (not synchronous) so serveDirectoryOnce's recover wouldn't catch them.
func safeGoDir(f func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				recoverLog("safeGoDir", r)
			}
		}()
		f()
	}()
}

func serveDirectoryOnce(stop <-chan struct{}, machineKey ed25519.PrivateKey) (served bool) {
	// The dispatch loop below processes UNTRUSTED relay input (owner-connect,
	// dir-query). A panic in a synchronous handler must NOT crash the always-on
	// daemon — recover it so the directory host just drops the connection and
	// reconnects. (Spawned dh.handleDir* goroutines recover via safeGoDir.)
	defer func() { _ = recover() }()
	machinePub := machineKey.Public().(ed25519.PublicKey)
	dh := &dirHost{
		machineKey:  machineKey,
		machinePub:  machinePub,
		dirID:       crypto.DeriveDirectoryID(machinePub),
		hshakeLimit: newTokenBucket(32, 16), // burst 32, 16/s sustained
		queryLimit:  newTokenBucket(32, 16),
		spawnLimit:  newTokenBucket(4, 1), // spawns fork a process — keep tight
		renameLimit: newTokenBucket(16, 8),
		spawn:       Spawn,
	}

	wsURL := config.SessionWS(dh.dirID, string(protocol.RoleAgent))
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		return false
	}
	defer conn.Close()
	conn.SetReadLimit(maxDirMessageBytes) // untrusted relay — cap frame size
	dh.conn = conn

	// Authenticate with the stable, machine-key-derived directory token so a
	// reconnect (or a sibling taking over) matches whatever the relay stored.
	if err := dh.write(protocol.Message{Type: protocol.TypeAuth, Token: crypto.DirectoryToken(machineKey)}); err != nil {
		return false
	}
	_, raw, err := conn.ReadMessage()
	if err != nil {
		return false
	}
	var m protocol.Message
	if json.Unmarshal(raw, &m) != nil || m.Type != protocol.TypeAuthOK {
		// Almost always "another agent already connected" — another session on
		// this machine holds the channel. Not an error; just retry later.
		return false
	}

	// One session key shared by every owner viewer, exactly like an agent's own
	// per-session key: the reply is broadcast and any enrolled owner can read it.
	sessionKey, err := crypto.NewSessionKey()
	if err != nil {
		return true
	}
	box, err := crypto.NewBox(sessionKey)
	if err != nil {
		return true
	}
	dh.sessionKey = sessionKey
	dh.box = box

	// Close the conn when stop fires so the read loop unblocks.
	readerStop := make(chan struct{})
	defer close(readerStop)
	go func() {
		select {
		case <-stop:
			conn.Close()
		case <-readerStop:
		}
	}()
	go dh.pingLoop(readerStop)

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return true
		}
		var msg protocol.Message
		if json.Unmarshal(raw, &msg) != nil {
			continue
		}
		switch msg.Type {
		case protocol.TypeOwnerInit:
			dh.handleOwnerInit(msg)
		case protocol.TypeDirQuery:
			dh.handleDirQuery(msg)
		case protocol.TypeNewSession:
			safeGoDir(func() { dh.handleNewSession(msg) })
		case protocol.TypeDirRename:
			safeGoDir(func() { dh.handleDirRename(msg) })
		case protocol.TypeDirRevokeSelf:
			safeGoDir(func() { dh.handleDirRevokeSelf(msg) })
		case protocol.TypeDirKill:
			safeGoDir(func() { dh.handleDirKill(msg) })
		case protocol.TypePing:
			_ = dh.write(protocol.Message{Type: protocol.TypePong})
		}
	}
}

type dirHost struct {
	machineKey ed25519.PrivateKey
	machinePub ed25519.PublicKey
	dirID      string

	sessionKey []byte
	box        *crypto.Box

	writeMu sync.Mutex
	conn    *websocket.Conn

	hshakeLimit *tokenBucket // owner handshakes
	queryLimit  *tokenBucket // dir_query
	spawnLimit  *tokenBucket // new_session (expensive — spawns a process)
	renameLimit *tokenBucket // dir_rename

	spawn func(name, cwd string) (*SpawnedSession, error) // Spawn; injectable for tests
}

// tokenBucket is a simple refilling rate limiter. Unlike a fixed min-interval
// gate, its burst lets several legitimate owners act at once (each of N
// concurrent queries takes a token) while a sustained flood is still capped at
// the refill rate.
type tokenBucket struct {
	mu     sync.Mutex
	tokens float64
	max    float64
	perSec float64
	last   time.Time
	inited bool
}

func newTokenBucket(max, perSec float64) *tokenBucket {
	return &tokenBucket{tokens: max, max: max, perSec: perSec}
}

func (tb *tokenBucket) allow(now time.Time) bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	if !tb.inited {
		tb.last = now
		tb.inited = true
	}
	tb.tokens += now.Sub(tb.last).Seconds() * tb.perSec
	if tb.tokens > tb.max {
		tb.tokens = tb.max
	}
	tb.last = now
	if tb.tokens >= 1 {
		tb.tokens--
		return true
	}
	return false
}

func (dh *dirHost) write(msg protocol.Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	dh.writeMu.Lock()
	defer dh.writeMu.Unlock()
	if dh.conn == nil {
		return errors.New("not connected")
	}
	_ = dh.conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
	return dh.conn.WriteMessage(websocket.TextMessage, data)
}

func (dh *dirHost) pingLoop(stop <-chan struct{}) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			if err := dh.write(protocol.Message{Type: protocol.TypePing}); err != nil {
				return
			}
		}
	}
}

// handleOwnerInit runs the server half of the PIN-free handshake on the
// directory channel — identical to the agent's, but the transcript binds the
// directory id and the wrapped key is this channel's shared key. A forged
// signature or an unenrolled device simply gets no reply.
func (dh *dirHost) handleOwnerInit(msg protocol.Message) {
	if !dh.hshakeLimit.allow(time.Now()) {
		return
	}
	exID, err := crypto.ParseExID(msg.ExID)
	if err != nil {
		return
	}
	viewerEph, err := base64.StdEncoding.DecodeString(msg.Data)
	if err != nil || len(viewerEph) != crypto.PubKeyBytes {
		return
	}
	devicePub, err := base64.StdEncoding.DecodeString(msg.DevicePub)
	if err != nil || len(devicePub) != ed25519.PublicKeySize {
		return
	}
	deviceSig, err := base64.StdEncoding.DecodeString(msg.DeviceSig)
	if err != nil {
		return
	}
	if !crypto.VerifyOwner(devicePub, crypto.OwnerClientTranscript(dh.dirID, viewerEph, devicePub), deviceSig) {
		return
	}
	if ok, err := IsOwner(devicePub); err != nil || !ok {
		return
	}
	peerKey, err := crypto.PeerPublicKey(viewerEph)
	if err != nil {
		return
	}
	eph, err := crypto.NewEphemeralKey()
	if err != nil {
		return
	}
	shared, err := eph.ECDH(peerKey)
	if err != nil {
		return
	}
	agentEph := eph.PublicKey().Bytes()
	machineSig := crypto.SignOwner(dh.machineKey, crypto.OwnerServerTranscript(dh.dirID, viewerEph, agentEph, devicePub, dh.machinePub))
	wrapped, err := crypto.WrapSessionKey(shared, exID, dh.sessionKey)
	if err != nil {
		return
	}
	_ = dh.write(protocol.Message{
		Type:       protocol.TypeOwnerResp,
		ExID:       msg.ExID,
		Data:       base64.StdEncoding.EncodeToString(agentEph),
		MachinePub: base64.StdEncoding.EncodeToString(dh.machinePub),
		MachineSig: base64.StdEncoding.EncodeToString(machineSig),
		Wrap:       base64.StdEncoding.EncodeToString(wrapped),
	})
}

// handleDirQuery replies with the machine's live sessions, encrypted under the
// channel key. The reply is broadcast; only owners who completed the handshake
// hold the key, so a party that merely knows the channel id learns nothing. Rate
// limited so a spammer can't turn the host into an amplifier.
func (dh *dirHost) handleDirQuery(msg protocol.Message) {
	if !dh.queryLimit.allow(time.Now()) {
		return
	}
	resp := protocol.DirResponse{Sessions: dh.localSessions()}
	if host, err := os.Hostname(); err == nil {
		resp.Hostname = host
	}
	// Optional encrypted payload. Older hosts ignore Data and still list.
	q := parseDirQuery(dh.box, msg.Data)
	if q.Pattern != "" {
		applyLocalSearchHits(&resp, q.Pattern)
	}
	if q.Transcript != "" {
		applyLocalTranscriptDump(&resp, q.Transcript)
	}
	if q.KeysID != "" && q.Keys != "" {
		applyLocalKeys(&resp, q.KeysID, q.Keys)
	}
	payload, err := json.Marshal(resp)
	if err != nil {
		return
	}
	enc, err := dh.box.Encrypt(payload)
	if err != nil {
		return
	}
	_ = dh.write(protocol.Message{Type: protocol.TypeDirResp, Data: enc})
}

// handleNewSession spawns a fresh session on this machine on an owner's request
// (the per-machine "+" in the web Machines panel) and returns its credentials,
// encrypted under the channel key. Same spawn the in-session "new session"
// button uses — no privilege escalation: an owner already has full access. Rate
// limited so a stuck client can't fork-bomb the host. A req_id is echoed so the
// requesting owner can match the reply among the channel's broadcasts.
func (dh *dirHost) handleNewSession(msg protocol.Message) {
	if !dh.spawnLimit.allow(time.Now()) {
		return
	}
	// Spawning is a real side effect, so it MUST be gated on owner auth. The only
	// party holding this channel's key is one that completed the own_init/own_resp
	// handshake (the key is wrapped only after VerifyOwner + IsOwner), so requiring
	// the request to decrypt is exactly that gate: a non-owner — including a relay
	// that merely knows the channel id — produces ciphertext that fails the GCM tag
	// and gets nothing. Do NOT spawn on an empty or undecryptable request.
	if msg.Data == "" {
		return
	}
	pt, err := dh.box.Decrypt(msg.Data)
	if err != nil {
		return
	}
	var req struct {
		Name  string `json:"name"`
		Cwd   string `json:"cwd"`
		ReqID string `json:"req_id"`
	}
	_ = json.Unmarshal(pt, &req)
	var payload struct {
		ReqID string `json:"req_id,omitempty"`
		ID    string `json:"id,omitempty"`
		PIN   string `json:"pin,omitempty"`
		Error string `json:"error,omitempty"`
	}
	payload.ReqID = req.ReqID
	if sp, err := dh.spawn(req.Name, req.Cwd); err != nil {
		payload.Error = err.Error()
	} else {
		payload.ID, payload.PIN = sp.ID, sp.PIN
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	enc, err := dh.box.Encrypt(raw)
	if err != nil {
		return
	}
	_ = dh.write(protocol.Message{Type: protocol.TypeNewSession, Data: enc})
}

// handleDirRename renames one of this machine's live sessions on an owner's
// request (the ✎ on a Machines-panel session). Owner-gated exactly like spawn:
// only a holder of the channel key can produce a decryptable request. Drives the
// target session's local control socket — the same path `reminal rename` uses —
// so the new name persists and propagates everywhere.
func (dh *dirHost) handleDirRename(msg protocol.Message) {
	if !dh.renameLimit.allow(time.Now()) {
		return
	}
	if msg.Data == "" {
		return
	}
	pt, err := dh.box.Decrypt(msg.Data)
	if err != nil {
		return
	}
	var req struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		ReqID string `json:"req_id"`
	}
	if json.Unmarshal(pt, &req) != nil {
		return
	}
	var payload struct {
		ReqID string `json:"req_id,omitempty"`
		OK    bool   `json:"ok,omitempty"`
		Error string `json:"error,omitempty"`
	}
	payload.ReqID = req.ReqID
	if err := renameLocalSession(req.ID, req.Name); err != nil {
		payload.Error = err.Error()
	} else {
		payload.OK = true
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	enc, err := dh.box.Encrypt(raw)
	if err != nil {
		return
	}
	_ = dh.write(protocol.Message{Type: protocol.TypeDirRename, Data: enc})
}

// handleDirRevokeSelf tombstones the SENDING device's ownership of this machine
// (the Machines panel's ✕ → "revoke my access"). Self-only and safe without
// sudo: the request must be signed by the very device it names (bound to this
// machine), so an owner can only lock ITSELF out, never another. Idempotent.
func (dh *dirHost) handleDirRevokeSelf(msg protocol.Message) {
	if !dh.renameLimit.allow(time.Now()) {
		return
	}
	if msg.Data == "" {
		return
	}
	pt, err := dh.box.Decrypt(msg.Data)
	if err != nil {
		return
	}
	var req struct {
		DevicePub string `json:"device_pub"`
		Sig       string `json:"sig"`
		ReqID     string `json:"req_id"`
	}
	if json.Unmarshal(pt, &req) != nil {
		return
	}
	devicePub, err := base64.StdEncoding.DecodeString(req.DevicePub)
	if err != nil || len(devicePub) != ed25519.PublicKeySize {
		return
	}
	sig, err := base64.StdEncoding.DecodeString(req.Sig)
	if err != nil {
		return
	}
	var payload struct {
		ReqID string `json:"req_id,omitempty"`
		OK    bool   `json:"ok,omitempty"`
		Error string `json:"error,omitempty"`
	}
	payload.ReqID = req.ReqID
	if !crypto.VerifyOwner(ed25519.PublicKey(devicePub), crypto.RevokeSelfTranscript(dh.machinePub, devicePub), sig) {
		payload.Error = "signature invalid"
	} else if err := RevokeSelf(ed25519.PublicKey(devicePub)); err != nil {
		payload.Error = err.Error()
	} else {
		payload.OK = true
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	enc, err := dh.box.Encrypt(raw)
	if err != nil {
		return
	}
	_ = dh.write(protocol.Message{Type: protocol.TypeDirRevokeSelf, Data: enc})
}

// handleDirKill terminates one of the machine's live sessions on an owner's
// request (the Machines panel's kill action). Owner-gated like rename/spawn —
// only a holder of the channel key can produce a decryptable request.
func (dh *dirHost) handleDirKill(msg protocol.Message) {
	if !dh.renameLimit.allow(time.Now()) {
		return
	}
	if msg.Data == "" {
		return
	}
	pt, err := dh.box.Decrypt(msg.Data)
	if err != nil {
		return
	}
	var req struct {
		ID    string `json:"id"`
		ReqID string `json:"req_id"`
	}
	if json.Unmarshal(pt, &req) != nil {
		return
	}
	var payload struct {
		ReqID string `json:"req_id,omitempty"`
		OK    bool   `json:"ok,omitempty"`
		Error string `json:"error,omitempty"`
	}
	payload.ReqID = req.ReqID
	if err := killLocalSession(req.ID); err != nil {
		payload.Error = err.Error()
	} else {
		payload.OK = true
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	enc, err := dh.box.Encrypt(raw)
	if err != nil {
		return
	}
	_ = dh.write(protocol.Message{Type: protocol.TypeDirKill, Data: enc})
}

// killLocalSession terminates a live local session by id: SIGTERM now, escalate
// to SIGKILL in the background if it lingers, and drop its active record. Same
// termination `reminal kill` uses. Returns after the SIGTERM so the reply is
// prompt even when the caller is killing the session hosting this directory.
func killLocalSession(id string) error {
	id = strings.ToUpper(strings.TrimSpace(id))
	all, err := session.ReadAllActive()
	if err != nil {
		return err
	}
	for _, a := range all {
		if a.ID != id {
			continue
		}
		pid := a.PID
		if err := proc.Terminate(pid); err != nil {
			if errors.Is(err, proc.ErrGone) {
				_ = session.ClearActive(a.ID) // already gone
				return nil
			}
			return fmt.Errorf("terminate %d: %w", pid, err)
		}
		// Drop the registry entry immediately so `reminal list` and the Machines
		// panel stop showing it the moment the kill is acknowledged — not after
		// the process finishes dying (which left a killed session lingering in
		// the list until the next refresh). The goroutine below still escalates
		// to SIGKILL if the shell ignores SIGTERM.
		_ = session.ClearActive(a.ID)
		go func(pid int) {
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				if !proc.Alive(pid) {
					return
				}
				time.Sleep(100 * time.Millisecond)
			}
			if proc.Alive(pid) {
				_ = proc.Kill(pid)
			}
		}(pid)
		return nil
	}
	return fmt.Errorf("session not found")
}

// renameLocalSession finds a live shell session by id and asks its agent to
// rename itself via the control socket.
func renameLocalSession(id, name string) error {
	id = strings.ToUpper(strings.TrimSpace(id))
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("name is empty")
	}
	all, err := session.ReadAllActive()
	if err != nil {
		return err
	}
	for _, a := range all {
		if a.ID == id {
			if a.IsPort() {
				return fmt.Errorf("can't rename a port forward")
			}
			_, err := sendControlTo(a.PID, "rename "+name)
			return err
		}
	}
	return fmt.Errorf("session not found")
}

// localSessions projects the local session registry to the wire DTO, dropping
// the PIN (owners reach sessions without it) and anything sensitive.
func (dh *dirHost) localSessions() []protocol.DirSession {
	return localDirSessions()
}

// LocalDirectory returns THIS machine's directory response — hostname + live
// sessions — read straight from the local registry, with no relay round-trip
// and no ownership handshake. `reminal machines` uses it for the machine it's
// running on: you always own the machine you're sitting at, so it should show up
// instantly and never depend on being enrolled as an owner of yourself.
func LocalDirectory() protocol.DirResponse {
	resp := protocol.DirResponse{Sessions: localDirSessions()}
	if host, err := os.Hostname(); err == nil {
		resp.Hostname = host
	}
	return resp
}

// localDirSessions is the shared projection used by both the directory host (for
// remote owners) and LocalDirectory (for the local CLI).
func localDirSessions() []protocol.DirSession {
	all, err := session.ReadAllActive()
	if err != nil {
		return nil
	}
	now := time.Now()
	out := make([]protocol.DirSession, 0, len(all))
	for _, a := range all {
		ds := protocol.DirSession{
			ID:       a.ID,
			Name:     a.Name,
			Cwd:      a.Cwd,
			Title:    a.Title,
			Kind:     a.Kind,
			Port:     a.Port,
			Headless: a.Headless,
			Viewers:  a.Viewers,
		}
		if la := a.LastActive(); !la.IsZero() {
			if secs := int64(now.Sub(la).Seconds()); secs > 0 {
				ds.IdleSecs = secs
			}
		}
		out = append(out, ds)
	}
	return out
}

func stopped(stop <-chan struct{}) bool {
	select {
	case <-stop:
		return true
	default:
		return false
	}
}

// sleepOrStop waits for d or until stop fires; returns true if stop fired.
func sleepOrStop(stop <-chan struct{}, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-stop:
		return true
	case <-t.C:
		return false
	}
}
