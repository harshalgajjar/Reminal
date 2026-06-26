package protocol

type Role string

const (
	RoleAgent  Role = "agent"
	RoleViewer Role = "viewer"
	// RoleTunnel is used by `reminal expose <port>`. The agent connects to
	// the relay with this role, registers a local port, then receives HTTP
	// tunnel-request frames and replies with tunnel responses. Distinct
	// from RoleAgent so shell broadcasts and HTTP tunneling never get
	// crossed in the relay's per-session state.
	RoleTunnel Role = "tunnel"
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
	// TypeKexInit is the viewer's opening message of the PIN-authenticated
	// X25519 handshake. Carries the viewer's ephemeral public key, blinded
	// by XOR-ing with HKDF(PIN). ExID is a random per-handshake
	// correlation ID the viewer picks; the agent echoes it in TypeKexResp
	// so the originating viewer can recognise the response among the
	// agent's broadcasts. See internal/crypto/kex.go for the construction.
	TypeKexInit MessageType = "kex_init"
	// TypeKexResp is the agent's reply to a TypeKexInit. Carries the
	// agent's blinded ephemeral public key plus the wrapped session key
	// (AES-256-GCM under HKDF(ECDH-shared, salt=ex_id)).
	TypeKexResp MessageType = "kex_resp"
	// TypeCopyAck is sent by the paste side of a rendezvous AFTER it has
	// received every chunk and written the file, to tell the source the
	// transfer landed. The source waits for it before closing — otherwise
	// closing right after the last chunk races (and on the network beats)
	// delivery of that chunk through the relay, silently truncating the
	// file. It also makes the source's "Sent." mean "the paste has it."
	TypeCopyAck MessageType = "copy_ack"
	// TypeKexConfirm is sent by the paste side of a `reminal copy`/`paste`
	// rendezvous after it unwraps the transfer key, to prove to the source
	// that it derived the same key (i.e. used the right code) BEFORE the
	// source streams any file bytes. Data is box.Encrypt of a fixed label
	// under the transfer key; the source decrypts and checks the label. A
	// wrong code fails the unwrap on the paste side and, even if a peer
	// forged a KexConfirm, fails this check on the source side — so a
	// wrong-code paste never receives ciphertext, it just burns an attempt.
	TypeKexConfirm MessageType = "kex_confirm"
	// TypeUpload carries an encrypted file from a viewer to the agent.
	// Payload (after decrypt) is JSON: {"name": "...", "content": "<base64>"}.
	TypeUpload MessageType = "upload"
	// TypeDownload carries an encrypted file from the agent to all
	// viewers (broadcast like TypeData), chunked the same way uploads are.
	// Payload after decrypt is JSON:
	//   {"download_id":"...", "index":0, "total":N, "name":"...",
	//    "content":"<base64 of this chunk>", "size":<total file bytes>}
	// Viewers buffer chunks by download_id and reassemble in index order
	// once all `total` chunks arrive. A single-chunk file (total<=1, or no
	// download_id from a legacy agent) is written straight to disk.
	TypeDownload MessageType = "download"
	// TypeNotify carries an encrypted user notification from the agent to
	// every viewer ("build done", "tests passed"). Payload after decrypt
	// is JSON: {"message": "..."}.
	TypeNotify MessageType = "notify"
	// TypeUploadAck is sent by the agent after a viewer-initiated upload
	// is written to disk. Broadcast to all viewers (so the originator
	// gets it back), but only the viewer whose upload_id matches will
	// react — by auto-typing the resolved absolute path into the shell
	// at the cursor, the way pasting a filename works on a desktop
	// terminal. Payload after decrypt is JSON:
	//   {"upload_id":"...", "path":"/Users/.../Downloads/reminal/foo.png"}
	TypeUploadAck MessageType = "upload_ack"

	// ---- Port-forward tunneling (RoleTunnel sessions) ----
	// These payloads are NOT end-to-end encrypted — the Worker needs to
	// route URL paths and serve a PIN gate, so it has to read them. Same
	// trust model as ngrok / cloudflared: the relay sees your HTTP.

	// TypeTunnelRegister is sent by the agent once on connect to declare
	// the local port it's proxying. Payload (Data, JSON):
	//   {"port": 3000, "pin_hash": "<bcrypt>", "public": false}
	// "public": true skips the PIN gate (use with care).
	TypeTunnelRegister MessageType = "tunnel_register"
	// TypeTunnelReq is the relay→agent envelope for a single incoming HTTP
	// request. Payload (Data, JSON):
	//   {"req_id":"abc","method":"GET","url":"/path?q=1",
	//    "headers":{"User-Agent":"...", ...}, "body":"<base64>"}
	TypeTunnelReq MessageType = "tunnel_req"
	// TypeTunnelResp is the agent→relay reply. Payload (Data, JSON):
	//   {"req_id":"abc","status":200,
	//    "headers":{"Content-Type":"text/html", ...},
	//    "body":"<base64>"}
	TypeTunnelResp MessageType = "tunnel_resp"
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
	// ExID is the per-handshake correlation ID used by TypeKexInit /
	// TypeKexResp. Hex-encoded so it's safe to read off the wire.
	ExID string `json:"ex_id,omitempty"`
	// Wrap carries the AES-GCM-wrapped session key in TypeKexResp,
	// base64-encoded.
	Wrap string `json:"wrap,omitempty"`
}
