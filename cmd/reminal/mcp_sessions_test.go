// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"encoding/json"
	"maps"
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
	if _, err := mcpSendKeys("", "", "ls", "", "", false); err == nil {
		t.Fatal("expected session required")
	}
	if _, err := mcpSendKeys("VW65K9YU", "", "", "", "", false); err == nil {
		t.Fatal("expected empty keys error")
	}
}

func TestMCPSendKeysPINRejectsBadPIN(t *testing.T) {
	if _, err := mcpSendKeys("ABCDEFGH", "", "ls", "", "12", true); err == nil {
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

// An agent that cannot put a raw control character in its call names the key:
// Ctrl-C goes first, on its own, then the text, then the Return. keys stays
// literal, so a backslash sequence someone meant to type is typed.
func TestSendKeysNamedKeysGoFirstAndKeysStayLiteral(t *testing.T) {
	for _, tc := range []struct {
		keys, key string
		enter     bool
		want      []string
	}{
		{"", "ctrl-c", false, []string{"\x03"}},
		{"", "Ctrl+C ctrl-c", false, []string{"\x03", "\x03"}},
		{"ls", "ctrl-c", true, []string{"\x03", "ls", "\r"}},
		{"", "esc,esc", false, []string{"\x1b", "\x1b"}},
		{"", "ctrl-c", true, []string{"\x03", "\r"}},
		{"echo hi\n", "ctrl-u", false, []string{"\x15", "echo hi\r"}},
		{"ls", "", true, []string{"ls", "\r"}},
		// Typed as written: nothing in keys is decoded.
		{`\u0003 \x03 ^C C:\new`, "", false, []string{`\u0003 \x03 ^C C:\new`}},
		{`ctrl-c`, "", false, []string{"ctrl-c"}},
	} {
		p, err := planSendKeys(tc.keys, tc.key, tc.enter)
		if err != nil {
			t.Fatalf("keys=%q key=%q: %v", tc.keys, tc.key, err)
		}
		var got []string
		for _, d := range p.deliveries {
			got = append(got, string(d))
		}
		if strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Errorf("keys=%q key=%q enter=%v: sends %q, want %q", tc.keys, tc.key, tc.enter, got, tc.want)
		}
	}
	for _, bad := range []struct{ keys, key string }{
		{"", ""},
		{"", "ctrl-cc"},
		{"ls", "f13"},
		{"", "ctrl-1"},
		{"", "a b c d e f g h i"},
	} {
		if _, err := planSendKeys(bad.keys, bad.key, false); err == nil {
			t.Errorf("keys=%q key=%q: no error", bad.keys, bad.key)
		}
	}
}

// The tool says how to send Ctrl-C, and send_keys no longer needs keys when
// a key is named.
func TestSendKeysToolNamesItsControlKeys(t *testing.T) {
	for _, tool := range mcpToolList() {
		if tool["name"] != "send_keys" {
			continue
		}
		desc, _ := tool["description"].(string)
		schema, _ := tool["inputSchema"].(map[string]any)
		props, _ := schema["properties"].(map[string]any)
		if !strings.Contains(desc, `key="ctrl-c"`) || props["key"] == nil {
			t.Fatalf("send_keys does not say how to send Ctrl-C: %s", desc)
		}
		if req, _ := schema["required"].([]string); contains(req, "keys") {
			t.Fatalf("keys is still required: %v", req)
		}
		return
	}
	t.Fatal("no send_keys tool")
}

// list_sessions says the background work a session's harness still has
// running as `reminal list` does: subagents, and the rest by kind. A kind from
// another machine's record is shown under the same bounds as one counted here.
func TestFleetMachineRowCarriesBackground(t *testing.T) {
	m := client.FleetMachine{ID: "mach_abc", Name: "box", Online: true, Sessions: []protocol.DirSession{{
		ID: "VW65K9YU", Attn: "done", Shells: 1, Agents: 2,
		Other: map[string]int{"background terminal": 1, "moni\x1b[2Jtor": 2, "none": 0},
	}}}
	s := fleetMachineRow(m, "").Sessions[0]
	if want := map[string]int{"background terminal": 1, "moni[2Jtor": 2}; s.Shells != 1 || s.Agents != 2 || !maps.Equal(s.Other, want) {
		t.Fatalf("row = %+v, want 1 shell, 2 agents, %v", s, want)
	}
	body, _ := json.Marshal(fleetMachineRow(client.FleetMachine{ID: "m", Sessions: []protocol.DirSession{{ID: "QUIET123"}}}, ""))
	for _, f := range []string{`"agents"`, `"other"`} {
		if strings.Contains(string(body), f) {
			t.Fatalf("a session with none should carry no %s: %s", f, body)
		}
	}
}
