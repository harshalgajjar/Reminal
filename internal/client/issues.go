// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"reminal/internal/session"
)

// A problem an agent had with reminal itself — a notice that was wrong, a
// message it could not act on, a tool that lied — reported to reminal rather
// than to the agent's own harness, whose maker cannot fix reminal.
//
// Reports are files under ~/.reminal/issues/, nothing more: nothing is sent
// anywhere. `reminal issues` lists them and exports them as text for a bug
// report the person chooses to file. In an org the fact of a report, and its
// title, also go on the org's timeline so its owner sees it.

// Issue is one report.
type Issue struct {
	At       time.Time `json:"at"`
	Session  string    `json:"session,omitempty"`
	Title    string    `json:"title"`
	What     string    `json:"what"`
	Expected string    `json:"expected,omitempty"`
	Harness  string    `json:"harness,omitempty"`
	Version  string    `json:"version,omitempty"`
	Screen   string    `json:"screen,omitempty"` // the reporting session's own screen, its last lines
}

func issuesDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".reminal", "issues"), nil
}

// SaveIssue writes a report and returns its path.
func SaveIssue(is Issue) (string, error) {
	dir, err := issuesDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if is.At.IsZero() {
		is.At = time.Now()
	}
	is.Title = strings.TrimSpace(is.Title)
	if is.Title == "" {
		return "", fmt.Errorf("a report needs a title")
	}
	name := is.At.Format("20060102-150405")
	if is.Session != "" {
		name += "-" + is.Session
	}
	path := filepath.Join(dir, name+".json")
	b, err := json.MarshalIndent(is, "", "  ")
	if err != nil {
		return "", err
	}
	return path, os.WriteFile(path, b, 0o600)
}

// ListIssues returns every report, oldest first, with its path.
func ListIssues() ([]Issue, []string, error) {
	dir, err := issuesDir()
	if err != nil {
		return nil, nil, err
	}
	ents, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var out []Issue
	var paths []string
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var is Issue
		if json.Unmarshal(b, &is) != nil {
			continue
		}
		out = append(out, is)
		paths = append(paths, p)
	}
	idx := make([]int, len(out))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return out[idx[a]].At.Before(out[idx[b]].At) })
	sorted, sortedPaths := make([]Issue, len(out)), make([]string, len(out))
	for i, j := range idx {
		sorted[i], sortedPaths[i] = out[j], paths[j]
	}
	return sorted, sortedPaths, nil
}

// ClearIssues removes every report.
func ClearIssues() (int, error) {
	_, paths, err := ListIssues()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, p := range paths {
		if os.Remove(p) == nil {
			n++
		}
	}
	return n, nil
}

// IssueMarkdown is a report as text for a bug report: what the agent said,
// where it was, and the screen it saw — nothing the person did not ask for.
func IssueMarkdown(is Issue) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## %s\n\n", is.Title)
	fmt.Fprintf(&b, "- when: %s\n", is.At.Format(time.RFC3339))
	if is.Version != "" {
		fmt.Fprintf(&b, "- reminal: %s\n", is.Version)
	}
	if is.Harness != "" {
		fmt.Fprintf(&b, "- harness: %s\n", is.Harness)
	}
	if is.Session != "" {
		fmt.Fprintf(&b, "- session: %s\n", is.Session)
	}
	fmt.Fprintf(&b, "\n**What happened**\n\n%s\n", strings.TrimSpace(is.What))
	if strings.TrimSpace(is.Expected) != "" {
		fmt.Fprintf(&b, "\n**Expected**\n\n%s\n", strings.TrimSpace(is.Expected))
	}
	if strings.TrimSpace(is.Screen) != "" {
		fmt.Fprintf(&b, "\n**The screen at the time**\n\n```\n%s\n```\n", strings.TrimSpace(is.Screen))
	}
	return b.String()
}

// OwnScreenTail is the last lines of a session's screen on this machine —
// what the reporting agent was looking at.
func OwnScreenTail(sessionID string, lines int) string {
	act, ok := activeByID(sessionID)
	if !ok {
		return ""
	}
	text, _, err := ReadAgentTranscript(act.PID, 3*time.Second)
	if err != nil {
		return ""
	}
	all := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	return strings.Join(all, "\n")
}

// HarnessOf is the program in a session's foreground, as its record says —
// which agent filed the report, when the report says "the tool misled me".
func HarnessOf(sessionID string) string {
	act, ok := activeByID(sessionID)
	if !ok {
		return ""
	}
	return act.Fg
}

func activeByID(id string) (session.Active, bool) {
	all, err := session.ReadAllActive()
	if err != nil {
		return session.Active{}, false
	}
	for _, a := range all {
		if strings.EqualFold(a.ID, id) {
			return a, true
		}
	}
	return session.Active{}, false
}
