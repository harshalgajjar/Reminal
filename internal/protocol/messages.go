package protocol

type Role string

const (
	RoleAgent  Role = "agent"
	RoleViewer Role = "viewer"
)

type MessageType string

const (
	TypeAuth      MessageType = "auth"
	TypeAuthOK    MessageType = "auth_ok"
	TypeRegister  MessageType = "register"
	TypeJoin      MessageType = "join"
	TypeData      MessageType = "data"
	TypeResize    MessageType = "resize"
	TypeConnected MessageType = "connected"
	TypeError     MessageType = "error"
	TypePing      MessageType = "ping"
	TypePong      MessageType = "pong"
	TypeClosed    MessageType = "closed"
)

type Message struct {
	Type      MessageType `json:"type"`
	SessionID string      `json:"session_id,omitempty"`
	Role      Role        `json:"role,omitempty"`
	Data      string      `json:"data,omitempty"`
	Pin       string      `json:"pin,omitempty"`
	PinHash   string      `json:"pin_hash,omitempty"`
	Cols      uint16      `json:"cols,omitempty"`
	Rows      uint16      `json:"rows,omitempty"`
	Error     string      `json:"error,omitempty"`
}
