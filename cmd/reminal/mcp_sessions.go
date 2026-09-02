// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/reminal/reminal/internal/client"
	"github.com/reminal/reminal/internal/protocol"
	"github.com/reminal/reminal/internal/session"
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
	Current  bool   `json:"current,omitempty"`
}

type mcpMachineRow struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Hostname string          `json:"hostname,omitempty"`
	Local    bool            `json:"local,omitempty"`
	Online   bool            `json:"online"`
	Error    string          `json:"error,omitempty"`
	Sessions []mcpSessionRow `json:"sessions"`
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
			Current:  currentID != "" && strings.EqualFold(s.ID, currentID),
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
	return out
}

func mcpReadTranscript(sessionSel, machineSel string) (string, error) {
	sessionSel = strings.TrimSpace(sessionSel)
	machineSel = strings.TrimSpace(machineSel)
	if sessionSel == "" {
		return "", fmt.Errorf("session is required (id from list_sessions)")
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

func mcpSendKeys(sessionSel, machineSel, keys, pin string, enter bool) (string, error) {
	sessionSel = strings.TrimSpace(sessionSel)
	machineSel = strings.TrimSpace(machineSel)
	pin = strings.TrimSpace(pin)
	if sessionSel == "" {
		return "", fmt.Errorf("session is required (id from list_sessions, or a join URL)")
	}
	data, err := client.PrepareInjectKeys(keys, enter)
	if err != nil {
		return "", err
	}

	id, urlPin := parseConnectTarget(sessionSel)
	if pin == "" {
		pin = urlPin
	}
	if pin != "" {
		if id == "" {
			id = strings.ToUpper(sessionSel)
		}
		if err := client.SendKeysPIN(id, pin, data); err != nil {
			return "", err
		}
		return fmt.Sprintf("Typed %d byte(s) into %s.", len(data), id), nil
	}

	if machineSel == "" || machineLooksLocal(machineSel) {
		if err := injectLocalSession(sessionSel, data); err == nil {
			return fmt.Sprintf("Typed %d byte(s) into %s.", len(data), sessionSel), nil
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
		return sendOneKeys(refs[0], data)
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
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("session %s is on %s, but that reminal is too old to accept keystrokes — upgrade it", h.s.ID, h.m.Name)
	}
	return fmt.Sprintf("Typed %d byte(s) into %s on %s.", len(data), h.s.ID, h.m.Name), nil
}

type transcriptRef struct {
	m client.FleetMachine
	s protocol.DirSession
}

type transcriptMeta struct {
	ID, Name, Path, Title, Machine string
	Truncated                      bool
}

var errNotLocal = fmt.Errorf("not a local session")

func sessionMatches(s protocol.DirSession, sel string) bool {
	if strings.EqualFold(s.ID, sel) {
		return true
	}
	return s.Name != "" && strings.EqualFold(s.Name, sel)
}

func machineMatches(m client.FleetMachine, sel string) bool {
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
	return text, transcriptMeta{
		ID: a.ID, Name: a.Name, Path: a.Cwd, Title: a.Title,
		Machine: "this machine", Truncated: truncated,
	}, nil
}

func readOneTranscript(h transcriptRef) (string, error) {
	if h.s.Kind == "port" {
		return "", fmt.Errorf("session %s is a port forward — no terminal transcript", h.s.ID)
	}
	meta := transcriptMeta{
		ID: h.s.ID, Name: h.s.Name, Path: h.s.Cwd, Title: h.s.Title, Machine: h.m.Name,
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
		return "", err
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
	body, err := json.MarshalIndent(row, "", "  ")
	if err != nil {
		return text
	}
	return string(body)
}
