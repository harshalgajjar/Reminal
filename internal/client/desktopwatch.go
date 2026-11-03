// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"reminal/internal/protocol"
)

// Watching an agent drive the desktop. An agent that runs agent-desktop inside
// one of its sessions (`agent-desktop session start`) leaves a trace: one JSON
// line per step, written as it happens. This reads those traces and tells the
// viewer, as window_ax {op:"agent"} messages, which window the agent is reading
// and which control it is about to press, so a viewer can open that window and
// mark the control live. It only reads files the agent writes in this user's
// home; it never runs or steers the agent.

// axStateRoot is agent-desktop's state directory, as agent-desktop resolves it.
func axStateRoot() string {
	if v := os.Getenv("AGENT_DESKTOP_HOME"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".agent-desktop")
}

// axTraceEvent is the part of one trace line this reads.
type axTraceEvent struct {
	Event    string `json:"event"`
	Action   string `json:"action"`
	Ref      string `json:"ref"`
	App      string `json:"app"`
	Snapshot string `json:"snapshot_id"`
	Session  string `json:"session_id"`
}

// axTraceTail follows every trace file under a state root, returning only
// lines written since it started.
type axTraceTail struct {
	root    string
	offsets map[string]int64
}

// newAXTraceTail starts at the end of every trace already there: what an agent
// did before the watch began is history, not something to show live. (By
// position, not by time: a file made a moment after the start can carry the
// same coarse mtime.)
func newAXTraceTail(root string) *axTraceTail {
	t := &axTraceTail{root: root, offsets: map[string]int64{}}
	for _, f := range t.files() {
		if st, err := os.Stat(f); err == nil {
			t.offsets[f] = st.Size()
		}
	}
	return t
}

func (t *axTraceTail) files() []string {
	files, _ := filepath.Glob(filepath.Join(t.root, "sessions", "*", "trace", "*.jsonl"))
	return files
}

// poll returns the complete trace lines appended since the last poll.
func (t *axTraceTail) poll() []axTraceEvent {
	var out []axTraceEvent
	for _, f := range t.files() {
		off := t.offsets[f]
		st, err := os.Stat(f)
		if err != nil {
			continue
		}
		if st.Size() <= off {
			continue
		}
		fh, err := os.Open(f)
		if err != nil {
			continue
		}
		_, _ = fh.Seek(off, io.SeekStart)
		buf, _ := io.ReadAll(io.LimitReader(fh, 1<<20))
		_ = fh.Close()
		// Only whole lines; a half-written one is read again next time.
		end := bytes.LastIndexByte(buf, '\n')
		if end < 0 {
			continue
		}
		t.offsets[f] = off + int64(end) + 1
		for _, line := range bytes.Split(buf[:end], []byte{'\n'}) {
			var ev axTraceEvent
			if json.Unmarshal(line, &ev) == nil && ev.Event != "" {
				out = append(out, ev)
			}
		}
	}
	return out
}

// axRefInfo is what a snapshot's refmap says about one ref.
type axRefInfo struct {
	Role     string `json:"role"`
	Name     string `json:"name"`
	PID      int    `json:"pid"`
	App      string `json:"source_app"`
	WindowID string `json:"source_window_id"`
}

// axRefmap reads the refmap of snapshot sid, from the session's store or the
// shared one.
func axRefmap(root, session, sid string) map[string]axRefInfo {
	for _, p := range []string{
		filepath.Join(root, "sessions", session, "snapshots", sid, "refmap.json"),
		filepath.Join(root, "snapshots", sid, "refmap.json"),
	} {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var m struct {
			Inner map[string]axRefInfo `json:"inner"`
		}
		if json.Unmarshal(raw, &m) == nil {
			return m.Inner
		}
	}
	return nil
}

// axLookupRef resolves "@<snapshot>:<element>" to its refmap entry.
func axLookupRef(root, session, ref string) (axRefInfo, bool) {
	sid, el, ok := strings.Cut(strings.TrimPrefix(ref, "@"), ":")
	if !ok {
		return axRefInfo{}, false
	}
	info, ok := axRefmap(root, session, sid)["@"+el]
	return info, ok
}

// axServe answers a window_ax request where this OS serves it: the daemon on
// macOS (for its Accessibility grant), this agent's own backend elsewhere.
func (a *Agent) axServe(req axReq) axReply {
	if runtime.GOOS == "darwin" {
		return mirrorForwardAX(req)
	}
	b := a.windows()
	if reason := b.unsupported(); reason != "" {
		return axReply{ID: req.ID, Op: req.Op, Seq: req.Seq, Error: reason}
	}
	return desktopAX.serve(b, req)
}

// watchAgentDesktop follows agent-desktop's traces for as long as this agent
// lives, once a viewer has asked for windows and agent-desktop is here.
func (a *Agent) watchAgentDesktop() {
	if !axAvailable() {
		return
	}
	a.axWatch.Do(func() {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					recoverLog("watchAgentDesktop", r)
				}
			}()
			tail := newAXTraceTail(axStateRoot())
			lastLook, lastLookAt := "", time.Time{}
			for {
				time.Sleep(250 * time.Millisecond)
				for _, ev := range tail.poll() {
					switch ev.Event {
					case "snapshot.saved":
						// The agent is reading a window: say which, at most once
						// every few seconds per window.
						for _, info := range axRefmap(tail.root, ev.Session, ev.Snapshot) {
							id := a.axWindowID(info)
							if id != "" && (id != lastLook || time.Since(lastLookAt) > 5*time.Second) {
								lastLook, lastLookAt = id, time.Now()
								a.sendWindowMsg(a.liveConn(), protocol.TypeWindowAX, axReply{ID: id, Op: "agent", OK: true, Did: "look"})
							}
							break
						}
					case "action.dispatch.start":
						info, ok := axLookupRef(tail.root, ev.Session, ev.Ref)
						if !ok {
							continue
						}
						id := a.axWindowID(info)
						if id == "" {
							continue
						}
						rep := a.axServe(axReq{ID: id, Op: "find", Role: info.Role, Name: info.Name})
						rep.Op, rep.Did, rep.OK = "agent", ev.Action, true
						a.sendWindowMsg(a.liveConn(), protocol.TypeWindowAX, rep)
					}
				}
			}
		}()
	})
}

// axWindowID maps agent-desktop's window to a reminal window id. On macOS both
// are the CGWindowNumber; elsewhere the owning process (or app) picks it.
func (a *Agent) axWindowID(info axRefInfo) string {
	if runtime.GOOS == "darwin" {
		if n, ok := strings.CutPrefix(info.WindowID, "w-"); ok {
			if _, err := strconv.Atoi(n); err == nil {
				return n
			}
		}
		return ""
	}
	wins, err := a.windows().list()
	if err != nil {
		return ""
	}
	for _, w := range wins {
		if (info.PID != 0 && w.PID == info.PID) || (info.App != "" && w.App == info.App) {
			return w.ID
		}
	}
	return ""
}
