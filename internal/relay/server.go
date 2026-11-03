// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package relay

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"reminal/internal/protocol"
)

// orphanTTL is how long a room is kept alive after the agent disconnects,
// giving the same agent a chance to reattach (e.g., across a network blip).
const orphanTTL = 10 * time.Minute

// ownerKeep is how long a session ID stays reserved for the agent that had it
// once its room has been cleared: only the same credential may claim it in
// that window (a reboot, `reminal restore`). Kept as a hash. Matches the
// Worker's OWNER_KEEP_MS.
const ownerKeep = 30 * 24 * time.Hour

// roomOwner is what is kept of a cleared room.
type roomOwner struct {
	hash  string
	until time.Time
}

// wsWriteWait bounds a single WS write to a peer. Without it, a peer that stops
// reading (its send buffer fills) makes WriteMessage block forever while holding
// writeMu, leaking the forwarding goroutine and stalling that peer's queue. The
// deadline frees the goroutine and drops the frame; a corrupt conn then fails
// fast on subsequent writes.
const wsWriteWait = 30 * time.Second

// Read deadlines bound how long a connection may sit silent before we drop it and
// reclaim its goroutine. Without them a peer that connects and never speaks — never
// even authenticating — leaks a goroutine forever (slow-loris). authWait is a tight
// window to send the first Auth/Register frame; readWait is the steady-state
// liveness budget once attached, comfortably above the clients' 30s ping cadence.
const (
	defaultAuthWait = 20 * time.Second
	defaultReadWait = 90 * time.Second
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

type peer struct {
	conn   *websocket.Conn
	role   protocol.Role
	authed bool
	// parked: a viewer's warm socket for a session it has switched away
	// from (protocol.TypePark). It gets nothing of the agent's and is not
	// counted as a viewer.
	parked  bool
	writeMu sync.Mutex
}

type room struct {
	agent *peer
	// pendingAgents are agent connections that have not authenticated yet.
	// Any number may wait; the first to prove the credential becomes r.agent
	// and the rest are told they were superseded. A connection that never
	// authenticates therefore never keeps the real agent out.
	pendingAgents []*peer
	viewers       []*peer
	auth          authState
	cleanup       *time.Timer
	mu            sync.Mutex
}

type Server struct {
	rooms map[string]*room
	// owners reserves cleared rooms' session IDs for their agents (ownerKeep).
	owners map[string]roomOwner
	mu     sync.RWMutex
	// Read-deadline windows; fields (not globals) so a test can set them on its
	// own Server before serving without racing the handler goroutines. See
	// defaultAuthWait/defaultReadWait.
	authWait time.Duration
	readWait time.Duration
	// handshakes counts PIN handshakes per session and client address
	// (handshakeAllowed).
	hsMu       sync.Mutex
	handshakes map[string]*addrBucket
	hsPruned   time.Time
	// Room lifetimes; fields so a test can shorten them on its own Server.
	// Zero means the default (orphanTTL, ownerKeep).
	orphanWait time.Duration
	ownerWait  time.Duration
}

func (s *Server) orphanTTL() time.Duration {
	if s.orphanWait > 0 {
		return s.orphanWait
	}
	return orphanTTL
}

func (s *Server) ownerKeep() time.Duration {
	if s.ownerWait > 0 {
		return s.ownerWait
	}
	return ownerKeep
}

func NewServer() *Server {
	return &Server{
		rooms:    make(map[string]*room),
		authWait: defaultAuthWait,
		readWait: defaultReadWait,
	}
}

func (s *Server) HandleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("upgrade: %v", err)
		return
	}
	conn.SetReadLimit(1 << 20)
	noteClientAddr(conn, r)
	go s.handleLegacyConn(conn)
}

func (s *Server) HandleSessionWS(w http.ResponseWriter, r *http.Request, sessionID, role string) {
	sessionID = strings.ToUpper(strings.TrimSpace(sessionID))
	peerRole := protocol.Role(strings.ToLower(strings.TrimSpace(role)))

	if peerRole != protocol.RoleAgent && peerRole != protocol.RoleViewer {
		http.Error(w, "invalid role", http.StatusBadRequest)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("upgrade: %v", err)
		return
	}
	conn.SetReadLimit(1 << 20)
	noteClientAddr(conn, r)

	if ok, reason := s.attach(sessionID, peerRole, conn); !ok {
		forgetClientAddr(conn)
		s.sendError(conn, reason)
		conn.Close()
		return
	}

	go s.handleSessionConn(sessionID, peerRole, conn)
}

// forwardableTypes is the single source of truth for which message types the
// relay blindly relays between an agent and its viewers. BOTH connection
// handlers (handleSessionConn and the legacy handleLegacyConn) consult it, so a
// new agent↔viewer type is forwarded on every path the moment it's added here —
// no more silently maintaining two lists that drift (an omission drops the type
// only on this local relay; the production Worker forwards opaquely, so such bugs
// hide until someone tests locally). Everything here is ciphertext or already
// end-to-end authenticated; the relay never inspects the contents.
var forwardableTypes = map[protocol.MessageType]bool{
	protocol.TypeData: true, protocol.TypeResize: true, protocol.TypeResume: true,
	protocol.TypeSnapshotPart: true, protocol.TypeSnapshotAck: true,
	protocol.TypeKexInit: true, protocol.TypeKexResp: true,
	protocol.TypePakeInit: true, protocol.TypePakeResp: true, protocol.TypePakeBusy: true,
	protocol.TypeSealed:    true,
	protocol.TypeOwnerInit: true, protocol.TypeOwnerResp: true, protocol.TypeOwnerBusy: true,
	protocol.TypeDirQuery: true, protocol.TypeDirResp: true, protocol.TypeDirRename: true,
	protocol.TypeDirRevokeSelf: true, protocol.TypeDirKill: true, protocol.TypeDirSleep: true, protocol.TypeDirIntegrate: true,
	protocol.TypeWindowList: true, protocol.TypeWindowCtl: true,
	protocol.TypeWindowFrame: true, protocol.TypeWindowInput: true, protocol.TypeWindowAck: true,
	protocol.TypeWindowClose: true, protocol.TypeWindowAX: true,
	protocol.TypeAppList: true, protocol.TypeAppOpen: true,
	protocol.TypeHostInfo: true, protocol.TypeNewSession: true,
	// File transfer and notices ride the session socket too: `reminal send`
	// and `reminal notify` push to viewers, a viewer uploads back, and the
	// host acknowledges. Missing here, all four did nothing at all on this
	// relay while working through the hosted one, which inspects nothing and
	// so cannot leave a type behind.
	protocol.TypeNotify: true, protocol.TypeDownload: true,
	protocol.TypeWindowNotes: true, protocol.TypeWindowNoteAct: true,
	protocol.TypeUpload: true, protocol.TypeUploadAck: true,
	protocol.TypeChangelog: true, protocol.TypeUpgrade: true, protocol.TypeSwitchChannel: true,
	protocol.TypeWebRTCHello: true, protocol.TypeWebRTCOffer: true,
	protocol.TypeWebRTCAnswer: true, protocol.TypeWebRTCICE: true,
}

func (s *Server) handleSessionConn(sessionID string, role protocol.Role, conn *websocket.Conn) {
	// Per-connection goroutine processing untrusted peer messages. A panic here
	// must NOT crash the relay — that would disconnect every other session. Recover
	// so only this connection drops (its deferred conn.Close/detach still run).
	defer func() { _ = recover() }()
	defer conn.Close()
	defer forgetClientAddr(conn)
	defer s.detach(sessionID, role, conn)

	authed := false
	for {
		wait := s.authWait
		if authed {
			wait = s.readWait
		}
		_ = conn.SetReadDeadline(time.Now().Add(wait))
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}

		var msg protocol.Message
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}

		r := s.getRoom(sessionID)
		if r == nil {
			return
		}

		r.mu.Lock()
		p := r.peerByConn(role, conn)
		if p == nil {
			// our peer slot was taken over or cleared; bail
			r.mu.Unlock()
			return
		}
		if !p.authed {
			if msg.Type != protocol.TypeAuth {
				r.mu.Unlock()
				s.sendError(conn, "authentication required")
				return
			}
			if errMsg := s.handleAuthLocked(r, role, msg); errMsg != "" {
				r.mu.Unlock()
				s.sendError(conn, errMsg)
				return
			}
			p.authed = true
			if role == protocol.RoleAgent {
				// This connection proved the credential: it is the agent now.
				// Whatever held the slot before is a stale socket of the same
				// agent (a reconnect after a drop), or never authenticated.
				for _, old := range r.superseded(p) {
					s.writeTo(old, protocol.Message{Type: protocol.TypeError, Error: "superseded by a fresh agent connection"})
					go old.conn.Close()
				}
			}
			authed = true // loosen this conn's read deadline to the liveness budget
			var stale []*peer
			if role == protocol.RoleAgent {
				// An agent come (back): parked sockets hold a key from before it.
				stale = r.dropParkedLocked()
			}
			// Compute presence flags for notifications after unlock.
			agentOnline := r.agent != nil && r.agent.authed
			anyViewerOnline := false
			for _, v := range r.viewers {
				if v.authed && !v.parked {
					anyViewerOnline = true
					break
				}
			}
			r.mu.Unlock()
			closeParked(stale)

			s.writeTo(p, protocol.Message{Type: protocol.TypeAuthOK})

			switch role {
			case protocol.RoleViewer:
				// Tell the freshly-authed viewer whether the agent is here.
				if agentOnline {
					s.writeTo(p, protocol.Message{Type: protocol.TypeConnected})
					// Inform agent that a viewer connected, with live count.
					s.notifyPeer(sessionID, protocol.RoleAgent, protocol.Message{
						Type:  protocol.TypeConnected,
						Count: s.viewerCount(sessionID),
					})
				} else {
					s.writeTo(p, protocol.Message{Type: protocol.TypeAgentOffline})
				}
			case protocol.RoleAgent:
				// If any viewers were already waiting, tell them the agent is back.
				if anyViewerOnline {
					s.broadcastViewers(sessionID, protocol.Message{Type: protocol.TypeAgentOnline})
				}
			}
			continue
		}
		r.mu.Unlock()

		switch {
		case msg.Type == protocol.TypePing:
			s.writeTo(p, protocol.Message{Type: protocol.TypePong})
		case role == protocol.RoleViewer && (msg.Type == protocol.TypePark || msg.Type == protocol.TypeUnpark):
			s.park(sessionID, p, msg.Type == protocol.TypePark)
		case forwardableTypes[msg.Type]:
			r.mu.Lock()
			parked := p.parked
			r.mu.Unlock()
			if parked {
				continue // a parked viewer says nothing to the agent until it unparks
			}
			if ok, retry := s.admitHandshake(sessionID, role, conn, &msg); !ok {
				s.writeTo(p, pakeBusy(msg, retry))
				continue
			}
			s.forward(sessionID, role, conn, msg)
		}
	}
}

// superseded makes p the room's agent and returns the connections it
// replaces: the previous agent, if any, and the other pending ones. Also
// cancels a pending expiry. Callers hold r.mu.
func (r *room) superseded(p *peer) []*peer {
	var old []*peer
	if r.agent != nil && r.agent != p {
		old = append(old, r.agent)
	}
	for _, a := range r.pendingAgents {
		if a != p {
			old = append(old, a)
		}
	}
	r.pendingAgents = nil
	r.agent = p
	if r.cleanup != nil {
		r.cleanup.Stop()
		r.cleanup = nil
	}
	return old
}

// peer returns the agent peer when role == RoleAgent. Callers wanting all
// viewers should iterate r.viewers directly under r.mu.
func (r *room) peer(role protocol.Role) *peer {
	if role == protocol.RoleAgent {
		return r.agent
	}
	return nil
}

// peerByConn resolves the peer for THIS specific connection, handling
// viewers (a room holds many, keyed by conn) as well as the agent.
// peer() can't do this — it returns nil for any viewer role — so a
// viewer's own read loop could never find itself and bailed before
// processing its auth message, dropping every browser viewer at connect.
func (r *room) peerByConn(role protocol.Role, conn *websocket.Conn) *peer {
	if role == protocol.RoleAgent {
		if r.agent != nil && r.agent.conn == conn {
			return r.agent
		}
		for _, a := range r.pendingAgents {
			if a.conn == conn {
				return a
			}
		}
		return nil
	}
	for _, v := range r.viewers {
		if v.conn == conn {
			return v
		}
	}
	return nil
}

func (s *Server) handleAuthLocked(r *room, role protocol.Role, msg protocol.Message) string {
	switch role {
	case protocol.RoleAgent:
		// The agent proves control of the session with a high-entropy reattach
		// *token* (preferred) and/or a legacy bcrypt *pin_hash*. The relay never
		// checks either against the PIN — they're opaque secrets it only matches
		// against what the session's original agent registered, so a stranger
		// can't hijack a session whose agent WS briefly dropped. Accept-either
		// keeps old agents (pin_hash only) working while new agents move to
		// token-only; a legacy session migrates the first time its upgraded
		// agent presents pin_hash (to prove control) alongside a fresh token.
		if msg.Token == "" && msg.PinHash == "" {
			return "agent credential required"
		}
		switch {
		case r.auth.token != "":
			// Already migrated to a token — nothing else authenticates.
			if !secretEqual(msg.Token, r.auth.token) {
				return "session credentials mismatch"
			}
		case r.auth.pinHash != "":
			// Legacy session: require the same pin_hash. If the agent also
			// brought a token, migrate to token-only and drop the pin_hash so
			// the offline-crackable value stops living at the relay.
			if !secretEqual(msg.PinHash, r.auth.pinHash) {
				return "session credentials mismatch"
			}
			if msg.Token != "" {
				r.auth.token = msg.Token
				r.auth.pinHash = ""
			}
		default:
			// No credential yet: a brand-new session, or one whose room was
			// cleared. A cleared room's session ID stays reserved for the
			// agent that had it.
			if r.auth.ownerHash != "" && !credentialMatches(r.auth.ownerHash, msg.Token, msg.PinHash) {
				return "session credentials mismatch"
			}
			// Register whatever proves control, preferring the token so no
			// PIN-derived value is ever stored.
			if msg.Token != "" {
				r.auth.token = msg.Token
			} else {
				r.auth.pinHash = msg.PinHash
			}
		}
		r.auth.agentAuthed = true
		return ""

	case protocol.RoleViewer:
		// The relay does NOT authenticate the viewer's PIN. A 6-digit PIN it
		// could verify would be offline-brute-forceable, and — worse — knowing
		// the PIN lets a malicious relay unblind both ephemeral keys and MITM the
		// EKE (learning the shared session key). Viewer PIN auth is done END-TO-END
		// by the EKE instead: a wrong PIN fails the AES-GCM session-key unwrap,
		// which both viewers surface as "PIN mismatch". We only gate on the
		// session being live. A `pin` field from an older viewer is ignored (still
		// accepted, for backward compatibility). Brute-force is bounded by the
		// agent-side kex throttle, since each guess needs an online EKE round-trip.
		if !r.auth.agentAuthed {
			return "session not ready"
		}
		return ""
	}

	return "invalid role"
}

func (s *Server) handleLegacyConn(conn *websocket.Conn) {
	// Per-connection goroutine; a panic must not crash the relay for everyone else.
	defer func() { _ = recover() }()
	defer conn.Close()
	defer forgetClientAddr(conn)

	var registered bool
	var sessionID string
	var role protocol.Role

	for {
		wait := s.authWait
		if registered {
			wait = s.readWait
		}
		_ = conn.SetReadDeadline(time.Now().Add(wait))
		_, raw, err := conn.ReadMessage()
		if err != nil {
			break
		}

		var msg protocol.Message
		if err := json.Unmarshal(raw, &msg); err != nil {
			s.sendError(conn, "invalid message")
			continue
		}

		switch msg.Type {
		case protocol.TypeRegister:
			// Agents register on /ws/<session>/agent and prove a credential
			// there. Nothing on this path can authenticate an agent any more,
			// so one is not accepted here at all.
			s.sendError(conn, "update reminal: agents no longer register here")
			return

		case protocol.TypeJoin:
			if registered {
				continue
			}
			sessionID = msg.SessionID
			role = protocol.RoleViewer
			if ok, reason := s.attach(sessionID, role, conn); !ok {
				s.sendError(conn, reason)
				return
			}
			registered = true
			s.notifyPeer(sessionID, protocol.RoleAgent, protocol.Message{
				Type:  protocol.TypeConnected,
				Count: s.viewerCount(sessionID),
			})
			s.broadcastViewers(sessionID, protocol.Message{Type: protocol.TypeConnected})

		case protocol.TypePing:
			s.write(conn, protocol.Message{Type: protocol.TypePong})

		default:
			// Blindly relay agent↔viewer traffic. Shares forwardableTypes with
			// handleSessionConn so the two paths can never drift (this legacy path
			// had silently fallen behind — missing new_session, window, app, and
			// webrtc types).
			if registered && forwardableTypes[msg.Type] {
				if ok, retry := s.admitHandshake(sessionID, role, conn, &msg); !ok {
					s.write(conn, pakeBusy(msg, retry))
					continue
				}
				s.forward(sessionID, role, conn, msg)
			}
		}
	}

	if registered {
		s.detach(sessionID, role, conn)
	}
}

func (s *Server) getRoom(sessionID string) *room {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.rooms[sessionID]
}

func (s *Server) attach(sessionID string, role protocol.Role, conn *websocket.Conn) (bool, string) {
	s.mu.Lock()
	r, exists := s.rooms[sessionID]
	if !exists {
		if role != protocol.RoleAgent {
			s.mu.Unlock()
			return false, "session not found or not ready"
		}
		r = &room{}
		if o, ok := s.owners[sessionID]; ok && time.Now().Before(o.until) {
			r.auth.ownerHash = o.hash
		}
		s.rooms[sessionID] = r
	}
	s.mu.Unlock()

	r.mu.Lock()
	defer r.mu.Unlock()

	p := &peer{conn: conn, role: role}
	switch role {
	case protocol.RoleAgent:
		r.pendingAgents = append(r.pendingAgents, p)
	case protocol.RoleViewer:
		// Allow viewer to connect as long as the room was set up by an
		// authenticated agent — even if the agent is briefly offline.
		if !r.auth.agentAuthed {
			return false, "session not found or not ready"
		}
		r.viewers = append(r.viewers, p)
	default:
		return false, "invalid role"
	}
	return true, ""
}

func (s *Server) detach(sessionID string, role protocol.Role, conn *websocket.Conn) {
	r := s.getRoom(sessionID)
	if r == nil {
		return
	}

	r.mu.Lock()
	switch role {
	case protocol.RoleAgent:
		for i, a := range r.pendingAgents {
			if a.conn == conn {
				r.pendingAgents = append(r.pendingAgents[:i], r.pendingAgents[i+1:]...)
				break
			}
		}
		if r.agent == nil || r.agent.conn != conn {
			// A connection that never became the agent, or was superseded:
			// nothing changes for the viewers.
			break
		}
		r.agent = nil
		// Parked sockets are closed instead: the agent they would resume has
		// gone, and the one that comes back has a key of its own.
		stale := r.dropParkedLocked()
		defer closeParked(stale)
		for _, v := range r.viewers {
			if v.authed {
				s.writeTo(v, protocol.Message{Type: protocol.TypeAgentOffline})
			}
		}
		// Schedule TTL cleanup. If the agent reattaches before it fires,
		// its authentication cancels this timer.
		if r.cleanup != nil {
			r.cleanup.Stop()
		}
		sid := sessionID
		r.cleanup = time.AfterFunc(s.orphanTTL(), func() { s.expireRoom(sid) })
	case protocol.RoleViewer:
		counted := false
		for i, v := range r.viewers {
			if v.conn == conn {
				counted = !v.parked
				r.viewers = append(r.viewers[:i], r.viewers[i+1:]...)
				break
			}
		}
		// A parked socket was no viewer to the agent — nor one the room has
		// already dropped (dropParkedLocked): its going changes nothing there.
		if counted && r.agent != nil && r.agent.authed {
			s.writeTo(r.agent, protocol.Message{
				Type:  protocol.TypeClosed,
				Error: "viewer disconnected",
				Count: r.activeViewersLocked(),
			})
		}
	}
	empty := r.agent == nil && len(r.pendingAgents) == 0 && len(r.viewers) == 0 && r.cleanup == nil
	r.mu.Unlock()

	if empty {
		s.mu.Lock()
		if existing, ok := s.rooms[sessionID]; ok && existing == r {
			r.mu.Lock()
			stillEmpty := r.agent == nil && len(r.pendingAgents) == 0 && len(r.viewers) == 0 && r.cleanup == nil
			r.mu.Unlock()
			if stillEmpty {
				delete(s.rooms, sessionID)
			}
		}
		s.mu.Unlock()
	}
}

// keepOwnerLocked reserves sessionID for the credential hash owner, and drops
// reservations that have run out. Callers hold s.mu.
func (s *Server) keepOwnerLocked(sessionID, owner string) {
	if s.owners == nil {
		s.owners = make(map[string]roomOwner)
	}
	now := time.Now()
	for id, o := range s.owners {
		if now.After(o.until) {
			delete(s.owners, id)
		}
	}
	if owner != "" {
		s.owners[sessionID] = roomOwner{hash: owner, until: now.Add(s.ownerKeep())}
	}
}

// expireRoom fires after orphanTTL. If the agent never reattached, we kick
// the viewer (if any) and drop the room. If the agent did come back, this is
// a no-op.
func (s *Server) expireRoom(sessionID string) {
	r := s.getRoom(sessionID)
	if r == nil {
		return
	}

	r.mu.Lock()
	if r.agent != nil {
		// Agent is back; nothing to do.
		r.cleanup = nil
		r.mu.Unlock()
		return
	}
	viewerConns := make([]*websocket.Conn, 0, len(r.viewers))
	for _, v := range r.viewers {
		s.writeTo(v, protocol.Message{Type: protocol.TypeClosed, Error: "agent session expired"})
		viewerConns = append(viewerConns, v.conn)
	}
	r.viewers = nil
	r.cleanup = nil
	owner := credentialHash(r.auth.token, r.auth.pinHash)
	r.mu.Unlock()

	for _, c := range viewerConns {
		_ = c.Close()
	}

	s.mu.Lock()
	if existing, ok := s.rooms[sessionID]; ok && existing == r {
		delete(s.rooms, sessionID)
		s.keepOwnerLocked(sessionID, owner)
	}
	s.mu.Unlock()
}

func (s *Server) notifyPeer(sessionID string, role protocol.Role, msg protocol.Message) {
	r := s.getRoom(sessionID)
	if r == nil {
		return
	}
	r.mu.Lock()
	p := r.peer(role)
	r.mu.Unlock()
	if p != nil && p.authed {
		s.writeTo(p, msg)
	}
}

func (s *Server) forward(sessionID string, from protocol.Role, conn *websocket.Conn, msg protocol.Message) {
	r := s.getRoom(sessionID)
	if r == nil {
		return
	}

	r.mu.Lock()
	if !r.auth.agentAuthed {
		r.mu.Unlock()
		return
	}
	var targets []*peer
	switch from {
	case protocol.RoleAgent:
		// Only the agent's own connection speaks for the agent; one that is
		// still waiting to authenticate does not.
		if r.agent == nil || r.agent.conn != conn {
			r.mu.Unlock()
			return
		}
		// Broadcast agent output to every authed viewer, but not to a
		// parked one.
		for _, v := range r.viewers {
			if v.authed && !v.parked {
				targets = append(targets, v)
			}
		}
	case protocol.RoleViewer:
		if r.agent != nil && r.agent.authed {
			targets = []*peer{r.agent}
		}
	}
	r.mu.Unlock()

	for _, t := range targets {
		s.writeTo(t, msg)
	}
}

// viewerCount returns the current live viewer count for a session, used to
// stamp presence notifications sent to the agent.
func (s *Server) viewerCount(sessionID string) int {
	r := s.getRoom(sessionID)
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.activeViewersLocked()
}

// activeViewersLocked counts the viewers the agent has: all but the parked.
// Callers hold r.mu.
func (r *room) activeViewersLocked() int {
	n := 0
	for _, v := range r.viewers {
		if !v.parked {
			n++
		}
	}
	return n
}

// dropParkedLocked takes the parked sockets out of the room, to be closed
// (closeParked) once r.mu is let go. Callers hold r.mu.
func (r *room) dropParkedLocked() []*peer {
	var out []*peer
	kept := r.viewers[:0]
	for _, v := range r.viewers {
		if v.parked {
			out = append(out, v)
		} else {
			kept = append(kept, v)
		}
	}
	r.viewers = kept
	return out
}

func closeParked(ps []*peer) {
	for _, p := range ps {
		go p.conn.Close()
	}
}

// park parks or unparks the viewer p, which sent it — never another
// socket — and tells the agent its count of viewers. It is paced per
// session and address (takePark). Unparking needs an agent to go back to;
// without one, or past the pace, it is refused and the viewer dials afresh.
func (s *Server) park(sessionID string, p *peer, park bool) {
	if ok, _ := s.takePark(sessionID, p.conn, time.Now()); !ok {
		s.writeTo(p, protocol.Message{Type: protocol.TypeParkRefused, Error: "too many switches just now"})
		return
	}
	r := s.getRoom(sessionID)
	if r == nil {
		return
	}
	r.mu.Lock()
	if !park && (r.agent == nil || !r.agent.authed) {
		r.mu.Unlock()
		s.writeTo(p, protocol.Message{Type: protocol.TypeParkRefused, Error: "agent not connected"})
		return
	}
	changed := p.parked != park
	p.parked = park
	agent := r.agent
	count := r.activeViewersLocked()
	r.mu.Unlock()
	if changed && agent != nil && agent.authed {
		if park {
			s.writeTo(agent, protocol.Message{Type: protocol.TypeClosed, Error: "viewer parked", Count: count})
		} else {
			s.writeTo(agent, protocol.Message{Type: protocol.TypeConnected, Count: count})
		}
	}
	if park {
		s.writeTo(p, protocol.Message{Type: protocol.TypeParked})
	} else {
		s.writeTo(p, protocol.Message{Type: protocol.TypeUnparked})
	}
}

// broadcastViewers sends msg to every authed viewer in the room. Used for
// presence transitions (agent_online/agent_offline) — agent_offline goes via
// detach()'s direct write loop since it has the room lock already.
func (s *Server) broadcastViewers(sessionID string, msg protocol.Message) {
	r := s.getRoom(sessionID)
	if r == nil {
		return
	}
	r.mu.Lock()
	targets := make([]*peer, 0, len(r.viewers))
	for _, v := range r.viewers {
		if v.authed && !v.parked {
			targets = append(targets, v)
		}
	}
	r.mu.Unlock()
	for _, t := range targets {
		s.writeTo(t, msg)
	}
}

func (s *Server) write(conn *websocket.Conn, msg protocol.Message) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	_ = conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
	_ = conn.WriteMessage(websocket.TextMessage, data)
}

func (s *Server) writeTo(p *peer, msg protocol.Message) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	_ = p.conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
	_ = p.conn.WriteMessage(websocket.TextMessage, data)
}

func (s *Server) sendError(conn *websocket.Conn, text string) {
	s.write(conn, protocol.Message{Type: protocol.TypeError, Error: text})
}
