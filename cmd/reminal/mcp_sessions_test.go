// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"reminal/internal/client"
	"reminal/internal/protocol"
)

func TestMCPToolListIncludesSessionTools(t *testing.T) {
	names := map[string]bool{}
	for _, tool := range mcpToolList() {
		name, _ := tool["name"].(string)
		names[name] = true
	}
	for _, want := range []string{"list_sessions", "search_sessions", "read_transcript", "send_keys", "list_windows", "add_note"} {
		if !names[want] {
			t.Errorf("mcpToolList missing %s", want)
		}
	}
}

func TestMCPSendKeysRequiresSessionAndKeys(t *testing.T) {
	if _, err := mcpSendKeys("", "", "ls", "", false); err == nil {
		t.Fatal("expected session required")
	}
	if _, err := mcpSendKeys("VW65K9YU", "", "", "", false); err == nil {
		t.Fatal("expected empty keys error")
	}
}

func TestMCPSendKeysPINRejectsBadPIN(t *testing.T) {
	if _, err := mcpSendKeys("ABCDEFGH", "", "ls", "12", true); err == nil {
		t.Fatal("expected invalid PIN")
	}
}

func TestMCPSearchSessionsRejectsBadPattern(t *testing.T) {
	if _, err := mcpSearchSessions("("); err == nil {
		t.Fatal("expected invalid regex error")
	}
	if _, err := mcpSearchSessions(""); err == nil {
		t.Fatal("expected empty pattern error")
	}
}

func TestMatchSessionMetadataAndTranscript(t *testing.T) {
	m := client.FleetMachine{Name: "windows-box"}
	s := protocol.DirSession{
		ID:    "NFWW9EDD",
		Name:  "work",
		Cwd:   `C:\Users\h\proj`,
		Title: "pwsh",
	}
	re := regexp.MustCompile(`(?i)proj`)
	hit := matchSession(m, s, re, nil)
	if hit == nil {
		t.Fatal("expected path match")
	}
	if hit.Machine != "windows-box" || hit.Session != "NFWW9EDD" {
		t.Fatalf("%+v", hit)
	}
	if !contains(hit.Where, "path") {
		t.Fatalf("where = %v", hit.Where)
	}

	re = regexp.MustCompile("Get-Date")
	hit = matchSession(m, s, re, []string{"PS> Get-Date"})
	if hit == nil || !contains(hit.Where, "transcript") {
		t.Fatalf("transcript hit: %+v", hit)
	}
}

func TestFleetMachineRowUsesPathNotPIN(t *testing.T) {
	m := client.FleetMachine{
		ID:     "mach_abc",
		Name:   "this-mac",
		Local:  true,
		Online: true,
		Sessions: []protocol.DirSession{{
			ID: "VW65K9YU", Cwd: "/Users/h/reminal", Title: "zsh",
		}},
	}
	row := fleetMachineRow(m, "VW65K9YU")
	body, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(body)), "pin") {
		t.Fatalf("PIN leaked: %s", body)
	}
	if row.Sessions[0].Path != "/Users/h/reminal" || !row.Sessions[0].Current {
		t.Fatalf("row = %+v", row.Sessions[0])
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// A send_keys report is what an agent trusts to decide whether its message
// went. A bare Return (keys="" enter=true — how a message stuck in an input box
// is submitted) and keys ending in a newline both press Return, and must not
// be reported as text left unsubmitted, or the agent keeps pressing Return.
func TestSendKeysReportsEveryReturn(t *testing.T) {
	for _, tc := range []struct {
		keys    string
		enter   bool
		pressed bool
	}{
		{"hello", false, false},
		{"hello", true, true},
		{"", true, true},
		{"ls\n", false, true},
		{"ls\n", true, true},
	} {
		body, tail, err := client.PrepareInjectKeysSplit(tc.keys, tc.enter)
		if err != nil {
			t.Fatalf("%q/%v: %v", tc.keys, tc.enter, err)
		}
		if got := returnPressed(body, tail); got != tc.pressed {
			t.Errorf("keys=%q enter=%v: returnPressed=%v, want %v", tc.keys, tc.enter, got, tc.pressed)
		}
		report := typedReport(len(body), returnPressed(body, tail), "S")
		if tc.pressed != strings.Contains(report, "pressed Return") || tc.pressed != strings.Contains(report, "read_transcript") {
			t.Errorf("keys=%q enter=%v: report %q", tc.keys, tc.enter, report)
		}
	}
}
