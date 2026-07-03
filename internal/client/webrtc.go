// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"encoding/json"
	"sync"
	"sync/atomic"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"
	"github.com/reminal/reminal/internal/protocol"
)

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

// rtcSTUN is a public STUN server used to discover each peer's public address
// for NAT traversal over the internet. On a LAN, host candidates connect
// directly without it; when peers can't punch through, a TURN server would be
// needed (not configured yet — such viewers just stay on the WS fallback).
var rtcICEServers = []webrtc.ICEServer{
	{URLs: []string{"stun:stun.l.google.com:19302"}},
}

// rtcPeer is one viewer's peer connection and its frame DataChannel.
type rtcPeer struct {
	id   string
	pc   *webrtc.PeerConnection
	dc   *webrtc.DataChannel
	open atomic.Bool // true once the DataChannel is ready to carry frames

	mu         sync.Mutex
	haveRemote bool                      // remote description applied yet?
	pendingICE []webrtc.ICECandidateInit // candidates that arrived early
}

// signal payloads (the JSON inside each webrtc_* message's encrypted Data).
type rtcHelloMsg struct {
	Peer string `json:"peer"`
}
type rtcSDPMsg struct {
	Peer string `json:"peer"`
	SDP  string `json:"sdp"`
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

	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{ICEServers: rtcICEServers})
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
	dc.OnOpen(func() { peer.open.Store(true) })
	dc.OnClose(func() { peer.open.Store(false) })
	dc.OnMessage(func(m webrtc.DataChannelMessage) {
		// The only viewer→agent traffic on this channel is frame acks.
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

	a.sendWindowMsg(conn, protocol.TypeWebRTCOffer, rtcSDPMsg{Peer: hello.Peer, SDP: offer.SDP})
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

// rtcFrameSinks returns the DataChannels of every peer ready to receive frames.
// streamWindow sends frames over these instead of the relay when any exist.
func (a *Agent) rtcFrameSinks() []*webrtc.DataChannel {
	a.rtcMu.Lock()
	defer a.rtcMu.Unlock()
	var out []*webrtc.DataChannel
	for _, p := range a.rtcPeers {
		if p.open.Load() {
			out = append(out, p.dc)
		}
	}
	return out
}
