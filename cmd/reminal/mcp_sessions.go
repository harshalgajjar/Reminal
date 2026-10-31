// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"reminal/internal/client"
	"reminal/internal/protocol"
	"reminal/internal/session"
)

type mcpSessionRow struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	Path     string `json:"path,omitempty"`
	Title    string `json:"title,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Port     int    `json:"port,omitempty"`
	Headless bool   `json:"headless,omitempty"`
	Viewers  int    `json:"viewers,omitempty"`
	IdleSecs int64  `json:"idle_secs,omitempty"`
	// Attn is what the session's agent is doing, as the session detects it:
	// "working", "input" (needs a person), "done", "logged-out" (needs a
	// person to log it in) or "limited" (held back by its service's usage or
	// rate limit). Empty for a plain shell.
	Attn string `json:"attn,omitempty"`
	// ResetsAt is, while Attn is "limited", when the limit resets (RFC3339),
	// as the agent's own notice said; omitted when it did not say.
	ResetsAt string `json:"resets_at,omitempty"`
	// Shells is how many shells the session's harness still has running — a
	// finished turn that left a build going reads "done" and is not finished.
	Shells  int  `json:"shells,omitempty"`
	Current bool `json:"current,omitempty"`
	// Asleep: the session sleeps (its processes ended, its record kept).
	// read_transcript and search_sessions answer from what it saved;
	// send_keys wakes it first. SleptAt is when (RFC3339); Dropped, what the
	// person let go of by sleeping it anyway.
	Asleep  bool     `json:"asleep,omitempty"`
	SleptAt string   `json:"slept_at,omitempty"`
	Dropped []string `json:"dropped,omitempty"`
}

type mcpMachineRow struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Hostname string `json:"hostname,omitempty"`
	Local    bool   `json:"local,omitempty"`
	Online   bool   `json:"online"`
	Error    string `json:"error,omitempty"`
	// Battery is omitted entirely for machines that have no battery, so an
	// agent can read its presence as "this is a laptop" and its absence as
	// "this is a desktop or a server" without a second field to consult.
	Battery  *mcpBattery     `json:"battery,omitempty"`
	Sessions []mcpSessionRow `json:"sessions"`
}

// mcpBattery is a machine's power state as an agent sees it. Fields are named
// for what they mean rather than what a UI would draw, and the estimate says
// what it counts down TO — an agent deciding where to put a two-hour job needs
// "43 minutes until dead" and "43 minutes until full" to be unmistakable.
type mcpBattery struct {
	Percent int `json:"percent"`
	// State is "charging", "discharging" or "charged".
	State string `json:"state,omitempty"`
	// MinutesToEmpty is set only while discharging; MinutesToFull only while
	// charging. Both omitted when the OS had no estimate (common for a minute
	// or two after a power change, and always on Windows while charging).
	MinutesToEmpty int `json:"minutes_to_empty,omitempty"`
	MinutesToFull  int `json:"minutes_to_full,omitempty"`
	// AsOf is when this reading was taken, RFC3339. Stale marks a reading kept
	// from the last time the machine answered rather than read just now — an
	// offline laptop still reports its last known charge, and an agent must be
	// able to tell that apart from a live one.
	AsOf  string `json:"as_of,omitempty"`
	Stale bool   `json:"stale,omitempty"`
}

// mcpBatteryOf projects a snapshot for the wire, or nil when the machine has
// no battery to report.
func mcpBatteryOf(b *client.BatterySnapshot) *mcpBattery {
	if b == nil {
		return nil
	}
	out := &mcpBattery{Percent: b.Pct, State: b.State, Stale: b.Stale}
	if !b.At.IsZero() {
		out.AsOf = b.At.Format(time.RFC3339)
	}
	switch b.State {
	case "charging":
		out.MinutesToFull = b.Mins
	case "charged":
		// "0:00 remaining" on a full battery is not a countdown to anything.
	default:
		out.MinutesToEmpty = b.Mins
	}
	return out
}

type mcpSearchHit struct {
	Machine  string   `json:"machine"`
	Session  string   `json:"session"`
	Name     string   `json:"name,omitempty"`
	Path     string   `json:"path,omitempty"`
	Title    string   `json:"title,omitempty"`
	Where    []string `json:"where"`
	Snippets []string `json:"snippets,omitempty"`
}

func mcpListSessions() (string, error) {
	fleet, err := client.CollectFleet("")
	if err != nil {
		return "", err
	}
	current := strings.ToUpper(strings.TrimSpace(os.Getenv("REMINAL_SESSION")))
	if current == "" {
		// A harness that scrubbed the environment of its MCP server: the
		// process tree still says which session this is (session.Enclosing).
		current = strings.ToUpper(session.Enclosing())
	}
	rows := make([]mcpMachineRow, 0, len(fleet))
	online, sessions := 0, 0
	for _, m := range fleet {
		row := fleetMachineRow(m, current)
		if row.Online {
			online++
		}
		sessions += len(row.Sessions)
		rows = append(rows, row)
	}
	body, err := json.MarshalIndent(map[string]any{
		"machines":        rows,
		"machines_online": online,
		"machines_total":  len(rows),
		"sessions":        sessions,
	}, "", "  ")
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "No machines. This device has no reminal identity yet.", nil
	}
	return string(body), nil
}

func mcpSearchSessions(pattern string) (string, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("invalid regex: %w", err)
	}
	fleet, err := client.CollectFleet(pattern)
	if err != nil {
		return "", err
	}
	localHits := localTranscriptHits(pattern)
	var hits []mcpSearchHit
	for _, m := range fleet {
		for _, s := range m.Sessions {
			hit := matchSession(m, s, re, localHits[s.ID])
			if hit != nil {
				hits = append(hits, *hit)
			}
		}
	}
	body, err := json.MarshalIndent(map[string]any{
		"pattern": pattern,
		"matches": hits,
		"count":   len(hits),
	}, "", "  ")
	if err != nil {
		return "", err
	}
	if len(hits) == 0 {
		return fmt.Sprintf("No sessions matched %q.\n%s", pattern, body), nil
	}
	return string(body), nil
}

func fleetMachineRow(m client.FleetMachine, currentID string) mcpMachineRow {
	row := mcpMachineRow{
		ID:       m.ID,
		Name:     m.Name,
		Hostname: m.Hostname,
		Local:    m.Local,
		Online:   m.Online,
		Error:    m.Error,
		Battery:  mcpBatteryOf(m.Battery),
		Sessions: make([]mcpSessionRow, 0, len(m.Sessions)),
	}
	for _, s := range m.Sessions {
		row.Sessions = append(row.Sessions, mcpSessionRow{
			ID:       s.ID,
			Name:     s.Name,
			Path:     s.Cwd,
			Title:    s.Title,
			Kind:     s.Kind,
			Port:     s.Port,
			Headless: s.Headless,
			Viewers:  s.Viewers,
			IdleSecs: s.IdleSecs,
			Attn:     s.Attn,
			ResetsAt: resetsAtRFC3339(s.ResetsAt),
			Shells:   s.Shells,
			Current:  currentID != "" && strings.EqualFold(s.ID, currentID),
			Asleep:   s.Asleep,
			SleptAt:  resetsAtRFC3339(s.SleptAt),
			Dropped:  s.Dropped,
		})
	}
	return row
}

func matchSession(m client.FleetMachine, s protocol.DirSession, re *regexp.Regexp, extra []string) *mcpSearchHit {
	var where []string
	var snippets []string
	add := func(field, value string) {
		if value != "" && re.MatchString(value) {
			where = append(where, field)
			snippets = append(snippets, value)
		}
	}
	add("id", s.ID)
	add("name", s.Name)
	add("path", s.Cwd)
	add("title", s.Title)

	hits := extra
	if len(hits) == 0 {
		hits = s.SearchHits
	}
	if len(hits) > 0 {
		where = append(where, "transcript")
		snippets = append(snippets, hits...)
	}
	if len(where) == 0 {
		return nil
	}
	return &mcpSearchHit{
		Machine:  m.Name,
		Session:  s.ID,
		Name:     s.Name,
		Path:     s.Cwd,
		Title:    s.Title,
		Where:    where,
		Snippets: snippets,
	}
}

// localTranscriptHits asks each live local agent for scrollback matches.
// Remote hits arrive on DirSession.SearchHits from CollectFleet.
func localTranscriptHits(pattern string) map[string][]string {
	all, err := session.ReadAllActive()
	if err != nil {
		return nil
	}
	out := map[string][]string{}
	for _, a := range all {
		if a.IsPort() || a.PID <= 0 {
			continue
		}
		hits, err := client.SearchAgentTranscript(a.PID, pattern)
		if err != nil || len(hits) == 0 {
			continue
		}
		out[a.ID] = hits
	}
	// The sessions asleep: what they saved, searched without waking them.
	for id := range session.AsleepIDs() {
		if hits := client.SearchSleeperTranscript(id, pattern); len(hits) > 0 {
			out[id] = hits
		}
	}
	return out
}

func mcpReadTranscript(sessionSel, machineSel, pin string) (string, error) {
	sessionSel = strings.TrimSpace(sessionSel)
	machineSel = strings.TrimSpace(machineSel)
	pin = strings.TrimSpace(pin)
	if sessionSel == "" {
		return "", fmt.Errorf("session is required (id from list_sessions, or a join URL)")
	}

	// PIN path: read any reminal you have the id and PIN for, even one this
	// device doesn't own — the read counterpart to send_keys' PIN path. Mirrors
	// the same id/URL parsing so "id + pin" and a join URL both work.
	id, urlPin := parseConnectTarget(sessionSel)
	if pin == "" {
		pin = urlPin
	}
	if pin != "" {
		if id == "" {
			id = strings.ToUpper(sessionSel)
		}
		text, truncated, err := client.ReadTranscriptPIN(id, pin)
		if err != nil {
			return "", err
		}
		return formatTranscriptJSON(transcriptMeta{ID: id, Machine: "remote (PIN)", Truncated: truncated}, text), nil
	}

	refs, err := findSessionRefs(sessionSel, machineSel)
	if err != nil {
		return "", err
	}
	switch len(refs) {
	case 0:
		// Local-only shortcut: session may exist here even if fleet lookup missed.
		if machineSel == "" || machineLooksLocal(machineSel) {
			if text, meta, err := readLocalTranscript(sessionSel); err == nil {
				return formatTranscriptJSON(meta, text), nil
			} else if !errors.Is(err, errNotLocal) {
				return "", err
			}
		}
		return "", fmt.Errorf("no session matching %q — run list_sessions", sessionSel)
	case 1:
		return readOneTranscript(refs[0])
	default:
		return "", ambiguousSession(sessionSel, refs)
	}
}

func findSessionRefs(sessionSel, machineSel string) ([]transcriptRef, error) {
	fleet, err := client.CollectFleet("")
	if err != nil {
		return nil, err
	}
	var hits []transcriptRef
	for _, m := range fleet {
		if !machineMatches(m, machineSel) {
			continue
		}
		for _, s := range m.Sessions {
			if sessionMatches(s, sessionSel) {
				hits = append(hits, transcriptRef{m: m, s: s})
			}
		}
	}
	return hits, nil
}

func ambiguousSession(sessionSel string, hits []transcriptRef) error {
	var b strings.Builder
	fmt.Fprintf(&b, "session %q matches more than one machine — pass machine:", sessionSel)
	for _, h := range hits {
		fmt.Fprintf(&b, "\n  %s  %s  %s", h.s.ID, h.m.Name, h.s.Cwd)
	}
	return fmt.Errorf("%s", b.String())
}

// typedReport says whether the Return was actually pressed. "Typed N byte(s)"
// alone reads like delivery confirmation and is not: the text can be sitting in
// the target's input box, unsent, which is exactly how a briefing to another
// agent went missing while the sender believed it had arrived.
func typedReport(n int, pressed bool, target string) string {
	if !pressed {
		return fmt.Sprintf("Typed %d byte(s) into %s (no Return — the text is in the input box, not submitted).", n, target)
	}
	return fmt.Sprintf("Typed %d byte(s) into %s, then pressed Return. %s", n, target, confirmLanded)
}

// confirmLanded rides on every report of a pressed Return. The Return reaching
// the PTY is not the message being accepted: a busy agent or a slow redraw can
// swallow it and leave the text in the input box, so the caller has to look.
const confirmLanded = "Confirm with read_transcript that it was submitted and is not still in the input box."

// returnPressed is whether a send_keys delivery ended in a Return — either the
// separate one in tail, or one inside body itself: a bare Return (keys="" with
// enter=true, the way to submit text left in the input box) or keys ending in
// a newline.
func returnPressed(body, tail []byte) bool {
	return tail != nil || bytes.HasSuffix(body, []byte{'\r'})
}

// sendKeysPlan is what one send_keys call delivers.
type sendKeysPlan struct {
	pressed    [][]byte // the keys named in key, one each
	body, tail []byte   // the text, and the Return sent after it (nil: none)
	// deliveries is everything sent, in order, a moment apart: the named
	// keys, the text, then the Return.
	deliveries [][]byte
}

// planSendKeys turns send_keys' arguments into what it sends. Keys named in
// key are pressed first, each on its own (see client.NamedKeys): Ctrl-C to
// stop what is running, Ctrl-U to clear the line, and then whatever keys
// types — literally, a backslash sequence in it included.
//
// The Return travels as its own delivery a moment after the text — see
// client.PrepareInjectKeysSplit. It has to be split here rather than at the
// PTY, because the agent-side write for the PIN path is the same one that
// carries live keystrokes from a browser viewer, and pausing before a
// person's Enter would be a real regression.
func planSendKeys(keys, key string, enter bool) (sendKeysPlan, error) {
	var p sendKeysPlan
	var err error
	if p.pressed, err = client.NamedKeys(key); err != nil {
		return p, err
	}
	switch {
	case keys == "" && len(p.pressed) > 0:
		// Only keys pressed by name, and perhaps a Return after them.
		if enter {
			p.tail = []byte{'\r'}
		}
	default:
		if p.body, p.tail, err = client.PrepareInjectKeysSplit(keys, enter); err != nil {
			return p, err
		}
	}
	p.deliveries = append(p.deliveries, p.pressed...)
	if len(p.body) > 0 {
		p.deliveries = append(p.deliveries, p.body)
	}
	if p.tail != nil {
		p.deliveries = append(p.deliveries, p.tail)
	}
	return p, nil
}

func mcpSendKeys(sessionSel, machineSel, keys, key, pin string, enter bool) (string, error) {
	sessionSel = strings.TrimSpace(sessionSel)
	machineSel = strings.TrimSpace(machineSel)
	pin = strings.TrimSpace(pin)
	if sessionSel == "" {
		return "", fmt.Errorf("session is required (id from list_sessions, or a join URL)")
	}
	plan, err := planSendKeys(keys, key, enter)
	if err != nil {
		return "", err
	}
	pressed, body, tail, deliveries := plan.pressed, plan.body, plan.tail, plan.deliveries
	// sendRest sends what follows the first delivery. A failure here is worth
	// reporting: what went before landed, so silently swallowing it would
	// leave the caller believing a message was sent when it is actually
	// sitting in the target's input box.
	sendRest := func(send func([]byte) error) error {
		for i, d := range deliveries[1:] {
			time.Sleep(client.EnterSettle)
			if err := send(d); err != nil {
				if tail != nil && i == len(deliveries)-2 {
					return fmt.Errorf("typed the text but could not press Return: %w", err)
				}
				return fmt.Errorf("sent part of it, then: %w", err)
			}
		}
		return nil
	}
	report := func(target string) string {
		if len(pressed) == 0 {
			return typedReport(len(body), returnPressed(body, tail), target)
		}
		said := "Pressed " + strings.Join(client.KeyNames(key), " ") + " in " + target
		switch {
		case len(body) > 0 && returnPressed(body, tail):
			return fmt.Sprintf("%s, then typed %d byte(s) and pressed Return. %s", said, len(body), confirmLanded)
		case len(body) > 0:
			return fmt.Sprintf("%s, then typed %d byte(s) (no Return — the text is in the input box, not submitted).", said, len(body))
		case tail != nil:
			return said + ", then Return. " + confirmLanded
		}
		return said + "."
	}

	id, urlPin := parseConnectTarget(sessionSel)
	if pin == "" {
		pin = urlPin
	}
	if pin != "" {
		if id == "" {
			id = strings.ToUpper(sessionSel)
		}
		send := func(b []byte) error { return client.SendKeysPIN(id, pin, b) }
		if err := send(deliveries[0]); err != nil {
			return "", err
		}
		if err := sendRest(send); err != nil {
			return "", err
		}
		return report(id), nil
	}

	if machineSel == "" || machineLooksLocal(machineSel) {
		// Asleep here: woken first, the keys held until its agent is back at
		// its prompt (never typed into a shell the agent is still starting in).
		if err := wakeForKeys(sessionSel); err != nil {
			return "", err
		}
		send := func(b []byte) error { return injectLocalSession(sessionSel, b) }
		if err := send(deliveries[0]); err == nil {
			if err := sendRest(send); err != nil {
				return "", err
			}
			return report(sessionSel), nil
		} else if !errors.Is(err, errNotLocal) {
			return "", err
		}
	}

	refs, err := findSessionRefs(sessionSel, machineSel)
	if err != nil {
		return "", err
	}
	switch len(refs) {
	case 0:
		return "", fmt.Errorf("no session matching %q — pass pin to type into an unowned reminal, or run list_sessions", sessionSel)
	case 1:
		send := func(b []byte) error { _, e := sendOneKeys(refs[0], b); return e }
		if err := send(deliveries[0]); err != nil {
			return "", err
		}
		if err := sendRest(send); err != nil {
			return "", err
		}
		// Name the machine, which is worth keeping.
		return report(refs[0].s.ID + " on " + refs[0].m.Name), nil
	default:
		return "", ambiguousSession(sessionSel, refs)
	}
}

func injectLocalSession(sessionSel string, data []byte) error {
	all, err := session.ReadAllActive()
	if err != nil {
		return err
	}
	var match []session.Active
	for _, a := range all {
		if strings.EqualFold(a.ID, sessionSel) || (a.Name != "" && strings.EqualFold(a.Name, sessionSel)) {
			match = append(match, a)
		}
	}
	if len(match) == 0 {
		return errNotLocal
	}
	if len(match) > 1 {
		return fmt.Errorf("session %q is ambiguous locally — use the id from list_sessions", sessionSel)
	}
	a := match[0]
	if a.IsPort() {
		return fmt.Errorf("session %s is a port forward — no terminal", a.ID)
	}
	return client.InjectAgentKeys(a.PID, data)
}

func sendOneKeys(h transcriptRef, data []byte) (string, error) {
	if h.s.Kind == "port" {
		return "", fmt.Errorf("session %s is a port forward — no terminal", h.s.ID)
	}
	if h.m.Local {
		if err := client.InjectKeysByID(h.s.ID, data); err != nil {
			return "", err
		}
		return fmt.Sprintf("Typed %d byte(s) into %s on %s.", len(data), h.s.ID, h.m.Name), nil
	}
	ok, err := client.SendRemoteKeys(h.m.Key, h.s.ID, data)
	if err != nil {
		return "", onMachineErr(h, err)
	}
	if !ok {
		return "", fmt.Errorf("session %s is on %s, but that reminal is too old to accept keystrokes — upgrade it", h.s.ID, h.m.Name)
	}
	return fmt.Sprintf("Typed %d byte(s) into %s on %s.", len(data), h.s.ID, h.m.Name), nil
}

// onMachineErr says which session and machine a remote call was about when
// the machine was too old for it: "session X is on Y, but its reminal (3.1.4)
// is too old to …".
func onMachineErr(h transcriptRef, err error) error {
	var old *client.HostTooOldError
	if errors.As(err, &old) {
		return fmt.Errorf("session %s is on %s, but %w", h.s.ID, h.m.Name, err)
	}
	return err
}

type transcriptRef struct {
	m client.FleetMachine
	s protocol.DirSession
}

type transcriptMeta struct {
	ID, Name, Path, Title, Machine string
	Truncated                      bool
	// Attn and ResetsAt as list_sessions has them, when known.
	Attn, ResetsAt string
}

// resetsAtRFC3339 is a limit's reset time (unix seconds) for the wire; "" for
// none.
func resetsAtRFC3339(unix int64) string {
	if unix <= 0 {
		return ""
	}
	return time.Unix(unix, 0).Format(time.RFC3339)
}

var errNotLocal = fmt.Errorf("not a local session")

func sessionMatches(s protocol.DirSession, sel string) bool {
	if strings.EqualFold(s.ID, sel) {
		return true
	}
	return s.Name != "" && strings.EqualFold(s.Name, sel)
}

func machineMatches(m client.FleetMachine, sel string) bool {
	sel = strings.TrimSuffix(sel, "…") // short ids used to be printed with one
	if sel == "" {
		return true
	}
	if strings.EqualFold(m.Name, sel) || strings.EqualFold(m.Hostname, sel) {
		return true
	}
	low := strings.ToLower(sel)
	return strings.EqualFold(m.ID, sel) ||
		strings.HasPrefix(strings.ToLower(m.ID), low) ||
		strings.Contains(strings.ToLower(m.ShortID), low)
}

func machineLooksLocal(sel string) bool {
	sel = strings.TrimSuffix(sel, "…") // short ids used to be printed with one
	if sel == "" || strings.EqualFold(sel, "local") {
		return true
	}
	host, _ := os.Hostname()
	if host != "" && strings.EqualFold(sel, host) {
		return true
	}
	localKey, err := client.MachinePub()
	if err != nil || localKey == nil {
		return false
	}
	id := client.MachineID(localKey)
	short := client.ShortMachineID(localKey)
	low := strings.ToLower(sel)
	return strings.EqualFold(id, sel) ||
		strings.HasPrefix(strings.ToLower(id), low) ||
		strings.Contains(strings.ToLower(short), low)
}

func readLocalTranscript(sessionSel string) (string, transcriptMeta, error) {
	all, err := session.ReadAllActive()
	if err != nil {
		return "", transcriptMeta{}, err
	}
	var match []session.Active
	for _, a := range all {
		if strings.EqualFold(a.ID, sessionSel) || (a.Name != "" && strings.EqualFold(a.Name, sessionSel)) {
			match = append(match, a)
		}
	}
	if len(match) == 0 {
		return "", transcriptMeta{}, errNotLocal
	}
	if len(match) > 1 {
		return "", transcriptMeta{}, fmt.Errorf("session %q is ambiguous locally — use the id from list_sessions", sessionSel)
	}
	a := match[0]
	if a.IsPort() {
		return "", transcriptMeta{}, fmt.Errorf("session %s is a port forward — no terminal transcript", a.ID)
	}
	text, truncated, err := client.ReadAgentTranscript(a.PID)
	if err != nil {
		return "", transcriptMeta{}, err
	}
	meta := transcriptMeta{
		ID: a.ID, Name: a.Name, Path: a.Cwd, Title: a.Title,
		Machine: "this machine", Truncated: truncated, Attn: a.Attn,
	}
	if a.Attn == "limited" && !a.ResetsAt.IsZero() {
		meta.ResetsAt = a.ResetsAt.Format(time.RFC3339)
	}
	return text, meta, nil
}

func readOneTranscript(h transcriptRef) (string, error) {
	if h.s.Kind == "port" {
		return "", fmt.Errorf("session %s is a port forward — no terminal transcript", h.s.ID)
	}
	meta := transcriptMeta{
		ID: h.s.ID, Name: h.s.Name, Path: h.s.Cwd, Title: h.s.Title, Machine: h.m.Name,
		Attn: h.s.Attn, ResetsAt: resetsAtRFC3339(h.s.ResetsAt),
	}
	if h.m.Local {
		text, truncated, err := client.ReadAgentTranscriptFromID(h.s.ID)
		if err != nil {
			return "", err
		}
		meta.Truncated = truncated
		return formatTranscriptJSON(meta, text), nil
	}
	text, truncated, ok, err := client.ReadRemoteTranscript(h.m.Key, h.s.ID)
	if err != nil {
		return "", onMachineErr(h, err)
	}
	if !ok {
		return "", fmt.Errorf("session %s is on %s, but that reminal is too old to dump scrollback — upgrade it", h.s.ID, h.m.Name)
	}
	meta.Truncated = truncated
	return formatTranscriptJSON(meta, text), nil
}

func formatTranscriptJSON(meta transcriptMeta, text string) string {
	row := map[string]any{
		"session": meta.ID,
		"machine": meta.Machine,
		"text":    text,
	}
	if meta.Name != "" {
		row["name"] = meta.Name
	}
	if meta.Path != "" {
		row["path"] = meta.Path
	}
	if meta.Title != "" {
		row["title"] = meta.Title
	}
	if meta.Truncated {
		row["truncated"] = true
	}
	if meta.Attn != "" {
		row["attn"] = meta.Attn
	}
	if meta.ResetsAt != "" {
		row["resets_at"] = meta.ResetsAt
	}
	body, err := json.MarshalIndent(row, "", "  ")
	if err != nil {
		return text
	}
	return string(body)
}
