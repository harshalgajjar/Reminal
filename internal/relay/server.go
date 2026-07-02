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
	"github.com/reminal/reminal/internal/protocol"
	"github.com/reminal/reminal/internal/session"
)

// orphanTTL is how long a room is kept alive after the agent disconnects,
// giving the same agent a chance to reattach (e.g., across a network blip).
const orphanTTL = 10 * time.Minute

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

type peer struct {
	conn   *websocket.Conn
	role   protocol.Role
	authed bool
	writeMu sync.Mutex
}

type room struct {
	agent   *peer
	viewers []*peer
	auth    authState
	cleanup *time.Timer
	mu      sync.Mutex
}

type Server struct {
	rooms map[string]*room
	mu    sync.RWMutex
}

func NewServer() *Server {
	return &Server{rooms: make(map[string]*room)}
}

func (s *Server) HandleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("upgrade: %v", err)
		return
	}
	conn.SetReadLimit(1 << 20)
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

	if ok, reason := s.attach(sessionID, peerRole, conn); !ok {
		s.sendError(conn, reason)
		conn.Close()
		return
	}

	go s.handleSessionConn(sessionID, peerRole, conn)
}

func (s *Server) handleSessionConn(sessionID string, role protocol.Role, conn *websocket.Conn) {
	defer conn.Close()
	defer s.detach(sessionID, role, conn)

	for {
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
			// Compute presence flags for notifications after unlock.
			agentOnline := r.agent != nil && r.agent.authed
			anyViewerOnline := false
			for _, v := range r.viewers {
				if v.authed {
					anyViewerOnline = true
					break
				}
			}
			r.mu.Unlock()

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

		switch msg.Type {
		case protocol.TypeData, protocol.TypeResize, protocol.TypeResume,
			protocol.TypeKexInit, protocol.TypeKexResp,
			protocol.TypeWindowList, protocol.TypeWindowCtl,
			protocol.TypeWindowFrame, protocol.TypeWindowInput,
			protocol.TypeWindowAck:
			s.forward(sessionID, role, msg)
		case protocol.TypePing:
			s.writeTo(p, protocol.Message{Type: protocol.TypePong})
		}
	}
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
	if r.auth.isLocked() {
		return "too many failed attempts — try again in a few minutes"
	}

	switch role {
	case protocol.RoleAgent:
		if msg.PinHash == "" {
			return "pin_hash required"
		}
		// If the room was previously authenticated, the reattaching agent
		// must present the same pin_hash. This keeps a stranger from
		// hijacking the session even if the original agent's WS drops.
		if r.auth.pinHash != "" && r.auth.pinHash != msg.PinHash {
			return "session credentials mismatch"
		}
		r.auth.pinHash = msg.PinHash
		r.auth.agentAuthed = true
		r.auth.resetFailures()
		return ""

	case protocol.RoleViewer:
		if msg.Pin == "" {
			return "pin required"
		}
		if !r.auth.agentAuthed || r.auth.pinHash == "" {
			return "session not ready"
		}
		if !session.CheckPIN(r.auth.pinHash, msg.Pin) {
			r.auth.recordFailure()
			return "incorrect PIN"
		}
		r.auth.resetFailures()
		return ""
	}

	return "invalid role"
}

func (s *Server) handleLegacyConn(conn *websocket.Conn) {
	defer conn.Close()

	var registered bool
	var sessionID string
	var role protocol.Role

	for {
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
			if registered {
				continue
			}
			sessionID = msg.SessionID
			role = protocol.RoleAgent
			if ok, reason := s.attach(sessionID, role, conn); !ok {
				s.sendError(conn, reason)
				return
			}
			registered = true

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

		case protocol.TypeData, protocol.TypeResize, protocol.TypeResume,
			protocol.TypeKexInit, protocol.TypeKexResp:
			if !registered {
				continue
			}
			s.forward(sessionID, role, msg)

		case protocol.TypePing:
			s.write(conn, protocol.Message{Type: protocol.TypePong})
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
		s.rooms[sessionID] = r
	}
	s.mu.Unlock()

	r.mu.Lock()
	defer r.mu.Unlock()

	p := &peer{conn: conn, role: role}
	switch role {
	case protocol.RoleAgent:
		if r.agent != nil {
			return false, "another agent is already connected to this session"
		}
		r.agent = p
		if r.cleanup != nil {
			r.cleanup.Stop()
			r.cleanup = nil
		}
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
		r.agent = nil
		for _, v := range r.viewers {
			if v.authed {
				s.writeTo(v, protocol.Message{Type: protocol.TypeAgentOffline})
			}
		}
		// Schedule TTL cleanup. If the agent reattaches before it fires,
		// attach() cancels this timer.
		if r.cleanup != nil {
			r.cleanup.Stop()
		}
		sid := sessionID
		r.cleanup = time.AfterFunc(orphanTTL, func() { s.expireRoom(sid) })
	case protocol.RoleViewer:
		for i, v := range r.viewers {
			if v.conn == conn {
				r.viewers = append(r.viewers[:i], r.viewers[i+1:]...)
				break
			}
		}
		if r.agent != nil && r.agent.authed {
			s.writeTo(r.agent, protocol.Message{
				Type:  protocol.TypeClosed,
				Error: "viewer disconnected",
				Count: len(r.viewers),
			})
		}
	}
	empty := r.agent == nil && len(r.viewers) == 0 && r.cleanup == nil
	r.mu.Unlock()

	if empty {
		s.mu.Lock()
		if existing, ok := s.rooms[sessionID]; ok && existing == r {
			r.mu.Lock()
			stillEmpty := r.agent == nil && len(r.viewers) == 0 && r.cleanup == nil
			r.mu.Unlock()
			if stillEmpty {
				delete(s.rooms, sessionID)
			}
		}
		s.mu.Unlock()
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
	r.mu.Unlock()

	for _, c := range viewerConns {
		_ = c.Close()
	}

	s.mu.Lock()
	if existing, ok := s.rooms[sessionID]; ok && existing == r {
		delete(s.rooms, sessionID)
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

func (s *Server) forward(sessionID string, from protocol.Role, msg protocol.Message) {
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
		// Broadcast agent output to every authed viewer.
		for _, v := range r.viewers {
			if v.authed {
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
	return len(r.viewers)
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
		if v.authed {
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
	_ = conn.WriteMessage(websocket.TextMessage, data)
}

func (s *Server) writeTo(p *peer, msg protocol.Message) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	_ = p.conn.WriteMessage(websocket.TextMessage, data)
}

func (s *Server) sendError(conn *websocket.Conn, text string) {
	s.write(conn, protocol.Message{Type: protocol.TypeError, Error: text})
}
