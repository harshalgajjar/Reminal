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
	viewer  *peer
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
	defer s.detach(sessionID, role)

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
		p := r.peer(role)
		if p == nil || p.conn != conn {
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
			viewerOnline := r.viewer != nil && r.viewer.authed
			r.mu.Unlock()

			s.writeTo(p, protocol.Message{Type: protocol.TypeAuthOK})

			switch role {
			case protocol.RoleViewer:
				// Tell the freshly-authed viewer whether the agent is here.
				if agentOnline {
					s.writeTo(p, protocol.Message{Type: protocol.TypeConnected})
					// Inform agent that a viewer connected.
					s.notifyPeer(sessionID, protocol.RoleAgent, protocol.Message{Type: protocol.TypeConnected})
				} else {
					s.writeTo(p, protocol.Message{Type: protocol.TypeAgentOffline})
				}
			case protocol.RoleAgent:
				// If a viewer was already waiting, tell them the agent is back.
				if viewerOnline {
					s.notifyPeer(sessionID, protocol.RoleViewer, protocol.Message{Type: protocol.TypeAgentOnline})
				}
			}
			continue
		}
		r.mu.Unlock()

		switch msg.Type {
		case protocol.TypeData, protocol.TypeResize, protocol.TypeResume:
			s.forward(sessionID, role, msg)
		case protocol.TypePing:
			s.writeTo(p, protocol.Message{Type: protocol.TypePong})
		}
	}
}

func (r *room) peer(role protocol.Role) *peer {
	switch role {
	case protocol.RoleAgent:
		return r.agent
	case protocol.RoleViewer:
		return r.viewer
	default:
		return nil
	}
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
		r.auth.viewerAuthed = true
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
			s.notifyPeer(sessionID, protocol.RoleAgent, protocol.Message{Type: protocol.TypeConnected})
			s.notifyPeer(sessionID, protocol.RoleViewer, protocol.Message{Type: protocol.TypeConnected})

		case protocol.TypeData, protocol.TypeResize, protocol.TypeResume:
			if !registered {
				continue
			}
			s.forward(sessionID, role, msg)

		case protocol.TypePing:
			s.write(conn, protocol.Message{Type: protocol.TypePong})
		}
	}

	if registered {
		s.detach(sessionID, role)
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
		if r.viewer != nil {
			return false, "another viewer is already connected to this session"
		}
		r.viewer = p
	default:
		return false, "invalid role"
	}
	return true, ""
}

func (s *Server) detach(sessionID string, role protocol.Role) {
	r := s.getRoom(sessionID)
	if r == nil {
		return
	}

	r.mu.Lock()
	switch role {
	case protocol.RoleAgent:
		r.agent = nil
		if r.viewer != nil && r.viewer.authed {
			s.writeTo(r.viewer, protocol.Message{Type: protocol.TypeAgentOffline})
		}
		// Schedule TTL cleanup. If the agent reattaches before it fires,
		// attach() cancels this timer.
		if r.cleanup != nil {
			r.cleanup.Stop()
		}
		sid := sessionID
		r.cleanup = time.AfterFunc(orphanTTL, func() { s.expireRoom(sid) })
	case protocol.RoleViewer:
		r.viewer = nil
		r.auth.viewerAuthed = false
		if r.agent != nil && r.agent.authed {
			s.writeTo(r.agent, protocol.Message{Type: protocol.TypeClosed, Error: "viewer disconnected"})
		}
	}
	empty := r.agent == nil && r.viewer == nil && r.cleanup == nil
	r.mu.Unlock()

	if empty {
		s.mu.Lock()
		// Re-check under the global lock; another goroutine may have re-populated.
		if existing, ok := s.rooms[sessionID]; ok && existing == r {
			r.mu.Lock()
			stillEmpty := r.agent == nil && r.viewer == nil && r.cleanup == nil
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
	var viewerConn *websocket.Conn
	if r.viewer != nil {
		s.writeTo(r.viewer, protocol.Message{Type: protocol.TypeClosed, Error: "agent session expired"})
		viewerConn = r.viewer.conn
		r.viewer = nil
	}
	r.cleanup = nil
	r.mu.Unlock()

	if viewerConn != nil {
		_ = viewerConn.Close()
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
	var target *peer
	switch from {
	case protocol.RoleAgent:
		target = r.viewer
	case protocol.RoleViewer:
		target = r.agent
	}
	r.mu.Unlock()

	if target == nil || !target.authed {
		return
	}
	s.writeTo(target, msg)
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
