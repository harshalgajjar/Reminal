// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"reminal/internal/crypto"
	"reminal/internal/protocol"
)

// A PTY resize is sent to a viewer in its place in the output stream: the
// output before it, then the size, then the output the program wrote for
// that size. Told beside the stream, the size could reach a viewer after
// that output, and a phone turned back to landscape had its grid grow
// under a half-written Claude Code frame (one word a row, stepping right).
// And a viewer's stream begins with the PTY's size as it is: a viewer that
// has just (re)connected knows none, falls back to its own width, and the
// agent tells a size only when it changes, so a snapshot laid out for 120
// columns landed in a 90-column grid before any size ever came.
func TestAResizeIsSentInItsPlaceInTheStream(t *testing.T) {
	box, _ := crypto.NewBox(make([]byte, 32))
	a := &Agent{box: box, buf: newScrollback(1 << 16)}
	a.sizeBook.setApplied(102, 49)
	agentEnd, viewer := wsPair(t)
	a.buf.Append("before")
	a.buf.AppendResize(102, 49)
	a.buf.Append("after")

	cursorCh := make(chan uint64, 1)
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	// A viewer attached here (local): one through the relay is sent nothing
	// until the relay counts it.
	go func() { _ = a.runSender(agentEnd, cursorCh, stop, true) }()
	cursorCh <- 1

	var got []string
	for len(got) < 4 {
		_ = viewer.SetReadDeadline(time.Now().Add(5 * time.Second))
		var m protocol.Message
		if err := viewer.ReadJSON(&m); err != nil {
			t.Fatalf("after %v: %v", got, err)
		}
		switch m.Type {
		case protocol.TypeData:
			got = append(got, "data:"+m.Data)
		case protocol.TypeResize:
			pt, err := box.Decrypt(m.Data)
			if err != nil {
				t.Fatalf("resize: %v", err)
			}
			var p struct{ Cols, Rows uint16 }
			_ = json.Unmarshal(pt, &p)
			got = append(got, fmt.Sprintf("resize:%dx%d@%d", p.Cols, p.Rows, m.Seq))
		}
	}
	if want := []string{"resize:102x49@0", "data:before", "resize:102x49@2", "data:after"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("the viewer got %v, want %v", got, want)
	}
}

// A PTY resize is told to the relay only in its place in the stream: a size
// sent beside it could overtake output still queued for that socket (behind
// a paced snapshot's part) and put old-width bytes in the new grid. Viewers
// attached here are still told beside it.
func TestAResizeIsNotToldToTheRelayBesideTheStream(t *testing.T) {
	box, _ := crypto.NewBox(make([]byte, 32))
	a := &Agent{box: box}
	relayEnd, relay := wsPair(t)
	localEnd, local := wsPair(t)
	a.currentConn = relayEnd
	a.addLocalConn(localEnd)

	a.tellLocalViewers(90, 40)

	_ = local.SetReadDeadline(time.Now().Add(5 * time.Second))
	var m protocol.Message
	if err := local.ReadJSON(&m); err != nil || m.Type != protocol.TypeResize {
		t.Fatalf("the viewer attached here: %v %v, want the size", m.Type, err)
	}
	_ = relay.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if err := relay.ReadJSON(&m); err == nil {
		t.Fatalf("the relay was sent %s beside the stream", m.Type)
	}
}
