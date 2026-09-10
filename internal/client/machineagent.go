// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/gorilla/websocket"

	"github.com/reminal/reminal/internal/config"
	"github.com/reminal/reminal/internal/crypto"
	"github.com/reminal/reminal/internal/protocol"
	"github.com/reminal/reminal/internal/session"
	"github.com/reminal/reminal/internal/updater"
)

// The machine-mode Agent: this machine's owner channel, served by the daemon.
//
// A session agent carries the rich surface — host stats, windows and their
// mirroring, apps, upgrades — but only while that session is open, and only
// to whoever holds its key. The machine channel used to be a separate, smaller
// program (dirHost) that could list, spawn, rename and kill sessions and
// nothing else. So "open an app on that machine" or "look at that window"
// needed a session to exist first, and the owner handshake was maintained
// twice.
//
// Now the channel IS an Agent, with no shell behind it: it registers at the
// same derived directory id with the same derived token, answers only to
// enrolled owner devices (the PIN handshake is refused at the reader), and
// handles everything a session agent handles minus the terminal, plus the
// directory messages. One implementation, and a machine is fully reachable
// with no session open at all.

// newMachineAgent builds the channel's Agent. Identity comes from the machine
// key rather than a minted session: the id is derived so every owner can find
// it without coordinating, and the token is derived so a restarted daemon
// presents the same credential the relay already holds.
func newMachineAgent(version string, daemonHost bool) (*Agent, error) {
	machineKey, err := loadOrCreateMachineKey()
	if err != nil {
		return nil, err
	}
	machinePub := machineKey.Public().(ed25519.PublicKey)
	sessionKey, err := crypto.NewSessionKey()
	if err != nil {
		return nil, err
	}
	box, err := crypto.NewBox(sessionKey)
	if err != nil {
		return nil, err
	}
	return &Agent{
		sessionID:      crypto.DeriveDirectoryID(machinePub),
		token:          crypto.DirectoryToken(machineKey),
		webURL:         config.WebURL(),
		version:        version,
		box:            box,
		sessionKey:     sessionKey,
		buf:            newScrollback(scrollbackBytes),
		hostEscape:     make(chan struct{}),
		pendingUploads: make(map[string]*pendingUpload),
		headless:       true,
		machine:        true,
		daemonHost:     daemonHost,
		dirLimits:      newDirLimits(),
		cwd:            currentCwd(),
	}, nil
}

// RunMachine serves the channel until stop closes. No shell is started, no
// active record is written (a machine is not a session and must not be listed
// as one), and nothing here touches the terminal paths — in particular no
// metaFlushLoop runs, so metaKick is left nil (its only sender, in setName, is
// nil-guarded) rather than allocating a channel nothing ever reads.
func (a *Agent) RunMachine(stop <-chan struct{}) error {
	a.startedAt = time.Now()
	return a.serveRelay(stop)
}

// notify prints a session's host-side notices. The machine channel has no
// host terminal to read them, and every Machines-panel open would otherwise
// log a connect/disconnect pair to the daemon's log.
func (a *Agent) notify(format string, args ...any) {
	if a.machine {
		return
	}
	agentNotify(format, args...)
}

// recordActive writes this agent's session record — for a session. The
// machine channel has no record: it is reached through the directory, not
// listed in it.
func (a *Agent) recordActive(viewers int) {
	if a.machine {
		return
	}
	_ = session.WriteActive(a.activeRecord(viewers))
}

// spawnSession starts a detached session on this host (see Spawn); a field
// override lets tests observe the call without forking a shell.
func (a *Agent) spawnSession(name, cwd string) (*SpawnedSession, error) {
	if a.spawn != nil {
		return a.spawn(name, cwd)
	}
	return Spawn(name, cwd)
}

// machineAccepts is what the machine channel will act on. Everything
// terminal-shaped — keystrokes, resizes, resumes, uploads, and the PIN
// handshake — is refused before the switch: there is no shell to deliver it
// to, and the channel answers only to enrolled owners.
var machineAccepts = map[protocol.MessageType]bool{
	protocol.TypeOwnerInit:     true,
	protocol.TypeDirQuery:      true,
	protocol.TypeDirRename:     true,
	protocol.TypeDirRevokeSelf: true,
	protocol.TypeDirKill:       true,
	protocol.TypeNewSession:    true,
	protocol.TypeHostInfo:      true,
	protocol.TypeChangelog:     true,
	protocol.TypeUpgrade:       true,
	protocol.TypeWindowList:    true,
	protocol.TypeWindowNoteAct: true,
	protocol.TypeWindowCtl:     true,
	protocol.TypeWindowInput:   true,
	protocol.TypeWindowAck:     true,
	protocol.TypeAppList:       true,
	protocol.TypeAppOpen:       true,
	protocol.TypeWebRTCHello:   true,
	protocol.TypeWebRTCAnswer:  true,
	protocol.TypeWebRTCICE:     true,
	protocol.TypeConnected:     true,
	// TypeClosed is as load-bearing as TypeConnected here: the machine channel
	// streams windows to owners, and the daemon serving it never exits. Drop a
	// viewer's disconnect and the "last viewer left" cleanup — stop the window
	// streams, drop RTC peers, release held input — never runs, so the
	// always-on daemon keeps capturing the screen into the void.
	protocol.TypeClosed: true,
	protocol.TypePing:   true,
	protocol.TypePong:   true,
}

// dirChannelOnly are the directory actions only the machine channel — an
// owner-authenticated Agent — may serve. A session agent shares its key with
// every PIN guest, so these are refused there (see runReader): a guest of one
// session must not be able to reach the machine-wide session registry to list,
// dump the transcript of, inject keys into, rename, or kill another session.
var dirChannelOnly = map[protocol.MessageType]bool{
	protocol.TypeDirQuery:      true,
	protocol.TypeDirRename:     true,
	protocol.TypeDirRevokeSelf: true,
	protocol.TypeDirKill:       true,
}

// servesOnThisChannel reports whether an incoming message of type t should be
// acted on by THIS agent, and is the single gate at the top of runReader's
// dispatch. The machine channel — owner-authenticated, no shell — takes only
// machineAccepts. A session agent takes everything a shell session should, i.e.
// everything EXCEPT the directory actions reserved to the machine channel
// (dirChannelOnly): those reach the machine's whole session registry and must
// never be exposed to a session's PIN guests.
func (a *Agent) servesOnThisChannel(t protocol.MessageType) bool {
	if a.machine {
		return machineAccepts[t]
	}
	return !dirChannelOnly[t]
}

// dirLimits bounds the machine channel's per-action rates. The channel is
// reachable by every enrolled owner device at once, and some actions fork a
// process, so a runaway client must not be able to turn that into a storm.
type dirLimits struct {
	hshake, query, spawn, rename *tokenBucket
}

func newDirLimits() *dirLimits {
	return &dirLimits{
		hshake: newTokenBucket(32, 16),
		query:  newTokenBucket(32, 16),
		spawn:  newTokenBucket(4, 1), // spawns fork a process — keep tight
		rename: newTokenBucket(16, 8),
	}
}

type dirAction int

const (
	dirActQuery dirAction = iota
	dirActSpawn
	dirActRename
)

// allowOwnerHandshake gates owner handshakes: the machine channel's wider
// bucket in machine mode, a session's PIN-guess bucket otherwise.
func (a *Agent) allowOwnerHandshake() bool {
	if a.dirLimits != nil {
		return a.dirLimits.hshake.allow(time.Now())
	}
	return a.allowKex(time.Now())
}

// allowDir applies the machine channel's limits. A session agent (no limits
// object) answers freely: its viewers hold its key, which is the gate there.
func (a *Agent) allowDir(act dirAction) bool {
	if a.dirLimits == nil {
		return true
	}
	now := time.Now()
	switch act {
	case dirActQuery:
		return a.dirLimits.query.allow(now)
	case dirActSpawn:
		return a.dirLimits.spawn.allow(now)
	default:
		return a.dirLimits.rename.allow(now)
	}
}

// handleDirQuery answers "what is on this machine": hostname, live sessions,
// battery, and — on request — a scrollback search, a transcript, or keys
// typed into a session.
func (a *Agent) handleDirQuery(conn *websocket.Conn, data string) {
	if !a.allowDir(dirActQuery) {
		return
	}
	resp := LocalDirectory()
	if resp.Stats != nil {
		resp.Stats.Version, resp.Stats.Update = a.version, updater.Available(a.version)
	}
	q := parseDirQuery(a.box, data)
	if q.Pattern != "" {
		applyLocalSearchHits(&resp, q.Pattern)
	}
	if q.Transcript != "" {
		applyLocalTranscriptDump(&resp, q.Transcript)
	}
	if q.KeysID != "" && q.Keys != "" {
		applyLocalKeys(&resp, q.KeysID, q.Keys)
	}
	a.sendWindowMsg(conn, protocol.TypeDirResp, resp)
}

// dirAck is the reply shape shared by the directory's mutating actions.
type dirAck struct {
	ReqID string `json:"req_id,omitempty"`
	OK    bool   `json:"ok,omitempty"`
	Error string `json:"error,omitempty"`
}

// decryptDir opens a directory request under the channel key. Anything that
// does not decrypt is dropped without a reply: the relay can see the message
// go by but must not be able to author one.
func (a *Agent) decryptDir(data string, into any) bool {
	if a.box == nil || data == "" {
		return false
	}
	pt, err := a.box.Decrypt(data)
	if err != nil {
		return false
	}
	return json.Unmarshal(pt, into) == nil
}

func (a *Agent) handleDirRename(conn *websocket.Conn, data string) {
	if !a.allowDir(dirActRename) {
		return
	}
	var req struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		ReqID string `json:"req_id"`
	}
	if !a.decryptDir(data, &req) {
		return
	}
	ack := dirAck{ReqID: req.ReqID}
	if err := renameLocalSession(req.ID, req.Name); err != nil {
		ack.Error = err.Error()
	} else {
		ack.OK = true
	}
	a.sendWindowMsg(conn, protocol.TypeDirRename, ack)
}

func (a *Agent) handleDirRevokeSelf(conn *websocket.Conn, data string) {
	if !a.allowDir(dirActRename) {
		return
	}
	var req struct {
		DevicePub string `json:"device_pub"`
		Sig       string `json:"sig"`
		ReqID     string `json:"req_id"`
	}
	if !a.decryptDir(data, &req) {
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
	ack := dirAck{ReqID: req.ReqID}
	machinePub, err := MachinePub()
	if err != nil || machinePub == nil {
		// Without the machine key the transcript is built wrong and a correctly
		// signed request would be rejected as forged. Say what actually failed
		// rather than the misleading "signature invalid".
		ack.Error = "cannot read machine key"
		a.sendWindowMsg(conn, protocol.TypeDirRevokeSelf, ack)
		return
	}
	if !crypto.VerifyOwner(ed25519.PublicKey(devicePub), crypto.RevokeSelfTranscript(machinePub, devicePub), sig) {
		ack.Error = "signature invalid"
	} else if err := RevokeSelf(ed25519.PublicKey(devicePub)); err != nil {
		ack.Error = err.Error()
	} else {
		ack.OK = true
	}
	a.sendWindowMsg(conn, protocol.TypeDirRevokeSelf, ack)
}

func (a *Agent) handleDirKill(conn *websocket.Conn, data string) {
	if !a.allowDir(dirActRename) {
		return
	}
	var req struct {
		ID    string `json:"id"`
		ReqID string `json:"req_id"`
	}
	if !a.decryptDir(data, &req) {
		return
	}
	ack := dirAck{ReqID: req.ReqID}
	if err := killLocalSession(req.ID); err != nil {
		ack.Error = err.Error()
	} else {
		ack.OK = true
	}
	a.sendWindowMsg(conn, protocol.TypeDirKill, ack)
}
