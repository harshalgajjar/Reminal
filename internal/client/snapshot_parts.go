// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"slices"

	"github.com/gorilla/websocket"

	"reminal/internal/protocol"
)

// A joining viewer's snapshot in parts. The plain snapshot is one TypeData
// frame, so its history is cut to what fits the relay's 1 MiB message once
// encrypted (maxSnapshotPlaintext): a viewer opening a long session saw its
// newest ~1,200 lines, while one already open had them all. A viewer that
// says it can (Snap "gz+parts" on its resume) is sent the whole history the
// agent keeps for snapshots (scrollbackBytes), gzip-compressed and split into
// parts that each fit a message, addressed to it alone: agent messages reach
// every viewer, and a part — not a self-contained paint — must reach only the
// viewer assembling it. The address is inside the encrypted part (its
// header's "to"), not on the message: a sealed frame carries no more than its
// type, counters, seq and payload. It paints once, with every part in.

const (
	// snapCapParts is the capability a viewer's resume names.
	snapCapParts = "gz+parts"
	// snapshotPartBytes is a part's share of the gzip stream: encrypted and
	// base64'd (4/3, plus nonce, tag and header) it stays under 640 KiB.
	snapshotPartBytes = 470 << 10
)

// noteSnapshotJoiner records, from a resume, who will be owed a snapshot and
// how it can take one: only a resume whose cursor is older than the buffer
// gets one (runSender). paced: the viewer paints the screen first and
// acknowledges the parts (SnapAck). Any resume ends what was being paced to
// that viewer: it belonged to the connection the viewer had before.
func (a *Agent) noteSnapshotJoiner(viewer, snap string, paced bool, cursor uint64) {
	a.snapMu.Lock()
	defer a.snapMu.Unlock()
	if viewer != "" {
		a.snapXfers = slices.DeleteFunc(a.snapXfers, func(x *snapXfer) bool { return x.viewer == viewer })
	}
	if a.buf == nil || cursor > a.buf.OldestSeq() {
		return
	}
	if snap == snapCapParts && viewer != "" {
		if a.snapJoiners == nil {
			a.snapJoiners = map[string]bool{}
		}
		a.snapJoiners[viewer] = paced
		return
	}
	a.snapPlain = true
}

// takeSnapshotJoiners is who to send parts to for the snapshot now owed (all
// at once, and paced), and clears the record. None when anyone owed it cannot
// take parts: the plain frame, broadcast, serves every joiner, those that
// could take parts too.
func (a *Agent) takeSnapshotJoiners() (whole, paced []string) {
	a.snapMu.Lock()
	defer a.snapMu.Unlock()
	joiners, plain := a.snapJoiners, a.snapPlain
	a.snapJoiners, a.snapPlain = nil, false
	if plain || len(joiners) == 0 {
		return nil, nil
	}
	for v, p := range joiners {
		if p {
			paced = append(paced, v)
		} else {
			whole = append(whole, v)
		}
	}
	return whole, paced
}

// snapshotParts builds the snapshot (history within scrollbackBytes), gzips
// it and cuts it into parts, encrypted for each joiner (its id in each part's
// header), and returns them by joiner with the seq they cover. None: nothing
// to send.
func (a *Agent) snapshotParts(joiners []string) (map[string][]string, uint64, error) {
	if a.box == nil || len(joiners) == 0 {
		return nil, 0, nil
	}
	// As snapshotFrame: only a copy under screenMu, the rest outside it
	// (joinInputs, joinHistory).
	a.screenMu.Lock()
	if a.screen == nil {
		a.screenMu.Unlock()
		return nil, 0, nil
	}
	in := a.joinInputs()
	if a.dropFailedScreenLocked() {
		a.screenMu.Unlock()
		return nil, 0, nil
	}
	latest := a.buf.LatestSeq()
	a.screenMu.Unlock()
	history := a.joinHistory(in)
	snap := paintSnapshot(in.view, history, nil, a.snapshotBudget(), false)
	if snap == "" {
		return nil, 0, nil
	}
	body, err := gzipBytes(snap)
	if err != nil {
		return nil, 0, err
	}
	id := newSnapshotID()
	n := (len(body) + snapshotPartBytes - 1) / snapshotPartBytes
	out := make(map[string][]string, len(joiners))
	for _, to := range joiners {
		parts := make([]string, 0, n)
		for i := 0; i < n; i++ {
			chunk := body[i*snapshotPartBytes : min((i+1)*snapshotPartBytes, len(body))]
			enc, err := a.sealPart(partHeader{ID: id, I: i, N: n, To: to}, chunk)
			if err != nil {
				return nil, 0, err
			}
			parts = append(parts, enc)
		}
		out[to] = parts
	}
	return out, latest, nil
}

// snapshotBudget is the configured cap on a snapshot's history
// (REMINAL_SCROLLBACK_BYTES; 0: none, the line cap only) — not the one-frame
// cap the plain snapshot is held to.
func (a *Agent) snapshotBudget() int {
	if a.scrollbackBytes <= 0 {
		return 1 << 40
	}
	return a.scrollbackBytes
}

func gzipBytes(s string) ([]byte, error) {
	var gz bytes.Buffer
	w, _ := gzip.NewWriterLevel(&gz, gzip.BestSpeed)
	if _, err := w.Write([]byte(s)); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return gz.Bytes(), nil
}

func newSnapshotID() string {
	var idb [8]byte
	_, _ = rand.Read(idb[:])
	return hex.EncodeToString(idb[:])
}

// partHeader heads a snapshot part, inside its encryption. Ack: a paced part,
// to be acknowledged. Screen: not a part but the screen alone, painted first,
// with More saying whether the history's parts follow.
type partHeader struct {
	ID     string `json:"id"`
	I      int    `json:"i"`
	N      int    `json:"n"`
	To     string `json:"to"`
	Ack    bool   `json:"ack,omitempty"`
	Screen bool   `json:"screen,omitempty"`
	More   bool   `json:"more,omitempty"`
}

// sealPart encrypts [header length][header][body].
func (a *Agent) sealPart(h partHeader, body []byte) (string, error) {
	hdr, _ := json.Marshal(h)
	pt := make([]byte, 4, 4+len(hdr)+len(body))
	binary.BigEndian.PutUint32(pt, uint32(len(hdr)))
	pt = append(append(pt, hdr...), body...)
	return a.box.Encrypt(pt)
}

// sendSnapshotParts sends the snapshot in parts to each joiner, and returns
// the seq it covers (0: nothing sent).
func (a *Agent) sendSnapshotParts(conn *websocket.Conn, joiners []string) (uint64, error) {
	byJoiner, latest, err := a.snapshotParts(joiners)
	if err != nil || len(byJoiner) == 0 {
		return 0, err
	}
	for _, v := range joiners {
		for _, p := range byJoiner[v] {
			if err := a.writeMsg(conn, protocol.Message{Type: protocol.TypeSnapshotPart, Seq: latest, Data: p}); err != nil {
				return 0, err
			}
		}
	}
	return latest, nil
}

// Paced: a viewer that paints the screen first (SnapAck) is sent the screen
// alone at once — a few KB, addressed to it — and can type from there. Its
// history follows in small parts, each sent only when the viewer has
// acknowledged all but snapshotWindow-1 of those before it, from the same
// loop that sends the live output (runSender): so the output, and the echo
// of what the person types, waits behind at most a part, never behind the
// whole history. When every part is in, the viewer paints once more: the
// history, the screen as it was when the snapshot was taken, and the output
// since, which it has kept.
//
// A part not acknowledged stalls only that viewer's history: live output
// goes on regardless, and a resume (a reconnect) starts over. What is kept
// per viewer is the compressed history and a count; it goes when the last
// part is sent, when that viewer resumes again, when the last viewer leaves,
// or when more than snapshotXfersMax are under way (the oldest first).
const (
	// snapshotPacedBytes is a paced part's share of the gzip stream. On the
	// wire a part is about 4/3 of it (encrypted, base64'd): on a slow phone
	// link (1.6 Mbps) the window's two take about a fifth of a second, the
	// most an echo waits behind them.
	snapshotPacedBytes = 16 << 10
	// snapshotWindow is how many parts may be unacknowledged at once: the
	// next is on its way while the viewer acknowledges the one before.
	snapshotWindow = 2
	// snapshotXfersMax bounds the paced snapshots under way at once.
	snapshotXfersMax = 4
)

// snapXfer is a paced snapshot under way to one viewer.
type snapXfer struct {
	viewer, id string
	body       []byte // the gzip stream, shared by the viewers it went to at once; nil while being made
	n          int
	seq        uint64
	sent       int // parts sent
	acked      int // parts the viewer has
}

// snapAcks is what an acknowledgement signals the sender on.
func (a *Agent) snapAcks() <-chan struct{} {
	a.snapMu.Lock()
	defer a.snapMu.Unlock()
	if a.snapWake == nil {
		a.snapWake = make(chan struct{}, 1)
	}
	return a.snapWake
}

// startPacedSnapshot sends each viewer the screen alone, then sets its
// history going in parts (pumpSnapshotParts), and returns the seq both cover.
func (a *Agent) startPacedSnapshot(conn *websocket.Conn, viewers []string) (uint64, error) {
	if a.box == nil || len(viewers) == 0 {
		return 0, nil
	}
	a.screenMu.Lock()
	if a.screen == nil {
		a.screenMu.Unlock()
		return 0, nil
	}
	in := a.joinInputs()
	if a.dropFailedScreenLocked() {
		a.screenMu.Unlock()
		return 0, nil
	}
	latest := a.buf.LatestSeq()
	a.screenMu.Unlock()
	id := newSnapshotID()
	// The history's parts follow only if there is history: else the screen
	// is the whole of it.
	more := len(in.rows) > 0
	screen := []byte(paintSnapshot(in.view, nil, nil, 0, false))
	for _, to := range viewers {
		enc, err := a.sealPart(partHeader{ID: id, To: to, Screen: true, More: more}, screen)
		if err != nil {
			return 0, err
		}
		if err := a.writeMsg(conn, protocol.Message{Type: protocol.TypeSnapshotPart, Seq: latest, Data: enc}); err != nil {
			return 0, err
		}
	}
	if !more {
		return latest, nil
	}
	xs := make([]*snapXfer, 0, len(viewers))
	a.snapMu.Lock()
	for _, to := range viewers {
		x := &snapXfer{viewer: to, id: id, seq: latest}
		xs = append(xs, x)
		a.snapXfers = slices.DeleteFunc(a.snapXfers, func(x *snapXfer) bool { return x.viewer == to })
		a.snapXfers = append(a.snapXfers, x)
	}
	if over := len(a.snapXfers) - snapshotXfersMax; over > 0 {
		a.snapXfers = slices.Delete(a.snapXfers, 0, over)
	}
	a.snapMu.Unlock()
	// The history is made aside — on a long session it takes a while, and the
	// sender goes on with the live output meanwhile — from the same copy as
	// the screen, so both cover latest: what the viewer keeps of the output
	// after it is what follows the history.
	a.goGuarded("paced snapshot", func() {
		body, err := gzipBytes(paintSnapshot(in.view, a.joinHistory(in), nil, a.snapshotBudget(), false))
		a.snapMu.Lock()
		if err != nil {
			a.snapXfers = slices.DeleteFunc(a.snapXfers, func(x *snapXfer) bool { return slices.Contains(xs, x) })
		} else {
			n := (len(body) + snapshotPacedBytes - 1) / snapshotPacedBytes
			for _, x := range xs {
				x.body, x.n = body, n
			}
		}
		wake := a.snapWake
		a.snapMu.Unlock()
		if wake != nil {
			select {
			case wake <- struct{}{}:
			default:
			}
		}
	})
	return latest, nil
}

// pumpSnapshotParts sends, of each paced snapshot under way, the next part
// if its window allows, and says whether any may send another right away.
func (a *Agent) pumpSnapshotParts(conn *websocket.Conn) (bool, error) {
	type due struct {
		x *snapXfer
		i int
	}
	var todo []due
	more := false
	a.snapMu.Lock()
	for _, x := range a.snapXfers {
		if x.body == nil {
			continue // its history still being made
		}
		if x.sent < x.n && x.sent < x.acked+snapshotWindow {
			todo = append(todo, due{x, x.sent})
			x.sent++
		}
		if x.sent < x.n && x.sent < x.acked+snapshotWindow {
			more = true
		}
	}
	// Every part sent: nothing more to do for it, acknowledged or not.
	a.snapXfers = slices.DeleteFunc(a.snapXfers, func(x *snapXfer) bool { return x.body != nil && x.sent >= x.n })
	a.snapMu.Unlock()
	for _, d := range todo {
		x := d.x
		chunk := x.body[d.i*snapshotPacedBytes : min((d.i+1)*snapshotPacedBytes, len(x.body))]
		enc, err := a.sealPart(partHeader{ID: x.id, I: d.i, N: x.n, To: x.viewer, Ack: true}, chunk)
		if err != nil {
			return false, err
		}
		if err := a.writeMsg(conn, protocol.Message{Type: protocol.TypeSnapshotPart, Seq: x.seq, Data: enc}); err != nil {
			return false, err
		}
	}
	return more, nil
}

// ackSnapshot notes that viewer holds the first got parts of snapshot id, and
// wakes the sender to send what that allows. Anything else — a snapshot no
// longer under way, a count it was never sent — changes nothing.
func (a *Agent) ackSnapshot(viewer, id string, got uint64) {
	a.snapMu.Lock()
	woke := false
	for _, x := range a.snapXfers {
		if x.viewer == viewer && x.id == id && got > uint64(x.acked) && got <= uint64(x.sent) {
			x.acked = int(got)
			woke = true
		}
	}
	wake := a.snapWake
	a.snapMu.Unlock()
	if woke && wake != nil {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
}

// dropSnapshotXfers forgets every paced snapshot under way.
func (a *Agent) dropSnapshotXfers() {
	a.snapMu.Lock()
	a.snapXfers = nil
	a.snapMu.Unlock()
}
