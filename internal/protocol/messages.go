package protocol

type Role string

const (
	RoleAgent  Role = "agent"
	RoleViewer Role = "viewer"
)

type MessageType string

const (
	TypeAuth         MessageType = "auth"
	TypeAuthOK       MessageType = "auth_ok"
	TypeRegister     MessageType = "register"
	TypeJoin         MessageType = "join"
	TypeData         MessageType = "data"
	TypeResize       MessageType = "resize"
	TypeConnected    MessageType = "connected"
	TypeError        MessageType = "error"
	TypePing         MessageType = "ping"
	TypePong         MessageType = "pong"
	TypeClosed       MessageType = "closed"
	TypeResume       MessageType = "resume"
	TypeAgentOnline  MessageType = "agent_online"
	TypeAgentOffline MessageType = "agent_offline"
	// TypeUpload carries an encrypted file from a viewer to the agent.
	// Payload (after decrypt) is JSON: {"name": "...", "content": "<base64>"}.
	TypeUpload MessageType = "upload"
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
	Seq       uint64      `json:"seq,omitempty"`
	FromSeq   uint64      `json:"from_seq,omitempty"`
	// Count carries the live viewer count when the relay sends a presence
	// event (TypeConnected / TypeClosed) to the agent, so the host can
	// show "(N active)" without tracking churn itself.
	Count int `json:"count,omitempty"`
}
