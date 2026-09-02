// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

// `reminal mcp` — an MCP (Model Context Protocol) stdio server exposing the
// window-comment overlay as agent tools, so any MCP-capable agent can leave a
// note ON the window the note is about and learn when the user hands it back.
//
// Why it lives in the reminal binary rather than as a script: an MCP server is
// only useful if it is trivially registerable, and "point your agent at this
// python file in a repo" is not that. `reminal integrate` registers
// `<reminal> mcp`, which means one binary, no interpreter, and a path that
// survives upgrades.
//
// Transport is MCP stdio: newline-delimited JSON-RPC 2.0. stdout therefore
// carries protocol traffic ONLY — anything else corrupts the stream, so all
// diagnostics go to stderr.
//
// This process holds NO state of its own when the daemon is reachable, which is
// the normal case (install.sh starts the daemon regardless of ownership). A
// machine runs one `reminal mcp` per registered coding agent — four of them is
// ordinary — and when each kept its own copy of the notes they overwrote one
// another: a note dismissed on a phone came back from whichever publisher had
// not heard, and one publisher's update erased another's windows. The daemon is
// the machine's singleton, so it owns the store and the badge and every server
// here is a thin client. The in-process path is kept only as a fallback for a
// machine with no daemon running.
//
// Notes are still ephemeral either way: they live exactly as long as their
// window, so there is nothing to persist and the CGWindowID is a fine key.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/reminal/reminal/internal/client"
)

const mcpProtocolFallback = "2024-11-05"

// maxOverlayWindows bounds how many windows can carry badges at once — each is a
// helper process. maxReplyEvents bounds the reply backlog for an agent that
// never calls read_replies.
const (
	maxOverlayWindows = 12
	maxReplyEvents    = 500
)

// mcpInstructions is handed to the client on initialize and is what the model
// actually reads. It matters as much as the schemas: the difference between a
// badge used well and a badge that becomes noise is almost entirely here.
const mcpInstructions = `reminal is two things for an agent: notes on a window, and a view of every reminal this device owns.

Sessions — every machine this device owns (this box and any you have owner-connected to):
  1. list_sessions to see machines and their live terminals: id, name, path (cwd), title, viewers, idle time.
  2. search_sessions with a regex to find which session mentioned something. It matches name/path/title/id on every machine, and live terminal scrollback on this machine (and on remotes that have been upgraded).
  3. read_transcript to pull one session's current scrollback as plain text (ANSI stripped; long buffers return the newest tail).
  4. send_keys to type into a session. Owned sessions need only the id; any other reminal needs session id + PIN (or a join URL). Set enter=true to press Return after the text.
If the user just arrived from another reminal, list or search, then read that transcript before asking them to recap. To run a command in a reminal, send_keys then read_transcript.

Notes — a small floating badge ON a window, not text buried in a terminal they may not be looking at. Use when what you want to say is ABOUT a particular window. Do not use notes for ordinary conversation.

Workflow:
  1. list_windows to find the window your note belongs to.
  2. add_note with a status:
       attention — you are BLOCKED and need the user to act. Red, and the only status that pulses. Use sparingly; this interrupts.
       working   — you are mid-task on this. Ambient progress, no action wanted.
       info      — worth seeing, no action needed.
       done      — finished, nothing owed.
  3. If you posted 'attention', call read_replies later. A 'handback' reply means the user pressed Done and it is your turn again — pick the work back up.

Notes are ephemeral: they live only as long as the window, and you will see a 'closed' reply when it goes away. Keep titles to a few words — the badge is a glance surface, and the body carries detail. Only three notes are visible before the list scrolls, so remove notes you no longer need instead of letting them pile up.`

// ---------------------------------------------------------------- overlay children

// overlayHelper is the single reminal-overlay process serving every badged
// window. It used to be one process per window at ~26MB each; multiplexing also
// collapses N identical window-list enumerations per tick down to one.
type overlayHelper struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
}

type mcpServer struct {
	mu       sync.Mutex
	seq      uint64 // disambiguates auto-generated note ids
	helper   *overlayHelper
	attached map[uint32]bool  // windows currently carrying a badge
	events   []map[string]any // replies from the user, oldest first
	pollOnce sync.Once
	// lastActSeq is the highest viewer-action sequence this process has applied.
	lastActSeq uint64
	// daemonOwned is set when the daemon's notes service answered at startup.
	// Then this process keeps NO state: it forwards every tool call and the
	// daemon owns the store and the badge. The in-process path below is only a
	// fallback for a machine with no daemon running.
	daemonOwned bool
	// lastReplySeq is our cursor into the daemon's badge-event log.
	lastReplySeq uint64
	// notes mirrors what each window is showing. Duplicated from the helper
	// because web viewers need the same list and they reach it through the
	// reminal agent, which is a different process tree entirely.
	notes map[uint32][]mcpNote
}

func newMCPServer() *mcpServer {
	return &mcpServer{attached: map[uint32]bool{}, notes: map[uint32][]mcpNote{}}
}

// mcpNote mirrors one badge entry. Kept here as well as in the helper because
// web viewers need the same list, and they reach it through the reminal agent,
// not through this process.
type mcpNote struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Title  string `json:"title"`
	Body   string `json:"body,omitempty"`
	Author string `json:"author,omitempty"`
	TS     int64  `json:"ts,omitempty"`
}

// publishNotes hands the whole current picture to every reminal agent running
// as this user, which mirrors it to their viewers.
//
// Broadcast rather than addressed: notes are machine-scoped (a note belongs to a
// window, and the window belongs to the machine), and this process has no idea
// which session — if any — the user happens to be watching. Sessions come and go
// while an agent works, so publishing to all of them is what makes a note show
// up on the phone regardless of which tab is open.
//
// Best-effort throughout: a dead socket from an exited session must never fail
// an MCP tool call. The badge on screen is the source of truth.
func (s *mcpServer) publishNotes() {
	s.mu.Lock()
	payload := map[string]any{"notes": map[string][]mcpNote{}}
	m := payload["notes"].(map[string][]mcpNote)
	for w, list := range s.notes {
		if len(list) > 0 {
			m[strconv.FormatUint(uint64(w), 10)] = list
		}
	}
	s.mu.Unlock()

	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	socks, _ := filepath.Glob(filepath.Join(home, ".reminal", "agent-*.sock"))
	for _, sock := range socks {
		conn, err := net.DialTimeout("unix", sock, 300*time.Millisecond)
		if err != nil {
			continue // session exited; its socket file lingers briefly
		}
		_ = conn.SetDeadline(time.Now().Add(time.Second))
		_, _ = fmt.Fprintf(conn, "notes %s\n", body)
		_ = conn.Close()
	}
}

// drainViewerActs collects what viewers did — dismiss / handback — from every
// local agent and applies it to our mirror.
//
// The viewer talks to an agent, not to this process, so without collecting them
// a dismissal made on the web is undone the moment we publish again. Polled
// rather than pushed because nothing can call into an MCP server: it is a stdio
// child of somebody else's client, with no address of its own.
func (s *mcpServer) drainViewerActs() {
	if s.daemonOwned {
		return // the daemon owns the store and applies viewer actions itself
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	socks, _ := filepath.Glob(filepath.Join(home, ".reminal", "agent-*.sock"))
	var acts []struct {
		Window string `json:"window"`
		ID     string `json:"id"`
		Action string `json:"action"`
		Seq    uint64 `json:"seq"`
	}
	for _, sock := range socks {
		conn, err := net.DialTimeout("unix", sock, 300*time.Millisecond)
		if err != nil {
			continue
		}
		_ = conn.SetDeadline(time.Now().Add(time.Second))
		_, _ = fmt.Fprintln(conn, "note-acts")
		line, err := bufio.NewReader(conn).ReadString('\n')
		_ = conn.Close()
		if err != nil {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "ok"))
		if payload == "" || payload == "[]" {
			continue
		}
		var batch []struct {
			Window string `json:"window"`
			ID     string `json:"id"`
			Action string `json:"action"`
			Seq    uint64 `json:"seq"`
		}
		if json.Unmarshal([]byte(payload), &batch) == nil {
			acts = append(acts, batch...)
		}
	}
	if len(acts) == 0 {
		return
	}

	// The agent serves a time-windowed log, not a drain-once queue, so every
	// publisher sees every action — including the ones this process already
	// applied. Skip those, or a single dismissal would re-fire on each 2s poll
	// for as long as it stays in the window.
	fresh, highest := acts[:0], s.lastActSeq
	for _, a := range acts {
		if a.Seq > s.lastActSeq {
			fresh = append(fresh, a)
			if a.Seq > highest {
				highest = a.Seq
			}
		}
	}
	acts, s.lastActSeq = fresh, highest
	if len(acts) == 0 {
		return
	}

	changed := false
	s.mu.Lock()
	for _, a := range acts {
		w64, err := strconv.ParseUint(a.Window, 10, 32)
		if err != nil {
			continue
		}
		win := uint32(w64)
		switch a.Action {
		case "dismiss_all":
			if _, ok := s.notes[win]; ok {
				delete(s.notes, win)
				changed = true
			}
		case "dismiss":
			if list, ok := s.notes[win]; ok {
				kept := list[:0]
				for _, n := range list {
					if n.ID != a.ID {
						kept = append(kept, n)
					}
				}
				if len(kept) == 0 {
					delete(s.notes, win)
				} else {
					s.notes[win] = kept
				}
				changed = true
			}
		case "handback":
			for i := range s.notes[win] {
				if s.notes[win][i].ID == a.ID && s.notes[win][i].Status != "handback" {
					s.notes[win][i].Status = "handback"
					changed = true
				}
			}
		}
	}
	s.mu.Unlock()

	// Mirror it onto the badge too, so dismissing on the phone clears the dot
	// on screen — the direction that never worked before.
	for _, a := range acts {
		if w64, err := strconv.ParseUint(a.Window, 10, 32); err == nil {
			switch a.Action {
			case "dismiss":
				_ = s.send(uint32(w64), map[string]any{"cmd": "remove", "id": a.ID})
			case "dismiss_all":
				_ = s.send(uint32(w64), map[string]any{"cmd": "clear"})
			}
		}
	}
	if changed {
		s.publishNotes()
	}
}

// overlayHelperPath mirrors how the agent finds reminal-capture: explicit
// override, then alongside this binary (the release layout), then PATH.
func overlayHelperPath() (string, error) {
	if p := os.Getenv("REMINAL_OVERLAY_BIN"); p != "" {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	if exe, err := os.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(exe), "reminal-overlay")
		if st, err := os.Stat(cand); err == nil && !st.IsDir() {
			return cand, nil
		}
	}
	if p, err := exec.LookPath("reminal-overlay"); err == nil {
		return p, nil
	}
	return "", errors.New("reminal-overlay helper not found next to reminal or on PATH")
}

func (s *mcpServer) ensureHelper() (*overlayHelper, error) {
	s.mu.Lock()
	if s.helper != nil && s.helper.cmd.ProcessState == nil {
		h := s.helper
		s.mu.Unlock()
		return h, nil
	}
	s.mu.Unlock()

	bin, err := overlayHelperPath()
	if err != nil {
		return nil, err
	}
	// No argv: the helper's default mode is a stdin-driven multiplexer. It shows
	// nothing until a window is attached and a note arrives.
	cmd := exec.Command(bin)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	h := &overlayHelper{cmd: cmd, stdin: stdin}
	s.mu.Lock()
	s.helper = h
	s.attached = map[uint32]bool{} // a fresh helper carries no badges
	s.mu.Unlock()
	go s.readReplies(stdout)
	s.pollOnce.Do(func() {
		go func() {
			for range time.Tick(2 * time.Second) {
				s.drainViewerActs()
			}
		}()
	})
	return h, nil
}

// readReplies collects what the user did — handback / dismiss / closed — from
// the one helper, for every window it serves.
func (s *mcpServer) readReplies(r io.ReadCloser) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		var ev map[string]any
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			continue
		}
		ev["received_at"] = time.Now().Unix()
		s.mu.Lock()
		s.events = append(s.events, ev)
		if len(s.events) > maxReplyEvents {
			s.events = s.events[len(s.events)-maxReplyEvents:]
		}
		// Apply what the user did to our mirror, not just to the badge. Without
		// this the mirror still holds a note they dismissed on screen, and the
		// next publish re-asserts it — the note reappears in the viewer by
		// itself and cannot be got rid of from either side.
		changed := false
		w, hasWin := ev["window"].(float64)
		id, _ := ev["id"].(string)
		if hasWin {
			win := uint32(w)
			switch ev["event"] {
			case "closed":
				delete(s.attached, win)
				delete(s.notes, win)
				changed = true
			case "dismiss", "evicted":
				if list, ok := s.notes[win]; ok {
					kept := list[:0]
					for _, n := range list {
						if n.ID != id {
							kept = append(kept, n)
						}
					}
					if len(kept) == 0 {
						delete(s.notes, win)
					} else {
						s.notes[win] = kept
					}
					changed = true
				}
			case "handback":
				for i := range s.notes[win] {
					if s.notes[win][i].ID == id {
						s.notes[win][i].Status = "handback"
						s.notes[win][i].TS = time.Now().Unix()
						changed = true
					}
				}
			}
		}
		s.mu.Unlock()
		if changed {
			s.publishNotes()
		}
	}
	s.mu.Lock()
	s.helper = nil
	s.attached = map[uint32]bool{}
	s.mu.Unlock()
}

// send delivers one command for one window, attaching the badge first if needed.
func (s *mcpServer) send(window uint32, payload map[string]any) error {
	h, err := s.ensureHelper()
	if err != nil {
		return err
	}
	s.mu.Lock()
	needAttach := !s.attached[window]
	if needAttach && len(s.attached) >= maxOverlayWindows {
		s.mu.Unlock()
		return fmt.Errorf("already showing notes on %d windows (the limit); "+
			"clear one with clear_notes before annotating another", maxOverlayWindows)
	}
	if needAttach {
		s.attached[window] = true
	}
	s.mu.Unlock()

	write := func(m map[string]any) error {
		b, _ := json.Marshal(m)
		if _, err := h.stdin.Write(append(b, '\n')); err != nil {
			s.mu.Lock()
			s.helper = nil
			s.attached = map[uint32]bool{}
			s.mu.Unlock()
			return errors.New("the overlay helper went away")
		}
		return nil
	}
	if needAttach {
		if err := write(map[string]any{
			"cmd": "attach", "window": window, "corner": "tr", "placement": "float",
		}); err != nil {
			return err
		}
	}
	payload["window"] = window
	return write(payload)
}

// detach drops one window's badge and frees its slot, leaving the rest running.
func (s *mcpServer) detach(window uint32) {
	s.mu.Lock()
	delete(s.attached, window)
	s.mu.Unlock()
}

// shutdown stops every badge. MCP clients restart their servers freely, and an
// orphaned overlay would leave stale badges stuck on windows with nothing able
// to clear them.
func (s *mcpServer) shutdown() {
	s.mu.Lock()
	h := s.helper
	s.helper = nil
	s.attached = map[uint32]bool{}
	s.mu.Unlock()
	s.mu.Lock()
	s.notes = map[uint32][]mcpNote{}
	s.mu.Unlock()
	s.publishNotes()
	if h == nil {
		return
	}
	_, _ = h.stdin.Write([]byte("{\"cmd\":\"quit\"}\n"))
	done := make(chan struct{})
	go func() { _ = h.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		if h.cmd.Process != nil {
			_ = h.cmd.Process.Kill()
		}
	}
}

// ---------------------------------------------------------------- tools

func mcpToolList() []map[string]any {
	obj := func(props map[string]any, required ...string) map[string]any {
		m := map[string]any{"type": "object", "properties": props}
		if len(required) > 0 {
			m["required"] = required
		}
		return m
	}
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	return []map[string]any{
		{
			"name": "list_sessions",
			"description": "List every reminal this device owns: this machine and any enrolled box, " +
				"each with live sessions (id, name, path/cwd, title, viewers, idle). " +
				"Does not include PINs. Call this to find which session to search or talk about.",
			"inputSchema": obj(map[string]any{}),
		},
		{
			"name": "search_sessions",
			"description": "Regex-search reminal sessions this device owns. Matches name, path, title, " +
				"and id on every machine; also searches live terminal scrollback on this machine " +
				"(and on remotes whose reminal is new enough). Returns snippets, not a full dump.",
			"inputSchema": obj(map[string]any{
				"pattern": str("Go/RE2 regular expression. Use (?i) for case-insensitive."),
			}, "pattern"),
		},
		{
			"name": "read_transcript",
			"description": "Read one reminal session's live terminal as plain text (ANSI stripped). " +
				"Use list_sessions to get the id. Long history is truncated to the newest tail. " +
				"Does not attach a viewer and does not include the PIN.",
			"inputSchema": obj(map[string]any{
				"session": str("Session id from list_sessions (or a unique session name)."),
				"machine": str("Optional machine name or id when the session id exists on more than one box."),
			}, "session"),
		},
		{
			"name": "send_keys",
			"description": "Type keystrokes into a reminal session's live terminal (the PTY). " +
				"Owned sessions: pass session id from list_sessions. Any other reminal: pass session + pin " +
				"(or a join URL). Newlines become Enter. Set enter=true to press Return. " +
				"Does not return command output — follow with read_transcript if you own the session.",
			"inputSchema": obj(map[string]any{
				"session": str("Session id, or a join URL like https://live.reminal.app/?s=ID#p=PIN."),
				"keys":    str("Characters to type. Use \\n for Enter. Ctrl-C is the U+0003 character."),
				"pin":     str("Session PIN. Required unless this device owns the machine (list_sessions) or the URL already has #p=."),
				"enter": map[string]any{
					"type":        "boolean",
					"default":     false,
					"description": "If true, press Return after keys (run a line).",
				},
				"machine": str("Optional machine name or id when the session id exists on more than one owned box."),
			}, "session", "keys"),
		},
		{
			"name": "list_windows",
			"description": "List the windows open on the user's screen with the window_id the other tools need. " +
				"Call this first — window ids are ephemeral and change when an app restarts.",
			"inputSchema": obj(map[string]any{}),
		},
		{
			"name": "add_note",
			"description": "Attach a note to a window, shown as a small floating badge on that window. " +
				"Reusing an existing note_id updates that note in place — use that to move a note from " +
				"'working' to 'done' rather than adding a second one.",
			"inputSchema": obj(map[string]any{
				"window_id": map[string]any{"type": "integer", "description": "From list_windows."},
				"title":     str("Short headline, a few words. Shown in bold."),
				"body":      str("Optional detail, one or two sentences."),
				"status": map[string]any{
					"type": "string", "enum": []string{"attention", "working", "info", "done"},
					"default": "info",
					"description": "attention = you are blocked and need the user (red, pulses, interrupts); " +
						"working = in progress; info = FYI; done = finished.",
				},
				"note_id": str("Stable id for this note; pass it again to update it in place."),
				"author":  str("Who is speaking, e.g. your agent name."),
			}, "window_id", "title"),
		},
		{
			"name":        "remove_note",
			"description": "Remove one note from a window's list.",
			"inputSchema": obj(map[string]any{
				"window_id": map[string]any{"type": "integer"},
				"note_id":   str("Id given to add_note."),
			}, "window_id", "note_id"),
		},
		{
			"name":        "clear_notes",
			"description": "Remove every note from a window and take the badge off it.",
			"inputSchema": obj(map[string]any{
				"window_id": map[string]any{"type": "integer"},
			}, "window_id"),
		},
		{
			"name": "read_replies",
			"description": "Read what the user did with your notes since the last call. 'handback' means they " +
				"pressed Done and it is your turn again; 'dismiss' means they cleared it; 'closed' means the " +
				"window went away and its list is gone. Replies are returned once, then forgotten.",
			"inputSchema": obj(map[string]any{
				"window_id": map[string]any{"type": "integer", "description": "Optional: only this window's replies."},
			}),
		},
	}
}

func argInt(args map[string]any, key string) (uint32, error) {
	switch v := args[key].(type) {
	case float64:
		return uint32(v), nil
	case string:
		n, err := strconv.ParseUint(v, 10, 32)
		return uint32(n), err
	}
	return 0, fmt.Errorf("%s is required", key)
}

func argStr(args map[string]any, key, def string) string {
	if s, ok := args[key].(string); ok && s != "" {
		return s
	}
	return def
}

func argBool(args map[string]any, key string, def bool) bool {
	switch v := args[key].(type) {
	case bool:
		return v
	case string:
		return v == "true" || v == "1" || strings.EqualFold(v, "yes")
	}
	return def
}

func (s *mcpServer) callTool(name string, args map[string]any) (string, error) {
	switch name {
	case "list_sessions":
		return mcpListSessions()
	case "search_sessions":
		return mcpSearchSessions(argStr(args, "pattern", argStr(args, "regex", "")))
	case "read_transcript":
		return mcpReadTranscript(argStr(args, "session", argStr(args, "id", "")), argStr(args, "machine", ""))
	case "send_keys":
		return mcpSendKeys(
			argStr(args, "session", argStr(args, "id", "")),
			argStr(args, "machine", ""),
			argStr(args, "keys", argStr(args, "text", "")),
			argStr(args, "pin", argStr(args, "PIN", "")),
			argBool(args, "enter", false) || argBool(args, "newline", false),
		)
	}
	if runtime.GOOS != "darwin" && name != "read_replies" {
		return "", errors.New("window notes are macOS-only for now")
	}
	switch name {
	case "list_windows":
		bin, err := overlayHelperPath()
		if err != nil {
			return "", err
		}
		out, err := exec.Command(bin, "windows").Output()
		if err != nil {
			return "", fmt.Errorf("listing windows: %w", err)
		}
		if len(strings.TrimSpace(string(out))) == 0 {
			return "No windows found.", nil
		}
		return "window_id\tapp\ttitle\tbounds\n" + strings.TrimRight(string(out), "\n"), nil

	case "add_note":
		wid, err := argInt(args, "window_id")
		if err != nil {
			return "", err
		}
		title := argStr(args, "title", "")
		if title == "" {
			return "", errors.New("title is required")
		}
		// A millisecond stamp alone collides: two add_note calls in the same
		// millisecond produced the same id, and on one window the second would
		// silently overwrite the first instead of adding a note.
		s.mu.Lock()
		s.seq++
		seq := s.seq
		s.mu.Unlock()
		id := argStr(args, "note_id", fmt.Sprintf("n%d-%d", time.Now().UnixMilli(), seq))
		if s.daemonOwned {
			warn, err := client.NotesAdd(wid, client.NoteInput{
				ID: id, Status: argStr(args, "status", "info"), Title: title,
				Body: argStr(args, "body", ""), Author: argStr(args, "author", "agent"),
				TS: time.Now().Unix(),
			})
			if err != nil {
				return "", err
			}
			msg := fmt.Sprintf("Note %s posted to window %d.", id, wid)
			if warn != "" {
				msg += " (" + warn + ")"
			}
			return msg, nil
		}
		err = s.send(wid, map[string]any{
			"cmd": "upsert", "id": id,
			"status": argStr(args, "status", "info"),
			"title":  title, "body": argStr(args, "body", ""),
			"author": argStr(args, "author", "agent"),
		})
		if err != nil {
			return "", err
		}
		s.mu.Lock()
		list := s.notes[wid]
		updated := false
		for i := range list {
			if list[i].ID == id {
				list[i] = mcpNote{ID: id, Status: argStr(args, "status", "info"), Title: title,
					Body: argStr(args, "body", ""), Author: argStr(args, "author", "agent"),
					TS: time.Now().Unix()}
				updated = true
				break
			}
		}
		if !updated {
			list = append(list, mcpNote{ID: id, Status: argStr(args, "status", "info"), Title: title,
				Body: argStr(args, "body", ""), Author: argStr(args, "author", "agent"),
				TS: time.Now().Unix()})
		}
		s.notes[wid] = list
		s.mu.Unlock()
		s.publishNotes()
		return fmt.Sprintf("Note %s posted to window %d.", id, wid), nil

	case "remove_note":
		wid, err := argInt(args, "window_id")
		if err != nil {
			return "", err
		}
		id := argStr(args, "note_id", "")
		if id == "" {
			return "", errors.New("note_id is required")
		}
		if s.daemonOwned {
			if err := client.NotesRemove(wid, id); err != nil {
				return "", err
			}
			return "Removed " + id + ".", nil
		}
		if err := s.send(wid, map[string]any{"cmd": "remove", "id": id}); err != nil {
			return "", err
		}
		s.mu.Lock()
		kept := s.notes[wid][:0]
		for _, n := range s.notes[wid] {
			if n.ID != id {
				kept = append(kept, n)
			}
		}
		s.notes[wid] = kept
		s.mu.Unlock()
		s.publishNotes()
		return "Removed " + id + ".", nil

	case "clear_notes":
		wid, err := argInt(args, "window_id")
		if err != nil {
			return "", err
		}
		if s.daemonOwned {
			if err := client.NotesClear(wid); err != nil {
				return "", err
			}
			return "Cleared.", nil
		}
		if err := s.send(wid, map[string]any{"cmd": "clear"}); err != nil {
			return "", err
		}
		// Also drop the panel, so the window-count limit reflects reality.
		_ = s.send(wid, map[string]any{"cmd": "detach"})
		s.detach(wid)
		s.mu.Lock()
		delete(s.notes, wid)
		s.mu.Unlock()
		s.publishNotes()
		return "Cleared.", nil

	case "read_replies":
		want, hasWant := args["window_id"].(float64)
		if s.daemonOwned {
			// Pull anything new from the daemon's log into the local buffer, then
			// fall through to the same filter/drain below. The cursor is ours
			// alone, so another MCP client reading its replies cannot blind us.
			evs, seq, err := client.NotesReplies(s.lastReplySeq)
			if err == nil {
				s.mu.Lock()
				s.events = append(s.events, evs...)
				if len(s.events) > maxReplyEvents {
					s.events = s.events[len(s.events)-maxReplyEvents:]
				}
				s.lastReplySeq = seq
				s.mu.Unlock()
			}
		}
		s.mu.Lock()
		var out, keep []map[string]any
		for _, ev := range s.events {
			w, _ := ev["window"].(float64)
			if !hasWant || w == want {
				out = append(out, ev)
			} else {
				keep = append(keep, ev)
			}
		}
		s.events = keep
		s.mu.Unlock()
		if len(out) == 0 {
			return "No replies.", nil
		}
		var b strings.Builder
		for _, ev := range out {
			line, _ := json.Marshal(ev)
			b.Write(line)
			b.WriteByte('\n')
		}
		return strings.TrimRight(b.String(), "\n"), nil
	}
	return "", fmt.Errorf("unknown tool: %s", name)
}

// ---------------------------------------------------------------- JSON-RPC

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

func runMCP(_ []string) error {
	srv := newMCPServer()
	// Ask once, at startup, who owns the notes. Reachable daemon (the normal
	// case) => this process is stateless and every tool call is forwarded.
	srv.daemonOwned = client.NotesDaemonReachable()
	defer srv.shutdown()

	out := json.NewEncoder(os.Stdout)
	reply := func(id json.RawMessage, result any) {
		_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	}

	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var msg rpcMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		switch msg.Method {
		case "initialize":
			var p struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			// Named `proto`, not `version`: the latter shadows reminal's own
			// build version and silently reports the protocol string as the
			// server version.
			proto := p.ProtocolVersion
			if proto == "" {
				proto = mcpProtocolFallback
			}
			reply(msg.ID, map[string]any{
				"protocolVersion": proto,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "reminal", "version": version},
				"instructions":    mcpInstructions,
			})
		case "ping":
			reply(msg.ID, map[string]any{})
		case "tools/list":
			reply(msg.ID, map[string]any{"tools": mcpToolList()})
		case "tools/call":
			var p struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			text, err := srv.callTool(p.Name, p.Arguments)
			if err != nil {
				// Tool failures are results, not protocol errors — the model
				// should see them and be able to correct course.
				reply(msg.ID, map[string]any{
					"content": []any{map[string]any{"type": "text", "text": "Error: " + err.Error()}},
					"isError": true,
				})
				continue
			}
			reply(msg.ID, map[string]any{
				"content": []any{map[string]any{"type": "text", "text": text}},
			})
		default:
			if len(msg.ID) == 0 {
				continue // a notification; nothing to answer
			}
			_ = out.Encode(map[string]any{
				"jsonrpc": "2.0", "id": msg.ID,
				"error": map[string]any{"code": -32601, "message": "method not found: " + msg.Method},
			})
		}
	}
	return nil
}
