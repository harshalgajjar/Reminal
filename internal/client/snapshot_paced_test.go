// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"

	"reminal/internal/protocol"
)

// pacedRig runs runSender for a with one viewer attached, over a real
// websocket pair, and hands over every message the "relay" receives.
type pacedRig struct {
	a        *Agent
	cursorCh chan uint64
	msgs     chan protocol.Message
}

func startPacedRig(t *testing.T, a *Agent) *pacedRig {
	t.Helper()
	r := &pacedRig{a: a, cursorCh: make(chan uint64, 4), msgs: make(chan protocol.Message, 4096)}
	a.viewerCount = 1
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, req, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var m protocol.Message
			if json.Unmarshal(raw, &m) == nil {
				r.msgs <- m
			}
		}
	}))
	t.Cleanup(ts.Close)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { _ = a.runSender(conn, r.cursorCh, stop, false); close(done) }()
	t.Cleanup(func() { close(stop); <-done })
	return r
}

// join is a paced viewer's resume from nothing.
func (r *pacedRig) join(viewer string) {
	r.a.noteSnapshotJoiner(viewer, snapCapParts, true, 0)
	r.cursorCh <- 0
}

// part opens a snapshot_part.
func (r *pacedRig) part(t *testing.T, m protocol.Message) (partHeader, []byte) {
	t.Helper()
	if m.Type != protocol.TypeSnapshotPart {
		t.Fatalf("got %s (seq %d), want a snapshot part", m.Type, m.Seq)
	}
	pt, err := r.a.box.Decrypt(m.Data)
	if err != nil {
		t.Fatal(err)
	}
	hl := binary.BigEndian.Uint32(pt)
	var h partHeader
	if err := json.Unmarshal(pt[4:4+hl], &h); err != nil {
		t.Fatal(err)
	}
	return h, pt[4+hl:]
}

// A paced join: the screen alone first, then the history two parts ahead of
// the viewer's acknowledgements and no further, live output going out between
// them — never behind more than the one part the window allows per round —
// and, once in, a history that is all of it.
func TestAPacedSnapshotSendsTheScreenFirstAndTheHistoryAsItIsAcknowledged(t *testing.T) {
	a := colouredSession(t, 4000, 8<<20)
	r := startPacedRig(t, a)
	r.join("v-test")

	m := <-r.msgs
	h, screen := r.part(t, m)
	if h.Resnap {
		t.Fatalf("a join's screen is marked a repaint: %+v", h)
	}
	if !h.Screen || !h.More || h.To != "v-test" || h.ID == "" || m.Seq != a.buf.LatestSeq() {
		t.Fatalf("first: %+v seq %d (latest %d), want the screen with more to come", h, m.Seq, a.buf.LatestSeq())
	}
	if want := paintSnapshot(viewOf(a.screen), nil, nil, 0, false); string(screen) != want {
		t.Fatalf("the screen part is not the screen alone (%d B, want %d B)", len(screen), len(want))
	}
	if n := numberedLines(string(screen)); n > 40 {
		t.Fatalf("the screen part carries %d lines: history came with it", n)
	}
	id, seq := h.ID, m.Seq

	var gz bytes.Buffer
	n := -1
	take := func(want int) {
		t.Helper()
		ph, body := r.part(t, <-r.msgs)
		if ph.Screen || !ph.Ack || ph.ID != id || ph.I != want || ph.To != "v-test" {
			t.Fatalf("part %+v, want paced part %d of %s", ph, want, id)
		}
		if len(body) > snapshotPacedBytes {
			t.Fatalf("part %d carries %d B", want, len(body))
		}
		n = ph.N
		gz.Write(body)
	}
	// The window: two parts, then nothing more until an acknowledgement —
	// what comes next is the live output recorded meanwhile.
	take(0)
	take(1)
	if n < 4 {
		t.Fatalf("%d parts: too few to show the pacing", n)
	}
	live := func() {
		t.Helper()
		a.record([]byte("typed\r\n"))
		m := <-r.msgs
		if m.Type != protocol.TypeData || m.Seq != a.buf.LatestSeq() {
			t.Fatalf("got %s seq %d, want the live output (seq %d) before any unacknowledged part", m.Type, m.Seq, a.buf.LatestSeq())
		}
	}
	live()
	// Acknowledgements that are not this one's change nothing.
	a.ackSnapshot("v-other", id, 1)
	a.ackSnapshot("v-test", "not-it", 1)
	a.ackSnapshot("v-test", id, 3) // more than it was sent
	live()
	for i := 2; i < n; i++ {
		a.ackSnapshot("v-test", id, uint64(i-1))
		take(i)
		live()
	}
	a.snapMu.Lock()
	left := len(a.snapXfers)
	a.snapMu.Unlock()
	if left != 0 {
		t.Fatalf("%d transfers kept after the last part", left)
	}
	zr, err := gzip.NewReader(&gz)
	if err != nil {
		t.Fatal(err)
	}
	whole, _ := io.ReadAll(zr)
	if got := numberedLines(string(whole)); got != 4000 {
		t.Fatalf("the history carries %d/4000 lines", got)
	}
	if seq == 0 {
		t.Fatal("no seq on the snapshot")
	}
}

// Nothing in the scrollback: the screen is the whole of it, and no parts
// follow (a viewer would otherwise keep its output for a history to come).
func TestAPacedSnapshotWithNoHistoryIsTheScreenAlone(t *testing.T) {
	a := colouredSession(t, 5, 0)
	r := startPacedRig(t, a)
	r.join("v-test")
	h, _ := r.part(t, <-r.msgs)
	if !h.Screen || h.More {
		t.Fatalf("%+v, want the screen and nothing to follow", h)
	}
	a.record([]byte("typed\r\n"))
	if m := <-r.msgs; m.Type != protocol.TypeData {
		t.Fatalf("after the screen: %s, want the live output", m.Type)
	}
}

// What is kept for a paced snapshot is let go: when that viewer resumes again
// (its earlier connection's), when the last viewer leaves, and past
// snapshotXfersMax at once (the oldest).
func TestPacedSnapshotsAreLetGo(t *testing.T) {
	a := colouredSession(t, 10, 0)
	count := func() int {
		a.snapMu.Lock()
		defer a.snapMu.Unlock()
		return len(a.snapXfers)
	}
	add := func(v string) {
		a.snapMu.Lock()
		a.snapXfers = append(a.snapXfers, &snapXfer{viewer: v, id: "x", n: 9})
		a.snapMu.Unlock()
	}
	add("v1")
	add("v2")
	a.noteSnapshotJoiner("v1", snapCapParts, true, a.buf.NextSeq()) // a resume with nothing owed
	if count() != 1 {
		t.Fatalf("after v1 resumed: %d kept, want 1", count())
	}
	a.dropSnapshotXfers()
	if count() != 0 {
		t.Fatalf("after the last viewer left: %d kept", count())
	}

	// The bound, through startPacedSnapshot: one more than the most, on a
	// session with a history to send.
	a = colouredSession(t, 2000, 0)
	r := startPacedRig(t, a)
	for i := 0; i <= snapshotXfersMax; i++ {
		v := "v" + string(rune('a'+i))
		r.join(v)
		for {
			if h, _ := r.part(t, <-r.msgs); h.Screen && h.To == v {
				break
			}
		}
	}
	// The sender has started the last; wait for its window to have gone
	// (its two parts), so the list is as it will stay.
	seen := 0
	for seen < 2 {
		if h, _ := r.part(t, <-r.msgs); h.To == "v"+string(rune('a'+snapshotXfersMax)) && h.Ack {
			seen++
		}
	}
	a.snapMu.Lock()
	var kept []string
	for _, x := range a.snapXfers {
		kept = append(kept, x.viewer)
	}
	a.snapMu.Unlock()
	if len(kept) > snapshotXfersMax || len(kept) == 0 || kept[0] == "va" {
		t.Fatalf("kept %v: want at most %d, the oldest (va) gone", kept, snapshotXfersMax)
	}
}
