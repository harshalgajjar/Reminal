// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"
	"github.com/reminal/reminal/internal/protocol"
)

// rtcProbeWindow is how long a connected-but-unconfirmed DataChannel is probed
// (frames sent over both it and WS) before we give up on it. A channel that
// hasn't delivered a single frame in this long — the cellular-MTU case — is
// closed so we stop wasting sends on it; the viewer re-negotiates later.
const rtcProbeWindow = 8 * time.Second

// rtcHandshakeTimeout bounds how long a freshly-offered peer may sit before its
// DataChannel opens. A viewer that requests an offer but never answers (crashed
// tab, flaky mobile, or a viewer spamming hellos with distinct peer ids) leaves
// a PeerConnection stuck in "new" — pion never fires Failed without a remote
// description, and rtcSinks only reaps peers that DID open — so nothing else
// collects it until the last viewer leaves. This reaps the un-opened peer.
const rtcHandshakeTimeout = 30 * time.Second

// Window frames are the relay's heaviest traffic, and the Cloudflare relay
// bills every forwarded WebSocket message as a request. When a viewer can open
// a WebRTC DataChannel, frames (and their acks) flow directly peer-to-peer over
// DTLS instead, taking the relay out of the hot path entirely — the relay only
// carries the handshake (a few messages) and stays the reliable fallback if the
// peer connection never forms.
//
// Trust: the SDP offer/answer and ICE candidates ride end-to-end encrypted in
// the existing session channel (sealed under the PIN-authenticated key), so the
// relay can't tamper with the DTLS fingerprints and therefore can't MITM the
// connection. Frames on the DataChannel are DTLS-protected, so they don't need
// the app-layer AES-GCM the WS path uses.

// STUN discovers each peer's public address so most connections go direct; a
// TURN server relays the (still DTLS-encrypted) media for peers that can't punch
// through — cellular CGNAT, strict firewalls. TURN is opt-in so no credentials
// live in the public frontend; the agent hands its ICE config to the viewer over
// the encrypted signaling channel (see iceConfig), so credentials never touch
// the served HTML. Configure EITHER:
//   - Cloudflare TURN (ephemeral creds): REMINAL_TURN_CF_KEY + REMINAL_TURN_CF_TOKEN
//   - a static server: REMINAL_TURN (comma-separated urls) + REMINAL_TURN_USER/_PASS
// With neither we're STUN-only and un-punchable peers stay on the WS relay.

// iceServerJSON is the wire form of one ICE server — it matches both the
// browser's RTCIceServer and Cloudflare's credential-API response, so servers
// round-trip straight through to the viewer.
type iceServerJSON struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

// iceConfig returns the ICE servers for a new connection in both the agent's
// (pion) and the viewer's (JSON) form, from one source so both peers use the
// same servers.
func iceConfig() ([]webrtc.ICEServer, []iceServerJSON) {
	js := iceServersJSON()
	pion := make([]webrtc.ICEServer, 0, len(js))
	for _, s := range js {
		ice := webrtc.ICEServer{URLs: s.URLs}
		if s.Username != "" {
			ice.Username = s.Username
			ice.Credential = s.Credential
			ice.CredentialType = webrtc.ICECredentialTypePassword
		}
		pion = append(pion, ice)
	}
	return pion, js
}

func iceServersJSON() []iceServerJSON {
	if cf, ok := cloudflareICE(); ok {
		return cf
	}
	stun := iceServerJSON{URLs: []string{"stun:stun.l.google.com:19302"}}
	var urls []string
	for _, u := range strings.Split(os.Getenv("REMINAL_TURN"), ",") {
		if u = strings.TrimSpace(u); u != "" {
			urls = append(urls, u)
		}
	}
	if len(urls) == 0 {
		return []iceServerJSON{stun}
	}
	return []iceServerJSON{stun, {URLs: urls, Username: os.Getenv("REMINAL_TURN_USER"), Credential: os.Getenv("REMINAL_TURN_PASS")}}
}

// cloudflareICE mints short-lived TURN credentials from Cloudflare's TURN
// credential API, caching them until half their TTL elapses so we don't hit the
// API on every connection. Returns false (→ STUN/static fallback) if it isn't
// configured or the call fails.
var (
	cfMu      sync.Mutex
	cfCache   []iceServerJSON
	cfExpires time.Time
)

func cloudflareICE() ([]iceServerJSON, bool) {
	keyID := os.Getenv("REMINAL_TURN_CF_KEY")
	token := os.Getenv("REMINAL_TURN_CF_TOKEN")
	if keyID == "" || token == "" {
		return nil, false
	}
	cfMu.Lock()
	defer cfMu.Unlock()
	if cfCache != nil && time.Now().Before(cfExpires) {
		return cfCache, true
	}
	const ttl = 86400 // seconds
	req, err := http.NewRequest(http.MethodPost,
		"https://rtc.live.cloudflare.com/v1/turn/keys/"+keyID+"/credentials/generate-ice-servers",
		strings.NewReader(`{"ttl":86400}`))
	if err != nil {
		return nil, false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 8 * time.Second}).Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, false
	}
	var out struct {
		ICEServers []iceServerJSON `json:"iceServers"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) != nil || len(out.ICEServers) == 0 {
		return nil, false
	}
	cfCache = out.ICEServers
	cfExpires = time.Now().Add((ttl / 2) * time.Second)
	return cfCache, true
}

// rtcPeer is one viewer's peer connection and its DataChannels.
type rtcPeer struct {
	id string
	pc *webrtc.PeerConnection
	dc *webrtc.DataChannel
	// ctl is the v2 out-of-band channel: acks, keyframe requests and
	// heartbeats. Separate from dc because SCTP orders and (on v1) reliably
	// retransmits per stream — an ack sharing the frame stream queues BEHIND
	// every buffered frame, so a bitrate spike delayed acks, which the pacing
	// loop read as a dead viewer and answered by demoting the transport and
	// then killing the stream ("screen may be locked or asleep" on a host that
	// was never away). Nil for a v1 viewer, which still acks over dc.
	ctl *webrtc.DataChannel
	// in is the v2 input channel: viewer→agent mouse/keyboard/scroll events.
	// Reliable and ordered (unlike ctl and frames) — a dropped keyup or click
	// desyncs the remote input state, so this is the one channel that must not
	// shed messages. It exists to take input OUT of the billed WS relay: input
	// used to cross the Cloudflare Durable Object on every event, adding a full
	// relay round-trip to every click and keystroke — the dominant reason a
	// remote window felt remote rather than local. Nil for a v1 viewer, which
	// keeps sending input over WS.
	in        *webrtc.DataChannel
	v2        bool        // viewer speaks the split-channel protocol
	h264      bool        // viewer declared WebCodecs H.264 decode in its hello
	open      atomic.Bool // true once the DataChannel is ready to carry frames
	confirmed atomic.Bool // true once the viewer acked a frame OVER this channel
	// (proving big frames actually traverse it — small acks alone can't)

	mu         sync.Mutex
	openedAt   time.Time                 // when probing started (guarded by mu)
	haveRemote bool                      // remote description applied yet?
	pendingICE []webrtc.ICECandidateInit // candidates that arrived early
}

// rtcSendBudget bounds how many bytes may sit unsent in one peer's SCTP send
// queue before we stop handing it frames. This is the congestion signal the
// stream had none of: the encoder's bitrate is fixed at startup, dc.Send never
// blocks and never fails on a slow link, so pion buffered without limit and
// latency grew without bound until the ack loop declared the viewer dead.
// Dropping instead is what a video call does — the viewer sees a sequence gap
// and asks for a keyframe (submitAU → requestH264Key), which it already did for
// packets lost in flight. Sized at roughly a quarter-second of the encoder's
// 12 Mbps high-quality ceiling: deep enough to ride out a burst, shallow enough that the
// picture stays current rather than replaying the past.
const rtcSendBudget = 384 << 10

// sendFrame hands one frame message to this peer, unless its send queue is
// already over budget. Best effort by design: the viewer's gap detection is
// what turns a skipped frame back into a picture, so there is nothing useful
// for a caller to do about one that didn't go.
func (p *rtcPeer) sendFrame(raw []byte) {
	if p.dc == nil || p.dc.BufferedAmount() > rtcSendBudget {
		return
	}
	_ = p.dc.Send(raw)
}

// sendCtl delivers a small out-of-band message (currently only heartbeats;
// acks travel the other way), falling back to the frame channel.
//
// The fallback is not just for v1 viewers that have no ctl channel. A ctl
// channel that exists but is not open — still completing its handshake, or
// closed on its own while frames kept flowing — used to swallow the message
// silently, because a failed send returned rather than trying the other
// channel. Heartbeats are the ONLY liveness signal an idle window has once a
// peer is confirmed and the relay copy has been dropped, so losing them puts
// "Waiting for host — screen may be locked or asleep" on a pane whose host is
// perfectly awake. That is the exact symptom the split channel was introduced
// to cure, so it must not be able to cause it.
func (p *rtcPeer) sendCtl(raw []byte) {
	if c := p.ctl; c != nil && c.ReadyState() == webrtc.DataChannelStateOpen {
		if c.Send(raw) == nil {
			return
		}
	}
	if p.dc != nil {
		_ = p.dc.Send(raw)
	}
}

func (p *rtcPeer) startProbe() {
	p.mu.Lock()
	p.openedAt = time.Now()
	p.mu.Unlock()
}
func (p *rtcPeer) probeAge() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	return time.Since(p.openedAt)
}

// rtcFrameLifetimeMS is how long SCTP may keep trying to deliver one frame
// message on a v2 channel before abandoning it. Long enough for a fast
// retransmit or two on a typical link (a 50ms RTT gets ~3 attempts), short
// enough that a link which genuinely can't carry the bitrate sheds frames
// instead of building a queue the viewer would have to watch its way through.
const rtcFrameLifetimeMS = 150

// onRTCViewerMsg handles one viewer→agent DataChannel message: a frame ack,
// optionally carrying a keyframe request. viaFrames says it arrived on the
// frame channel, which is the only thing that can CONFIRM that channel — the
// point of confirmation is proof that full-size frames traverse it, and a
// small message on the separate ctl channel proves nothing about the other.
// A v2 viewer therefore stamps Frames on the acks it sends over ctl for a
// frame that arrived peer-to-peer, which is the same evidence by another road.
func (a *Agent) onRTCViewerMsg(peer *rtcPeer, data []byte, viaFrames bool) {
	// Runs in pion's read goroutine, outside every other recover, on
	// viewer-supplied data. Contain any panic so a crafted DataChannel message
	// can't crash the agent.
	defer func() {
		if r := recover(); r != nil {
			recoverLog("dc.OnMessage", r)
		}
	}()
	var ack struct {
		ID  string `json:"id"`
		Seq uint64 `json:"seq"`
		Key bool   `json:"key"` // viewer lost sync — send an IDR now
		DC  bool   `json:"dc"`  // v2: the acked frame arrived peer-to-peer
	}
	if json.Unmarshal(data, &ack) != nil || ack.ID == "" {
		return
	}
	if viaFrames || ack.DC {
		peer.confirmed.Store(true)
	}
	// A keyframe request over P2P used to be parsed into a struct that had no
	// Key field and silently dropped, so a viewer that lost sync on the
	// DataChannel waited out the encoder's periodic IDR instead of getting the
	// one it asked for. It matters far more now that frames are droppable.
	if ack.Key {
		a.requestWindowKey(ack.ID)
	}
	a.deliverWindowAck(ack.ID, ack.Seq)
}

// onRTCInput handles one viewer→agent input message off the peer-to-peer input
// channel. The payload is a plaintext windowInput JSON — DTLS on the
// DataChannel already gives it the same confidentiality and integrity the WS
// path gets from app-layer AES-GCM, so it needs no further decryption. It funnels
// into exactly the same injection path as a relayed window_input, minus the
// relay round-trip that made every click and keystroke feel remote.
func (a *Agent) onRTCInput(data []byte) {
	// Runs in pion's read goroutine on viewer-supplied bytes, outside every
	// other recover — contain any panic so a crafted message can't crash the
	// agent.
	defer func() {
		if r := recover(); r != nil {
			recoverLog("input.OnMessage", r)
		}
	}()
	// Copy: pion may reuse the message buffer once this returns, and the darwin
	// path forwards the bytes to the daemon asynchronously.
	a.applyInputPayload(append([]byte(nil), data...))
}

// signal payloads (the JSON inside each webrtc_* message's encrypted Data).
type rtcHelloMsg struct {
	Peer string `json:"peer"`
	// H264 declares the viewer can decode H.264 via WebCodecs, so frames on
	// this peer's DataChannel may be compressed video instead of JPEGs (7-10×
	// less bandwidth at the same quality). Old viewers omit it → JPEG forever.
	H264 bool `json:"h264,omitempty"`
	// V2 declares the viewer understands the split-channel transport: a
	// separate "ctl" channel for acks/keyframe requests, and a time-limited
	// unordered "frames" channel carrying the chunk-indexed winBinMagicV2
	// framing. The agent is the offerer, so it must not create a channel an
	// old viewer would mistake for the frame channel — hence the flag rather
	// than always offering both. Old viewers omit it and get exactly the
	// reliable ordered single channel they have always had.
	V2 bool `json:"v2,omitempty"`
	// Viewer is a STABLE per-tab id. Peer churns across renegotiation
	// attempts, so keying capability records by it left one stale record per
	// attempt — fine for "can anyone decode h264", useless for counting how
	// many viewers there are.
	Viewer string `json:"viewer,omitempty"`
	// Panes is how many window panes this viewer currently has open. A
	// pointer so "didn't say" is distinguishable from "said zero": an older
	// viewer omits it, and treating that as zero would drop the relay copy it
	// still depends on.
	Panes *int `json:"panes,omitempty"`
	// CapsOnly asks the agent to record the above and do nothing else. A plain
	// hello builds a whole PeerConnection and offer, so a viewer that just
	// wants to refresh its record could not repeat it without leaving a trail
	// of peers to reap.
	CapsOnly bool `json:"caps_only,omitempty"`
}
type rtcSDPMsg struct {
	Peer string          `json:"peer"`
	SDP  string          `json:"sdp"`
	ICE  []iceServerJSON `json:"ice,omitempty"` // agent→viewer on the offer only
}
type rtcICEMsg struct {
	Peer      string  `json:"peer"`
	Candidate string  `json:"candidate"`
	Mid       *string `json:"mid,omitempty"`
	Line      *uint16 `json:"line,omitempty"`
}

// handleWebRTCHello answers a viewer's offer request: build a PeerConnection
// with a frames DataChannel, create an offer, and send it back. ICE candidates
// are trickled as they're gathered.
func (a *Agent) handleWebRTCHello(conn *websocket.Conn, encData string) {
	// Runs in its own goroutine (go a.handleWebRTCHello), so a panic here escapes
	// runConnection's recover and would crash the agent. Contain it: a malformed
	// viewer hello must at worst drop this P2P attempt (WS fallback covers it).
	defer func() { _ = recover() }()
	plaintext, err := a.box.Decrypt(encData)
	if err != nil {
		return
	}
	var hello rtcHelloMsg
	if json.Unmarshal(plaintext, &hello) != nil || hello.Peer == "" {
		return
	}

	// Record the viewer's decode capability BEFORE anything can fail. A viewer
	// whose DataChannel never opens still reaches us over the relay, and it
	// still wants video — so capability tracking must not be tied to P2P
	// succeeding. Viewers re-hello periodically while unconnected, which keeps
	// this fresh (see viewerCapTTL).
	capKey := hello.Viewer // stable per tab; falls back for older viewers
	if capKey == "" {
		capKey = hello.Peer
	}
	a.noteViewerCap(capKey, hello.H264, hello.Panes)
	if hello.CapsOnly {
		return // a record refresh, not a connection request
	}

	pionICE, viewerICE := iceConfig()
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{ICEServers: pionICE})
	if err != nil {
		return // frames stay on the WS fallback
	}
	peer := &rtcPeer{id: hello.Peer, pc: pc, h264: hello.H264, v2: hello.V2}

	// The frame channel. A v1 viewer gets the reliable ordered channel it has
	// always had. A v2 viewer gets PARTIAL reliability instead: an access unit
	// that can't be delivered within rtcFrameLifetime is abandoned rather than
	// retransmitted forever, and delivery is unordered so a straggler never
	// holds up the frames behind it. Fully reliable ordered delivery is the
	// wrong contract for video — one lost packet stalled every subsequent
	// frame until SCTP's retransmit timer fired, which is exactly the
	// stutter-then-catch-up-burst a video call avoids by dropping instead.
	// Loss is already handled downstream: the viewer sees the sequence gap and
	// asks for a fresh IDR.
	dcInit := (*webrtc.DataChannelInit)(nil)
	if hello.V2 {
		ordered, lifetime := false, uint16(rtcFrameLifetimeMS)
		dcInit = &webrtc.DataChannelInit{Ordered: &ordered, MaxPacketLifeTime: &lifetime}
	}
	dc, err := pc.CreateDataChannel("frames", dcInit)
	if err != nil {
		_ = pc.Close()
		return
	}
	peer.dc = dc
	dc.OnOpen(func() { peer.startProbe(); peer.open.Store(true) })
	dc.OnClose(func() { peer.open.Store(false) })
	// A v1 viewer acks on the frame channel; a v2 viewer acks on ctl but may
	// still be probing, so accept acks here either way.
	dc.OnMessage(func(m webrtc.DataChannelMessage) { a.onRTCViewerMsg(peer, m.Data, true) })

	// The v2 control channel: acks, keyframe requests, heartbeats. Unreliable
	// and unordered — every message on it is either idempotent (an ack folds in
	// as a high-water mark) or self-coalescing (a keyframe request is a flag),
	// so retransmitting a stale one buys nothing and queueing it behind video
	// is what caused the problem this channel exists to fix.
	if hello.V2 {
		ordered, retransmits := false, uint16(0)
		ctl, cerr := pc.CreateDataChannel("ctl", &webrtc.DataChannelInit{Ordered: &ordered, MaxRetransmits: &retransmits})
		if cerr == nil {
			peer.ctl = ctl
			ctl.OnMessage(func(m webrtc.DataChannelMessage) { a.onRTCViewerMsg(peer, m.Data, false) })
		}

		// The input channel: reliable + ordered (the default), the opposite of
		// frames/ctl. Input must never be dropped or reordered — losing a keyup
		// leaves a modifier stuck down, and a click landing before its focus
		// move lands in the wrong place — so this is the one channel that keeps
		// SCTP's full-reliability contract. Each message is a plaintext
		// windowInput JSON (DTLS already protects it, exactly like frames), fed
		// straight into the same injection path the WS relay uses.
		in, ierr := pc.CreateDataChannel("input", nil)
		if ierr == nil {
			peer.in = in
			in.OnMessage(func(m webrtc.DataChannelMessage) { a.onRTCInput(m.Data) })
		}
	}

	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return // nil marks end-of-candidates
		}
		ci := c.ToJSON()
		a.sendWindowMsg(a.liveConn(), protocol.TypeWebRTCICE, rtcICEMsg{
			Peer: hello.Peer, Candidate: ci.Candidate, Mid: ci.SDPMid, Line: ci.SDPMLineIndex,
		})
	})
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		// Tear down ONLY on terminal states. "Disconnected" is transient — ICE
		// keepalives missed for a few seconds, routine on phones (WiFi power-
		// save, brief radio fades) — and normally self-heals back to
		// "connected"; killing the peer on it made every radio nap a full
		// relay-fallback + renegotiate cycle, flapping the transport with no
		// user-visible network change. While disconnected, the stream's own
		// liveness machinery covers us: acks dry up → frames demote to WS in
		// ~6s; the channel re-proves via a probe when connectivity returns; and
		// a blip that doesn't heal escalates to "failed", which lands here.
		if s == webrtc.PeerConnectionStateFailed || s == webrtc.PeerConnectionStateClosed {
			peer.open.Store(false)
			a.dropRTCPeer(hello.Peer, peer)
		}
	})

	offer, err := pc.CreateOffer(nil)
	if err != nil {
		_ = pc.Close()
		return
	}
	if err := pc.SetLocalDescription(offer); err != nil {
		_ = pc.Close()
		return
	}

	a.rtcMu.Lock()
	if a.rtcPeers == nil {
		a.rtcPeers = map[string]*rtcPeer{}
	}
	// Replace any prior connection for this peer id (viewer reconnected).
	if old := a.rtcPeers[hello.Peer]; old != nil {
		_ = old.pc.Close()
	}
	a.rtcPeers[hello.Peer] = peer
	a.rtcMu.Unlock()

	// Reap the peer if its DataChannel never opens (viewer never answered). Fire-
	// and-forget: if it opened, open.Load() is true and this no-ops; dropRTCPeer's
	// identity guard handles the case where a reconnect already replaced it.
	time.AfterFunc(rtcHandshakeTimeout, func() {
		if !peer.open.Load() {
			a.dropRTCPeer(hello.Peer, peer)
		}
	})

	a.sendWindowMsg(conn, protocol.TypeWebRTCOffer, rtcSDPMsg{Peer: hello.Peer, SDP: offer.SDP, ICE: viewerICE})
}

// handleWebRTCAnswer applies the viewer's answer to the matching connection and
// flushes any ICE candidates that arrived before it.
func (a *Agent) handleWebRTCAnswer(encData string) {
	plaintext, err := a.box.Decrypt(encData)
	if err != nil {
		return
	}
	var ans rtcSDPMsg
	if json.Unmarshal(plaintext, &ans) != nil || ans.Peer == "" {
		return
	}
	peer := a.rtcPeerByID(ans.Peer)
	if peer == nil {
		return
	}
	if err := peer.pc.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeAnswer, SDP: ans.SDP,
	}); err != nil {
		return
	}
	peer.mu.Lock()
	peer.haveRemote = true
	pending := peer.pendingICE
	peer.pendingICE = nil
	peer.mu.Unlock()
	for _, c := range pending {
		_ = peer.pc.AddICECandidate(c)
	}
}

// maxPendingICE caps candidates buffered before the answer arrives. Real ICE
// gathering yields a handful (host/srflx/relay per interface); the ceiling just
// stops a peer that trickles candidates but never answers from growing the buffer
// unbounded until its connection times out.
const maxPendingICE = 64

// handleWebRTCICE adds a trickled candidate, buffering it if the answer hasn't
// been applied yet (pion rejects candidates before the remote description).
func (a *Agent) handleWebRTCICE(encData string) {
	plaintext, err := a.box.Decrypt(encData)
	if err != nil {
		return
	}
	var m rtcICEMsg
	if json.Unmarshal(plaintext, &m) != nil || m.Peer == "" {
		return
	}
	peer := a.rtcPeerByID(m.Peer)
	if peer == nil {
		return
	}
	cand := webrtc.ICECandidateInit{Candidate: m.Candidate, SDPMid: m.Mid, SDPMLineIndex: m.Line}
	peer.mu.Lock()
	if !peer.haveRemote {
		if len(peer.pendingICE) < maxPendingICE {
			peer.pendingICE = append(peer.pendingICE, cand)
		}
		peer.mu.Unlock()
		return
	}
	peer.mu.Unlock()
	_ = peer.pc.AddICECandidate(cand)
}

func (a *Agent) rtcPeerByID(id string) *rtcPeer {
	a.rtcMu.Lock()
	defer a.rtcMu.Unlock()
	return a.rtcPeers[id]
}

// dropRTCPeer tears down a peer connection and forgets it — but only if it's
// STILL the current peer for id. A reconnecting viewer replaces the map entry
// under the same id and closes the old pc; that old pc's async state-change
// callback lands here, and without the identity guard it would evict the fresh
// peer that just took its place (killing the new P2P transport → WS fallback).
func (a *Agent) dropRTCPeer(id string, p *rtcPeer) {
	a.rtcMu.Lock()
	if a.rtcPeers[id] == p {
		delete(a.rtcPeers, id)
	}
	a.rtcMu.Unlock()
	_ = p.pc.Close()
}

// closeAllRTCPeers tears down every peer connection (e.g. last viewer left).
func (a *Agent) closeAllRTCPeers() {
	a.rtcMu.Lock()
	peers := a.rtcPeers
	a.rtcPeers = nil
	a.rtcMu.Unlock()
	for _, p := range peers {
		_ = p.pc.Close()
	}
}

// rtcSinks classifies open DataChannels into those confirmed to deliver frames
// (send frames over these exclusively — the relay is bypassed) and those still
// being probed (send over these AND WS until one proves itself). A channel that
// has been probed past rtcProbeWindow without ever confirming is closed here —
// it connected but can't carry frames (cellular MTU), so we stop probing it and
// let the viewer re-negotiate later. allH264 reports whether every CONFIRMED
// peer declared WebCodecs H.264 decode (vacuously true with none confirmed) —
// the gate for switching a stream from JPEG to compressed video.
func (a *Agent) rtcSinks() (confirmed, probing []*rtcPeer, allH264 bool) {
	allH264 = true
	a.rtcMu.Lock()
	var stale []*rtcPeer
	for id, p := range a.rtcPeers {
		switch {
		case !p.open.Load():
			// not ready yet
		case p.confirmed.Load():
			confirmed = append(confirmed, p)
			if !p.h264 {
				allH264 = false
			}
		case p.probeAge() < rtcProbeWindow:
			probing = append(probing, p)
		default:
			stale = append(stale, p)
			delete(a.rtcPeers, id)
		}
	}
	a.rtcMu.Unlock()
	for _, p := range stale {
		_ = p.pc.Close()
	}
	return confirmed, probing, allH264
}

// unconfirmRTC demotes every confirmed peer back to probing (resetting its probe
// clock), used when a confirmed channel goes quiet — frames revert to WS while
// the channel re-proves itself instead of streaming into a silent break.
func (a *Agent) unconfirmRTC() {
	a.rtcMu.Lock()
	defer a.rtcMu.Unlock()
	for _, p := range a.rtcPeers {
		if p.confirmed.Swap(false) {
			p.mu.Lock()
			p.openedAt = time.Now()
			p.mu.Unlock()
		}
	}
}

// viewerCapTTL bounds how long a viewer's announced capability and pane count
// are trusted without a refresh. Two constraints tie it to other numbers, and
// both are asserted by a test rather than left to whoever next tunes one:
//   - viewers re-announce at winCapAnnounceInterval, and this must span several
//     of those, so one lost announcement cannot expire a live record;
//   - streamNoWatcherTimeout must EXCEED it, so a record left behind by a
//     departed viewer always expires before it can complete a countdown to
//     reaping a stream someone is still watching. It has to outlive the viewer's re-announce
//
// cadence (~10s) but stay SHORT, because the failure mode is asymmetric: a
// stale record saying "can't decode H.264", left behind by a viewer that has
// since gone, holds every remaining viewer on JPEG until it expires. A viewer
// with a live DataChannel needs no record at all — its peer entry is
// authoritative and disappears the moment the channel does.
const viewerCapTTL = 90 * time.Second

// noteViewerCap records what a viewer said it can decode. Keyed by the peer id
// the viewer minted for this attempt — those churn across retries, which is
// fine: the decision below is "does ANY viewer lack H.264", not a headcount.
func (a *Agent) noteViewerCap(peer string, h264 bool, panes *int) {
	a.rtcMu.Lock()
	defer a.rtcMu.Unlock()
	if a.viewerCaps == nil {
		a.viewerCaps = map[string]viewerCap{}
	}
	now := time.Now()
	for id, c := range a.viewerCaps { // opportunistic sweep; the map stays tiny
		if now.Sub(c.seen) > viewerCapTTL {
			delete(a.viewerCaps, id)
		}
	}
	e := viewerCap{h264: h264, seen: now}
	if panes != nil {
		e.panes, e.panesKnown = *panes, true
	}
	a.viewerCaps[peer] = e
}

// idleViewerCount is how many connected viewers have positively told us they
// have no window pane open. They still count toward the relay's viewer total,
// which is what decides whether a relay copy of every frame is needed — so a
// viewer watching nothing used to force the whole stream onto the billed path
// at half the frame rate and a 200ms batching delay. Popping a window out hits
// this every time: the opener keeps its tab open with no panes, and having
// nothing to receive it can never confirm a DataChannel either, so the
// arithmetic said "someone here needs the relay" forever.
//
// Only viewers that REPORTED counts here. An older viewer says nothing, and
// assuming zero for it would cut off the relay copy it still depends on.
func (a *Agent) idleViewerCount() int {
	a.rtcMu.Lock()
	defer a.rtcMu.Unlock()
	now, n := time.Now(), 0
	for _, c := range a.viewerCaps {
		if now.Sub(c.seen) <= viewerCapTTL && c.panesKnown && c.panes == 0 {
			n++
		}
	}
	return n
}

// viewersCanH264 reports whether it is safe to put compressed video on the
// wire for EVERY viewer. The relay is a broadcast: one message reaches all of
// them, so a single viewer that can't decode H.264 means nobody gets it.
// Deliberately conservative — it requires positive evidence from at least one
// viewer and no evidence against from any.
func (a *Agent) viewersCanH264() bool {
	a.rtcMu.Lock()
	defer a.rtcMu.Unlock()
	now, any := time.Now(), false
	for _, c := range a.viewerCaps {
		if now.Sub(c.seen) > viewerCapTTL {
			continue
		}
		if !c.h264 {
			return false // an old or incapable viewer is present
		}
		any = true
	}
	for _, p := range a.rtcPeers { // a live peer is authoritative over any record
		if !p.open.Load() {
			continue
		}
		if !p.h264 {
			return false
		}
		any = true // so a P2P viewer needs no periodic re-announcement
	}
	return any
}

// forgetViewerCaps drops every recorded capability. Called when the last viewer
// leaves: records are keyed by a per-attempt id with no disconnect signal
// behind them, so departure is the one moment we can be certain none of them
// describe anybody still watching.
func (a *Agent) forgetViewerCaps() {
	a.rtcMu.Lock()
	a.viewerCaps = nil
	a.rtcMu.Unlock()
}

// viewerCap is one viewer's announced decode capability and when it said so.
type viewerCap struct {
	h264 bool
	seen time.Time
	// panes is how many window panes the viewer had open when it last said
	// so; panesKnown is false for a viewer that never reported, which must be
	// assumed to want frames.
	panes      int
	panesKnown bool
}
