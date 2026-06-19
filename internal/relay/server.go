package relay

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/reminal/reminal/internal/protocol"
	"github.com/reminal/reminal/internal/session"
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
}

type room struct {
	agent  *peer
	viewer *peer
	auth   authState
	mu     sync.Mutex
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

	if !s.attach(sessionID, peerRole, conn) {
		var msg string
		switch peerRole {
		case protocol.RoleAgent:
			msg = "session already has an agent"
		case protocol.RoleViewer:
			msg = "session not found or not ready"
		default:
			msg = "connection rejected"
		}
		s.sendError(conn, msg)
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
		if p == nil || !p.authed {
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
			r.mu.Unlock()
			s.write(conn, protocol.Message{Type: protocol.TypeAuthOK})
			if role == protocol.RoleViewer {
				s.notifyConnected(sessionID)
			}
			continue
		}
		r.mu.Unlock()

		switch msg.Type {
		case protocol.TypeData, protocol.TypeResize:
			s.forward(sessionID, role, msg)
		case protocol.TypePing:
			s.write(conn, protocol.Message{Type: protocol.TypePong})
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
			if !s.attach(sessionID, role, conn) {
				s.sendError(conn, "session already has an agent")
				return
			}
			registered = true

		case protocol.TypeJoin:
			if registered {
				continue
			}
			sessionID = msg.SessionID
			role = protocol.RoleViewer
			if !s.attach(sessionID, role, conn) {
				s.sendError(conn, "session not found or viewer already connected")
				return
			}
			registered = true
			s.notifyConnected(sessionID)

		case protocol.TypeData, protocol.TypeResize:
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

func (s *Server) attach(sessionID string, role protocol.Role, conn *websocket.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.rooms[sessionID]
	if !ok {
		r = &room{}
		s.rooms[sessionID] = r
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	p := &peer{conn: conn, role: role}
	switch role {
	case protocol.RoleAgent:
		if r.agent != nil {
			return false
		}
		r.agent = p
	case protocol.RoleViewer:
		if r.agent == nil || !r.auth.agentAuthed {
			return false
		}
		if r.viewer != nil {
			return false
		}
		r.viewer = p
	default:
		return false
	}
	return true
}

func (s *Server) detach(sessionID string, role protocol.Role) {
	s.mu.Lock()
	r, ok := s.rooms[sessionID]
	if !ok {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	r.mu.Lock()
	switch role {
	case protocol.RoleAgent:
		if r.viewer != nil {
			s.write(r.viewer.conn, protocol.Message{Type: protocol.TypeClosed, Error: "agent disconnected"})
			r.viewer.conn.Close()
		}
		r.agent = nil
		r.viewer = nil
		r.auth = authState{}
	case protocol.RoleViewer:
		r.viewer = nil
		r.auth.viewerAuthed = false
	}
	r.mu.Unlock()

	s.mu.Lock()
	if r.agent == nil && r.viewer == nil {
		delete(s.rooms, sessionID)
	}
	s.mu.Unlock()
}

func (s *Server) notifyConnected(sessionID string) {
	s.mu.RLock()
	r, ok := s.rooms[sessionID]
	s.mu.RUnlock()
	if !ok {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	connected := protocol.Message{Type: protocol.TypeConnected, SessionID: sessionID}
	if r.agent != nil && r.agent.authed {
		s.write(r.agent.conn, connected)
	}
	if r.viewer != nil && r.viewer.authed {
		s.write(r.viewer.conn, connected)
	}
}

func (s *Server) forward(sessionID string, from protocol.Role, msg protocol.Message) {
	r := s.getRoom(sessionID)
	if r == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.auth.agentAuthed || !r.auth.viewerAuthed {
		return
	}

	var target *peer
	switch from {
	case protocol.RoleAgent:
		target = r.viewer
	case protocol.RoleViewer:
		target = r.agent
	}
	if target != nil && target.authed {
		s.write(target.conn, msg)
	}
}

func (s *Server) write(conn *websocket.Conn, msg protocol.Message) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	_ = conn.WriteMessage(websocket.TextMessage, data)
}

func (s *Server) sendError(conn *websocket.Conn, text string) {
	s.write(conn, protocol.Message{Type: protocol.TypeError, Error: text})
}
