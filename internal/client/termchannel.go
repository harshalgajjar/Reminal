// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

// The terminal on the direct channel.
//
// A session's terminal went through the relay alone, both ways, also when
// the viewer and the machine were on one network and joined by a WebRTC
// channel that carried its window frames: every key and its echo took the
// round trip to the relay. With a viewer that asks for it (rtcHelloMsg.Term)
// the agent opens one more data channel, "term", ordered and reliable, and
// the terminal goes on it AS WELL as on the relay:
//
//   - output: each entry the sender writes to the relay is also sent on the
//     channel, with the entry's own sequence number. The viewer takes the
//     copy that comes first and drops the other by that number, as it has
//     always dropped a repeated entry.
//   - input: the viewer numbers what is typed (per handshake stream, from 1)
//     and sends each piece on the channel and, sealed as ever with the
//     number as its sequence, on the relay. typeTermInput types the first
//     copy of each number and drops the second.
//
// The relay's copy is never left out, so nothing can be lost: with the
// channel slow, closed or gone, the terminal is what it was before. What
// goes on the channel is protected as its frames and window input are: by
// the channel's own encryption, between the two ends that exchanged its
// fingerprints inside the sealed session. Input is sent encrypted with the
// session key besides, as on the relay.

import (
	"encoding/base64"
	"encoding/json"
	"sync"

	"github.com/pion/webrtc/v4"
)

// termSendBudget is how many bytes may wait unsent on a peer's term channel
// before output is left to the relay's copy alone: a channel that is not
// keeping up must not grow a queue.
const termSendBudget = 256 << 10

// termStreamsKept bounds how many viewers' input counts are remembered.
const termStreamsKept = 1024

// termInMsg is one piece of input on the channel: its viewer's handshake
// stream, its number on that stream, and the bytes, encrypted with the
// session key.
type termInMsg struct {
	Stream uint32 `json:"s"`
	N      uint64 `json:"n"`
	Data   string `json:"data"`
}

// termOutMsg is one entry of output on the channel: its sequence number and
// its bytes (base64).
type termOutMsg struct {
	Seq  uint64 `json:"seq"`
	Data string `json:"data"`
}

// termOrder is the newest numbered input typed, by handshake stream.
type termOrder struct {
	mu   sync.Mutex
	last map[uint32]uint64
}

// take reports whether input n of stream is to be typed now, and counts it
// if so. Each number is typed once, in order: one already typed is the other
// path's copy. One ahead of the next can only be the relay's (both paths
// keep their order, and the relay's copy is never left out): on the channel
// it is left for the relay to bring, on the relay it is typed.
func (o *termOrder) take(stream uint32, n uint64, direct bool) bool {
	if o.last == nil {
		o.last = map[uint32]uint64{}
	}
	last, known := o.last[stream]
	if n <= last || (direct && n != last+1) {
		return false
	}
	if !known && len(o.last) >= termStreamsKept {
		// Viewers long gone: forgotten together. One still typing starts
		// again from the number it is at (its relay copy is typed).
		o.last = map[uint32]uint64{}
	}
	o.last[stream] = n
	return true
}

// typeTermInput types one numbered piece of input, from the relay (direct
// false) or the channel, unless its other copy was typed already. The lock
// is held while it is typed, so the two paths cannot type out of order.
func (a *Agent) typeTermInput(stream uint32, n uint64, enc string, direct bool) error {
	data, err := a.box.Decrypt(enc)
	if err != nil {
		return nil
	}
	a.termIn.mu.Lock()
	defer a.termIn.mu.Unlock()
	if !a.termIn.take(stream, n, direct) {
		return nil
	}
	a.noteInput(data)
	_, err = a.typeIn(data)
	return err
}

// onRTCTerm takes one message off a peer's term channel: input, from a
// viewer whose handshake stream this agent issued.
func (a *Agent) onRTCTerm(data []byte) {
	// Runs in pion's read goroutine on viewer-supplied bytes: a crafted
	// message must not crash the agent.
	defer func() {
		if r := recover(); r != nil {
			recoverLog("term.OnMessage", r)
		}
	}()
	var in termInMsg
	if json.Unmarshal(data, &in) != nil || in.N == 0 || in.Data == "" || !a.streamKnown(in.Stream) {
		return
	}
	// A restored session's replay still under way: input waits for it, as
	// the relay's does.
	if !a.waitReplayed(nil) {
		return
	}
	_ = a.typeTermInput(in.Stream, in.N, in.Data, true)
}

// streamKnown reports whether stream is one a handshake of this agent's
// issued.
func (a *Agent) streamKnown(stream uint32) bool {
	a.seal.mu.Lock()
	defer a.seal.mu.Unlock()
	_, ok := a.seal.streams[stream]
	return ok
}

// termPeers are the peers whose term channel is open.
func (a *Agent) termPeers() []*rtcPeer {
	a.rtcMu.Lock()
	defer a.rtcMu.Unlock()
	var out []*rtcPeer
	for _, p := range a.rtcPeers {
		if t := p.term; t != nil && t.ReadyState() == webrtc.DataChannelStateOpen {
			out = append(out, p)
		}
	}
	return out
}

// sendTerm sends one output entry (enc: its bytes under the session key, as
// the scrollback holds it) on every open term channel that is keeping up.
// Best effort: the relay has the entry too.
func (a *Agent) sendTerm(seq uint64, enc string) {
	peers := a.termPeers()
	if len(peers) == 0 {
		return
	}
	data, err := a.box.Decrypt(enc)
	if err != nil {
		return
	}
	raw, err := json.Marshal(termOutMsg{Seq: seq, Data: base64.StdEncoding.EncodeToString(data)})
	if err != nil {
		return
	}
	for _, p := range peers {
		if p.term.BufferedAmount() > termSendBudget {
			continue
		}
		_ = p.term.SendText(string(raw))
	}
}
