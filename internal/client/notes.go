// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

// Window notes — the same annotations the desktop badge shows, mirrored to web
// viewers so a phone sees what the Mac does.
//
// Why the agent holds them at all: the notes are created by `reminal mcp`, which
// an agent's MCP client (Claude Code, Codex, …) spawns in a completely separate
// process tree from the reminal session serving the viewer. `reminal mcp`
// publishes them over the per-agent control socket, every local agent stores
// them, and each pushes to its own viewers.
//
// Scope is the MACHINE, not the session: a note belongs to a window, and a
// window belongs to the machine. Two sessions open on the same Mac both show
// the same window's notes, which is exactly what the badge on screen does.
//
// Lifetime matches the badge too — nothing is persisted. A window that closes
// takes its notes with it.

import (
	"encoding/json"
	"sync"
	"time"

	"reminal/internal/protocol"
)

// windowNote is one annotation. Mirrors the overlay helper's model; `Status` is
// one of attention|working|info|done|handback.
type windowNote struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Title  string `json:"title"`
	Body   string `json:"body,omitempty"`
	Author string `json:"author,omitempty"`
	TS     int64  `json:"ts,omitempty"`
}

// noteStore is the machine's current annotations, keyed by CGWindowID as a
// string (JSON object keys are strings, and the viewer indexes by the same id it
// already uses for window frames).
//
// There is normally MORE THAN ONE publisher: every coding agent with reminal
// registered spawns its own `reminal mcp`, and four of them running for days is
// an ordinary state, not a leak. Each holds its own copy of the notes and
// publishes the complete set, so this store has to be defensive about them:
//
//   - A publisher that never learned about a dismissal would otherwise
//     resurrect the note on its next publish. That is the "I cleared this
//     yesterday and it keeps coming back" bug: whichever publisher happened to
//     read the action queue first consumed it, and the others carried the note
//     forever. `dismissed`/`clearedAt` are tombstones that outlive any single
//     publisher's memory, and replace() filters against them.
//   - Actions are therefore kept as a time-windowed log rather than a
//     drain-once queue, so EVERY publisher gets to see each one.
type noteStore struct {
	mu    sync.RWMutex
	byWin map[string][]windowNote
	// acts is what viewers did recently. Publishers poll it and skip anything
	// at or below the sequence they have already applied.
	acts []timedAct
	seq  uint64
	// dismissed records "window|id" that a viewer cleared, and clearedAt the
	// moment a whole window was cleared. Both suppress matching notes on the
	// way in, no matter which publisher sends them.
	dismissed map[string]int64
	clearedAt map[string]int64
}

// noteAct is one viewer action, collected by the MCP processes.
type noteAct struct {
	Window string `json:"window"`
	ID     string `json:"id"`
	Action string `json:"action"`
	Seq    uint64 `json:"seq"`
}

type timedAct struct {
	noteAct
	at time.Time
}

const (
	// actTTL only has to outlast a publisher's poll interval (2s) by a wide
	// margin; the tombstones are what protect against a publisher that was
	// asleep longer than this.
	actTTL   = 90 * time.Second
	maxActs  = 500
	tombTTL  = 48 * time.Hour
	maxTombs = 1000
)

func newNoteStore() *noteStore {
	return &noteStore{
		byWin:     map[string][]windowNote{},
		dismissed: map[string]int64{},
		clearedAt: map[string]int64{},
	}
}

// suppressedLocked reports whether a note was already cleared by a viewer, so a
// publisher that missed the action can't bring it back.
// A tombstone only suppresses notes that already existed when the viewer
// cleared them. Anything an agent posts AFTERWARDS is new and must still get
// through — `add_note` is an upsert on note_id and stamps TS afresh every time,
// so an agent legitimately re-raising the same id would otherwise be silenced
// forever by a single dismissal. A note carrying no timestamp can't be placed
// either side of the line, so it is left alone rather than silently swallowed.
func (n *noteStore) suppressedLocked(win string, note windowNote) bool {
	if note.TS == 0 {
		return false
	}
	if ts, ok := n.dismissed[win+"|"+note.ID]; ok && note.TS <= ts {
		return true
	}
	if ts, ok := n.clearedAt[win]; ok && note.TS <= ts {
		return true
	}
	return false
}

// pruneLocked bounds both tombstone maps. Notes are ephemeral, so a tombstone
// older than any plausible publisher lifetime is dead weight.
func (n *noteStore) pruneLocked() {
	cutoff := time.Now().Add(-tombTTL).Unix()
	for k, ts := range n.dismissed {
		if ts < cutoff {
			delete(n.dismissed, k)
		}
	}
	for k, ts := range n.clearedAt {
		if ts < cutoff {
			delete(n.clearedAt, k)
		}
	}
	// Hard caps as a backstop: drop the oldest first so a runaway can't grow
	// without bound.
	trim := func(m map[string]int64, max int) {
		for len(m) > max {
			oldestKey, oldest := "", int64(0)
			for k, ts := range m {
				if oldestKey == "" || ts < oldest {
					oldestKey, oldest = k, ts
				}
			}
			delete(m, oldestKey)
		}
	}
	trim(n.dismissed, maxTombs)
	trim(n.clearedAt, maxTombs)

	fresh := n.acts[:0]
	actCutoff := time.Now().Add(-actTTL)
	for _, a := range n.acts {
		if a.at.After(actCutoff) {
			fresh = append(fresh, a)
		}
	}
	n.acts = fresh
	if len(n.acts) > maxActs {
		n.acts = n.acts[len(n.acts)-maxActs:]
	}
}

// replace swaps the whole picture. `reminal mcp` publishes complete state rather
// than deltas: it is small, and a dropped delta would leave a viewer showing a
// note the badge no longer has, with nothing to correct it.
func (n *noteStore) replace(next map[string][]windowNote) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.pruneLocked()
	n.byWin = map[string][]windowNote{}
	for w, list := range next {
		kept := make([]windowNote, 0, len(list))
		for _, note := range list {
			if n.suppressedLocked(w, note) {
				continue // already cleared by a viewer; this publisher missed it
			}
			kept = append(kept, note)
		}
		if len(kept) > 0 {
			n.byWin[w] = kept
		}
	}
}

func (n *noteStore) snapshot() map[string][]windowNote {
	n.mu.RLock()
	defer n.mu.RUnlock()
	out := make(map[string][]windowNote, len(n.byWin))
	for w, list := range n.byWin {
		out[w] = list
	}
	return out
}

func (n *noteStore) empty() bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return len(n.byWin) == 0
}

// notesPayload is the wire shape for TypeWindowNotes.
type notesPayload struct {
	Notes map[string][]windowNote `json:"notes"`
}

// broadcastNotes pushes the current notes to every viewer. Called when
// `reminal mcp` publishes a change, and again when a viewer connects so a tab
// opened after the fact isn't blank until something else moves.
func (a *Agent) broadcastNotes() error {
	if a.notes == nil {
		return nil
	}
	payload, err := json.Marshal(notesPayload{Notes: a.notes.snapshot()})
	if err != nil {
		return err
	}
	enc, err := a.box.Encrypt(payload)
	if err != nil {
		return err
	}
	a.currentConnMu.Lock()
	conn := a.currentConn
	a.currentConnMu.Unlock()
	if conn == nil {
		return nil // not connected; the next viewer connect re-sends
	}
	return a.writeMsg(conn, protocol.Message{Type: protocol.TypeWindowNotes, Data: enc})
}

// setNotesJSON accepts the JSON published over the control socket and pushes the
// result to viewers.
func (a *Agent) setNotesJSON(raw string) error {
	var in notesPayload
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		return err
	}
	if a.notes == nil {
		a.notes = newNoteStore()
	}
	a.notes.replace(in.Notes)
	return a.broadcastNotes()
}

// handleNoteAct applies a viewer's Done / Dismiss / Dismiss-all, mirroring what
// the desktop badge's own buttons do, then re-broadcasts so every viewer agrees.
//
// Handback flips the note to `handback` rather than deleting it: that state is
// the whole point of the loop — it tells the agent the user did their part and
// it is its turn again.
//
// Caveat worth knowing: the badge on screen is driven by `reminal mcp`, a
// different process, so acting here does not yet move the badge. Closing that
// gap needs the daemon to own the store rather than the MCP process.
func (a *Agent) handleNoteAct(msg protocol.Message) {
	if a.notes == nil || len(msg.Data) == 0 {
		return
	}
	raw, err := a.box.Decrypt(msg.Data)
	if err != nil {
		return
	}
	var act struct {
		Window string `json:"window"`
		ID     string `json:"id"`
		Action string `json:"action"`
	}
	if err := json.Unmarshal(raw, &act); err != nil {
		return
	}

	a.notes.mu.Lock()
	list := a.notes.byWin[act.Window]
	switch act.Action {
	case "dismiss_all":
		delete(a.notes.byWin, act.Window)
	case "dismiss":
		kept := list[:0]
		for _, n := range list {
			if n.ID != act.ID {
				kept = append(kept, n)
			}
		}
		if len(kept) == 0 {
			delete(a.notes.byWin, act.Window)
		} else {
			a.notes.byWin[act.Window] = kept
		}
	case "handback":
		for i := range list {
			if list[i].ID == act.ID {
				list[i].Status = "handback"
				list[i].TS = time.Now().Unix()
			}
		}
		a.notes.byWin[act.Window] = list
	}
	// Tombstone it, so a publisher that never sees the action below still can't
	// resurrect the note on its next full-state publish.
	now := time.Now().Unix()
	switch act.Action {
	case "dismiss":
		a.notes.dismissed[act.Window+"|"+act.ID] = now
	case "dismiss_all":
		a.notes.clearedAt[act.Window] = now
	}
	a.notes.seq++
	a.notes.acts = append(a.notes.acts, timedAct{
		noteAct: noteAct{Window: act.Window, ID: act.ID, Action: act.Action, Seq: a.notes.seq},
		at:      time.Now(),
	})
	a.notes.pruneLocked()
	a.notes.mu.Unlock()
	// Tell the owner. The daemon holds the real store and drives the badge, so
	// this is what makes dismissing on a phone clear the dot on screen. The
	// local update above is optimistic: the daemon's next publish confirms it.
	go notesForwardAct(act.Window, act.ID, act.Action)
	_ = a.broadcastNotes()
}

// recentActs returns what viewers have done recently, WITHOUT consuming it.
//
// It used to drain the queue, which quietly broke the moment a second
// `reminal mcp` existed: whichever process polled first took the only copy, and
// every other publisher kept the dismissed note in its map forever. Since each
// publisher sends complete state, one of those stale copies would then put the
// note back — the "I cleared this yesterday and it keeps coming back" report.
//
// Every publisher now sees every action and skips the ones it has already
// applied, using the sequence number. The tombstones in replace() are the
// backstop for a publisher that was asleep for longer than actTTL.
func (a *Agent) recentActs() string {
	if a.notes == nil {
		return "[]"
	}
	a.notes.mu.Lock()
	a.notes.pruneLocked()
	acts := make([]noteAct, 0, len(a.notes.acts))
	for _, t := range a.notes.acts {
		acts = append(acts, t.noteAct)
	}
	a.notes.mu.Unlock()
	if len(acts) == 0 {
		return "[]"
	}
	b, err := json.Marshal(acts)
	if err != nil {
		return "[]"
	}
	return string(b)
}
