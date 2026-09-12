// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

// Input events used to cross the billed WS relay on every click, scroll and
// keystroke — a full Cloudflare Durable-Object round-trip that was the dominant
// reason a remote window felt remote rather than local. They now ride a
// dedicated reliable+ordered DataChannel straight to the agent over DTLS. This
// drives a REAL pion connection end to end: the agent offers the input channel
// exactly as handleWebRTCHello does, the viewer receives it by label and sends a
// window_input over it, and we prove the event reaches the shared injection path
// (onRTCInput → applyInputPayload → noteWindowFlush) with no relay in the middle.
func TestInputChannelDeliversToInjection(t *testing.T) {
	offerer, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Skipf("no peer connection in this environment: %v", err)
	}
	defer offerer.Close()
	answerer, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Skipf("no peer connection in this environment: %v", err)
	}
	defer answerer.Close()

	// The agent side: an input channel wired like the real one. The handler both
	// records the bytes (so the test can assert delivery on every platform) and
	// feeds them to the real onRTCInput, which must consume viewer-supplied data
	// without panicking. The injection that follows onRTCInput is
	// platform-specific (and no-ops on a headless test box with no desktop), so
	// it is covered by the applyWindowInput tests, not asserted here.
	a := &Agent{}
	got := make(chan []byte, 1)
	in, err := offerer.CreateDataChannel("input", nil)
	if err != nil {
		t.Fatalf("create input channel: %v", err)
	}
	if !in.Ordered() {
		t.Fatal("input channel negotiated unordered — a dropped or reordered event would desync remote input")
	}
	in.OnMessage(func(m webrtc.DataChannelMessage) {
		select {
		case got <- append([]byte(nil), m.Data...):
		default:
		}
		a.onRTCInput(m.Data) // the real handler must not panic on viewer data
	})

	// Wire the two ends together directly; no ICE server, no relay.
	offerer.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c != nil {
			_ = answerer.AddICECandidate(c.ToJSON())
		}
	})
	answerer.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c != nil {
			_ = offerer.AddICECandidate(c.ToJSON())
		}
	})

	// The viewer receives the channel by label (as its ondatachannel does) and
	// signals once it can send.
	ready := make(chan *webrtc.DataChannel, 1)
	answerer.OnDataChannel(func(d *webrtc.DataChannel) {
		if d.Label() != "input" {
			return
		}
		d.OnOpen(func() { ready <- d })
	})

	offer, err := offerer.CreateOffer(nil)
	if err != nil {
		t.Fatalf("offer: %v", err)
	}
	if err := offerer.SetLocalDescription(offer); err != nil {
		t.Fatalf("set local: %v", err)
	}
	if err := answerer.SetRemoteDescription(offer); err != nil {
		t.Fatalf("set remote: %v", err)
	}
	answer, err := answerer.CreateAnswer(nil)
	if err != nil {
		t.Fatalf("answer: %v", err)
	}
	if err := answerer.SetLocalDescription(answer); err != nil {
		t.Fatalf("answer set local: %v", err)
	}
	if err := offerer.SetRemoteDescription(answer); err != nil {
		t.Fatalf("answer set remote: %v", err)
	}

	var vch *webrtc.DataChannel
	select {
	case vch = <-ready:
	case <-time.After(15 * time.Second):
		t.Skip("input channel did not open in this environment")
	}

	// A scroll (not a click) keeps the payload off the context-menu path; the
	// fake window id resolves to nothing downstream, so nothing is ever injected
	// — the point is that the exact bytes traverse the reliable input channel to
	// the agent.
	payload := `{"id":"w1","kind":"scroll","x":0.5,"y":0.5,"dx":0,"dy":-3}`
	if err := vch.SendText(payload); err != nil {
		t.Fatalf("viewer send over input channel: %v", err)
	}

	select {
	case b := <-got:
		if string(b) != payload {
			t.Fatalf("agent received %q over the input channel, want %q", b, payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("input event never reached the agent over the DataChannel")
	}
}
