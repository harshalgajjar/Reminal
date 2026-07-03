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

// rtcPeer is one viewer's peer connection and its frame DataChannel.
type rtcPeer struct {
	id        string
	pc        *webrtc.PeerConnection
	dc        *webrtc.DataChannel
	open      atomic.Bool // true once the DataChannel is ready to carry frames
	confirmed atomic.Bool // true once the viewer acked a frame OVER this channel
	// (proving big frames actually traverse it — small acks alone can't)

	mu         sync.Mutex
	openedAt   time.Time                 // when probing started (guarded by mu)
	haveRemote bool                      // remote description applied yet?
	pendingICE []webrtc.ICECandidateInit // candidates that arrived early
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

// signal payloads (the JSON inside each webrtc_* message's encrypted Data).
type rtcHelloMsg struct {
	Peer string `json:"peer"`
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
	plaintext, err := a.box.Decrypt(encData)
	if err != nil {
		return
	}
	var hello rtcHelloMsg
	if json.Unmarshal(plaintext, &hello) != nil || hello.Peer == "" {
		return
	}

	pionICE, viewerICE := iceConfig()
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{ICEServers: pionICE})
	if err != nil {
		return // frames stay on the WS fallback
	}
	peer := &rtcPeer{id: hello.Peer, pc: pc}

	// Reliable, ordered channel — same delivery guarantees as the WS path, so
	// the viewer's render/ack loop is unchanged.
	dc, err := pc.CreateDataChannel("frames", nil)
	if err != nil {
		_ = pc.Close()
		return
	}
	peer.dc = dc
	dc.OnOpen(func() { peer.startProbe(); peer.open.Store(true) })
	dc.OnClose(func() { peer.open.Store(false) })
	dc.OnMessage(func(m webrtc.DataChannelMessage) {
		// The only viewer→agent traffic on this channel is frame acks. Because
		// the viewer acks a frame over the SAME transport it arrived on, an ack
		// here proves a full frame really traversed this channel — so it's now
		// safe to send frames over it exclusively.
		peer.confirmed.Store(true)
		var ack struct {
			ID  string `json:"id"`
			Seq uint64 `json:"seq"`
		}
		if json.Unmarshal(m.Data, &ack) == nil && ack.ID != "" {
			a.deliverWindowAck(ack.ID, ack.Seq)
		}
	})

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
		if s == webrtc.PeerConnectionStateFailed ||
			s == webrtc.PeerConnectionStateClosed ||
			s == webrtc.PeerConnectionStateDisconnected {
			peer.open.Store(false)
			a.dropRTCPeer(hello.Peer)
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
		peer.pendingICE = append(peer.pendingICE, cand)
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

// dropRTCPeer tears down and forgets a peer connection.
func (a *Agent) dropRTCPeer(id string) {
	a.rtcMu.Lock()
	peer := a.rtcPeers[id]
	delete(a.rtcPeers, id)
	a.rtcMu.Unlock()
	if peer != nil {
		_ = peer.pc.Close()
	}
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
// let the viewer re-negotiate later.
func (a *Agent) rtcSinks() (confirmed, probing []*webrtc.DataChannel) {
	a.rtcMu.Lock()
	var stale []*rtcPeer
	for id, p := range a.rtcPeers {
		switch {
		case !p.open.Load():
			// not ready yet
		case p.confirmed.Load():
			confirmed = append(confirmed, p.dc)
		case p.probeAge() < rtcProbeWindow:
			probing = append(probing, p.dc)
		default:
			stale = append(stale, p)
			delete(a.rtcPeers, id)
		}
	}
	a.rtcMu.Unlock()
	for _, p := range stale {
		_ = p.pc.Close()
	}
	return confirmed, probing
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
