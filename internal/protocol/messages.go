// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

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
	// TypeOwnerInit is an enrolled device's opening message of a PIN-free
	// (owner) connect: its raw ephemeral X25519 key (Data), its owner public
	// key (DevicePub), and a signature (DeviceSig) proving it controls that key
	// for this session. See internal/crypto/owner.go.
	TypeOwnerInit MessageType = "own_init"
	// TypeOwnerResp is the agent's reply to a TypeOwnerInit: the agent's raw
	// ephemeral key (Data), the machine identity key (MachinePub) and a
	// signature over both ephemerals + both identities (MachineSig) so the
	// device can confirm it's the real host, plus the wrapped session key
	// (Wrap) — delivered only after the device signature verified.
	TypeOwnerResp MessageType = "own_resp"
	// TypeDirQuery is sent by an owner device on a machine's owner-derived
	// directory channel, AFTER an own_init/own_resp handshake there has proven
	// ownership, to ask "what sessions are you running?". No payload.
	TypeDirQuery MessageType = "dir_query"
	// TypeDirResp is the directory host's reply: the machine's live session list
	// (and hostname) as a DirResponse JSON, encrypted under the channel's session
	// key (Data) so the relay never sees what's running. `reminal machines` and
	// the web Machines panel render it.
	TypeDirResp MessageType = "dir_resp"
	// TypeDirRename asks the directory host to rename one of the machine's live
	// sessions (from the Machines panel). Data is {"id","name","req_id"} encrypted
	// under the channel key — so, like new_session, only an authenticated owner can
	// issue it. The host drives the target session's local control socket. It
	// replies with a TypeDirRename echoing req_id and any error.
	TypeDirRename MessageType = "dir_rename"
	// TypeDirRevokeSelf asks the directory host to revoke the SENDING device's own
	// PIN-free access to this machine. Data is {"device_pub","sig","req_id"}
	// encrypted under the channel key; sig is over RevokeSelfTranscript so only the
	// device itself can revoke itself (no owner can revoke another). The host
	// tombstones the device and replies echoing req_id.
	TypeDirRevokeSelf MessageType = "dir_revoke_self"
	// TypeDirKill asks the directory host to terminate one of the machine's live
	// sessions (the Machines panel's kill action). Data is {"id","req_id"}
	// encrypted under the channel key — owner-gated like new_session/dir_rename.
	// The host SIGTERMs (then SIGKILLs) the target agent and replies echoing
	// req_id.
	TypeDirKill MessageType = "dir_kill"
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

	// ---- Window mirroring (view + control a host window in the browser) ----
	// Like uploads/downloads, every payload rides end-to-end encrypted in
	// Data as JSON; the relay forwards these opaquely (no relay changes).

	// TypeWindowList is bidirectional. Viewer→agent: a request with empty
	// Data ("what windows are open?"). Agent→viewer: the reply, Data =
	// encrypted JSON
	// {"windows":[{"id","app","title","icon","x","y","w","h"}, ...]}.
	// icon is an optional PNG data URL supplied by backends that can resolve it.
	TypeWindowList MessageType = "window_list"

	// TypeWindowNotes carries the agent→viewer note list for annotated windows —
	// the same notes the desktop badge shows. Data = encrypted JSON
	// {"notes":{"<windowID>":[{"id","status","title","body","author","ts"},…]}}.
	// Machine-scoped on purpose: a note belongs to a window, not to whichever
	// session's agent happened to publish it, so every viewer on this machine
	// sees the same thing the badge does.
	TypeWindowNotes MessageType = "window_notes"

	// TypeWindowNoteAct is viewer→agent: act on one note the same ways the
	// desktop badge offers. Data = encrypted JSON
	// {"window":"<id>","id":"<noteID>","action":"handback|dismiss|dismiss_all"}.
	TypeWindowNoteAct MessageType = "window_note_act"
	// TypeWindowCtl is viewer→agent. Data = encrypted JSON
	// {"action":"start"|"stop","id":"<window id>"}. "start" begins streaming
	// periodic JPEG frames of that window; "stop" ends the current stream.
	TypeWindowCtl MessageType = "window_ctl"
	// TypeWindowFrame is agent→viewer. Data = encrypted JSON
	// {"id","w","h","img":"<base64 JPEG>"} — one captured frame of the
	// window the viewer asked to stream.
	TypeWindowFrame MessageType = "window_frame"
	// TypeWindowInput is viewer→agent. Data = encrypted JSON describing a
	// mouse/keyboard event to inject into the streamed window, e.g.
	// {"kind":"click","x":0.5,"y":0.3} (x/y are 0..1 fractions of the
	// window) or {"kind":"key","text":"a"} / {"kind":"key","special":"return"}.
	TypeWindowInput MessageType = "window_input"
	// TypeWindowAck is viewer→agent. Data = encrypted JSON {"id","seq"}. The
	// viewer sends one after it has decoded+rendered a frame; the agent won't
	// send the next frame for that window until the previous one is acked (or a
	// short timeout elapses). This bounds in-flight frames to ~1 so latency can't
	// accumulate on a slow link, and paces the frame rate to what the viewer can
	// actually consume.
	TypeWindowAck MessageType = "window_ack"

	// TypeAppList is bidirectional. Viewer→agent: an empty-Data request ("what
	// apps can I launch?"). Agent→viewer: the reply, Data = encrypted JSON
	// {"apps":[{"id","name","icon"}, ...]} of installed apps, or
	// {"unsupported":"..."}. icon is optional for backend compatibility.
	TypeAppList MessageType = "app_list"
	// TypeAppOpen is viewer→agent. Data = encrypted JSON {"id":"<app id>"} —
	// launch (or foreground) that app so its window shows up in the window list.
	TypeAppOpen MessageType = "app_open"

	// TypeHostInfo is bidirectional. Viewer→agent: an empty-Data request ("tell
	// me about the machine you're on"). Agent→viewer: the reply, Data =
	// encrypted JSON of internal/client HostInfo (hostname, OS, arch, CPU model
	// + cores, total/used memory, uptime, load average, and the session PIN so
	// an owner-connected viewer can share). Rides E2E-encrypted in Data like
	// the window messages; the relay forwards it opaquely.
	TypeHostInfo MessageType = "host_info"

	// TypeNewSession is bidirectional. Viewer→agent: a request to spawn a fresh
	// detached headless reminal on the host (Data = encrypted JSON {"name":"…"},
	// name optional). Agent→viewer: the reply, Data = encrypted JSON
	// {"id":"…","pin":"…"} or {"error":"…"} — the viewer then connects to the
	// new session. No new capability: a viewer with shell access could already
	// run `reminal new`; this just makes it one tap.
	TypeNewSession MessageType = "new_session"

	// ---- WebRTC signaling (peer-to-peer frame transport) ----
	// Window frames are high-volume; when a viewer and agent can open a
	// WebRTC DataChannel, frames (and their acks) flow directly peer-to-peer
	// instead of through the relay, which bills each forwarded message. These
	// types carry only the handshake — a few messages per connection — and ride
	// end-to-end encrypted in Data (JSON) exactly like the window messages.
	// Because the payload is sealed under the PIN-authenticated session key, a
	// malicious relay can't tamper with the SDP/ICE (and thus can't MITM the
	// DTLS connection). Each carries a "peer" id (viewer-chosen, like kex ex_id)
	// so the agent runs one PeerConnection per viewer and each side ignores
	// signaling addressed to a different peer.

	// TypeWebRTCHello is viewer→agent: "I can do WebRTC — send me an offer."
	// Data = encrypted JSON {"peer":"<id>"}.
	TypeWebRTCHello MessageType = "webrtc_hello"
	// TypeWebRTCOffer is agent→viewer. Data = encrypted JSON
	// {"peer":"<id>","sdp":"<offer SDP>"}.
	TypeWebRTCOffer MessageType = "webrtc_offer"
	// TypeWebRTCAnswer is viewer→agent. Data = encrypted JSON
	// {"peer":"<id>","sdp":"<answer SDP>"}.
	TypeWebRTCAnswer MessageType = "webrtc_answer"
	// TypeWebRTCICE is bidirectional (trickle ICE). Data = encrypted JSON
	// {"peer":"<id>","candidate":"<candidate>","mid":"<sdpMid>","line":<idx>}.
	TypeWebRTCICE MessageType = "webrtc_ice"
)

type Message struct {
	Type      MessageType `json:"type"`
	SessionID string      `json:"session_id,omitempty"`
	Role      Role        `json:"role,omitempty"`
	Data      string      `json:"data,omitempty"`
	Pin       string      `json:"pin,omitempty"`
	PinHash   string      `json:"pin_hash,omitempty"`
	// Token is the agent's high-entropy reattach credential (Level B). It
	// replaces pin_hash so the relay never holds any PIN-derived, offline-
	// crackable value. A new agent sends this on register; on a legacy session
	// it also sends pin_hash once to prove control while migrating to token.
	Token string `json:"token,omitempty"`
	Cols  uint16 `json:"cols,omitempty"`
	Rows  uint16 `json:"rows,omitempty"`
	// Viewer is a stable per-tab id on encrypted resize reports, so the
	// agent can take min(width)×min(height) across everyone currently
	// attached. Absent on viewers older than this field: those share a
	// single anonymous slot (last anonymous report wins that slot).
	Viewer  string `json:"viewer,omitempty"`
	Error   string `json:"error,omitempty"`
	Seq     uint64 `json:"seq,omitempty"`
	FromSeq uint64 `json:"from_seq,omitempty"`
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
	// Owner-connect fields (TypeOwnerInit / TypeOwnerResp), all base64. The
	// ephemeral X25519 keys ride in Data (same as the PIN path). DevicePub +
	// DeviceSig authenticate the device to the agent; MachinePub + MachineSig
	// authenticate the machine to the device.
	DevicePub  string `json:"device_pub,omitempty"`
	DeviceSig  string `json:"device_sig,omitempty"`
	MachinePub string `json:"machine_pub,omitempty"`
	MachineSig string `json:"machine_sig,omitempty"`
}

// DirSession is one live session as reported by a machine's directory host. It
// deliberately OMITS the PIN — an owner reaches sessions PIN-free, and the PIN
// must never leak onto the directory channel.
type DirSession struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	Cwd      string `json:"cwd,omitempty"`
	Title    string `json:"title,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Port     int    `json:"port,omitempty"`
	Headless bool   `json:"headless,omitempty"`
	Viewers  int    `json:"viewers,omitempty"`
	IdleSecs int64  `json:"idle_secs,omitempty"` // seconds since last PTY activity
	// SearchHits is filled when the directory query carried a regex: snippets
	// from this session's live scrollback. Omitted on a plain listing, and by
	// hosts that do not search yet (they still return the session list).
	SearchHits []string `json:"search_hits,omitempty"`
	// Transcript is filled when the query asked for this session's scrollback
	// dump (stripped ANSI, tail-capped). TranscriptOK distinguishes "host is
	// old and ignored the request" from "host answered, buffer was empty".
	Transcript          string `json:"transcript,omitempty"`
	TranscriptTruncated bool   `json:"transcript_truncated,omitempty"`
	TranscriptOK        bool   `json:"transcript_ok,omitempty"`
}

// DirResponse is the encrypted payload of a TypeDirResp: the machine's hostname
// (so owners can auto-name it) and its live sessions.
type DirResponse struct {
	Hostname string       `json:"hostname,omitempty"`
	Sessions []DirSession `json:"sessions"`
	// KeysOK/KeysError are set when the query asked to inject keystrokes into
	// one session. Omitted by older hosts (they still return the session list).
	KeysOK    bool   `json:"keys_ok,omitempty"`
	KeysError string `json:"keys_error,omitempty"`
}
