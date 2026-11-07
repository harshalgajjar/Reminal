// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reminal/internal/session"
)

// Copilot CLI's hooks go in a file of our own in ~/.copilot/hooks, in its
// shape ({"version":1,"hooks":{event:[{type,bash,timeoutSec}]}}): once per
// event however often integrate runs, a person's own entry kept, and taken
// out again by --remove.
func TestCopilotHooksFile(t *testing.T) {
	home := t.TempDir()
	exe := "/usr/local/bin/reminal"
	spec := targetNamed(t, "copilot").hooks
	path := filepath.Join(home, spec.file)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	theirs := `{"version":1,"hooks":{"agentStop":[{"type":"command","bash":"notify-send done"}]}}`
	if err := os.WriteFile(path, []byte(theirs), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := applyHooks(spec, home, exe, false); err != nil {
			t.Fatal(err)
		}
	}
	var got struct {
		Version int                         `json:"version"`
		Hooks   map[string][]map[string]any `json:"hooks"`
	}
	b, _ := os.ReadFile(path)
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Version != 1 {
		t.Errorf("version %d, want 1: %s", got.Version, b)
	}
	for ev, state := range map[string]string{"userPromptSubmitted": "working", "permissionRequest": "input", "agentStop": "done", "sessionStart": "ready"} {
		var ours []map[string]any
		for _, e := range got.Hooks[ev] {
			if e[hookMarker] != nil {
				ours = append(ours, e)
			}
		}
		if len(ours) != 1 {
			t.Fatalf("%s: %d of ours, want 1: %s", ev, len(ours), b)
		}
		if e := ours[0]; e["type"] != "command" || e["bash"] != exe+" hook "+state || e["timeoutSec"] != float64(copilotHookTimeout) {
			t.Errorf("%s: %v", ev, e)
		}
	}
	if !strings.Contains(string(b), "notify-send done") {
		t.Errorf("the person's own hook went: %s", b)
	}
	if err := applyHooks(spec, home, exe, true); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	if strings.Contains(string(b), "reminal hook") || !strings.Contains(string(b), "notify-send done") {
		t.Errorf("after --remove: %s", b)
	}
}

// An entry of ours that lost its tag is still ours by what it runs; one a
// person changed or wrote is not.
func TestCopilotHookEntryIsOursUntagged(t *testing.T) {
	for _, c := range []struct {
		entry map[string]any
		ours  bool
	}{
		{map[string]any{"type": "command", "bash": "/usr/local/bin/reminal hook done", "timeoutSec": 10.0}, true},
		{map[string]any{"type": "command", "bash": "/usr/local/bin/reminal hook done"}, true},
		{map[string]any{"type": "command", "bash": "/usr/local/bin/reminal hook done; say hi"}, false},
		{map[string]any{"type": "command", "bash": "/usr/local/bin/reminal hook done", "cwd": "/tmp"}, false},
		{map[string]any{"type": "prompt", "bash": "/usr/local/bin/reminal hook done"}, false},
		{map[string]any{"type": "command", "bash": "notify-send done"}, false},
	} {
		if got := ourHookEntry(c.entry); got != c.ours {
			t.Errorf("%v: ours %v, want %v", c.entry, got, c.ours)
		}
	}
}

// Copilot CLI fires a subagent's own prompt and stop on the session's hooks,
// naming the agent that started it (its payloads, recorded in testdata/
// copilot): those are not the session's turn. The session's own, and its
// approval, are.
func TestCopilotSubagentTurnsAreNotTheSessions(t *testing.T) {
	for file, sub := range map[string]bool{
		"userPromptSubmitted-subagent": true,
		"agentStop-subagent":           true,
		"userPromptSubmitted":          false,
		"agentStop":                    false,
		"permissionRequest":            false,
	} {
		b, err := os.ReadFile(filepath.Join("testdata", "copilot", file+".json"))
		if err != nil {
			t.Fatal(err)
		}
		if got := subagentTurn(b); got != sub {
			t.Errorf("%s: a subagent's turn %v, want %v", file, got, sub)
		}
	}
	for _, b := range []string{"", "not json", `{"session_id":"abc","hook_event_name":"Stop"}`, `{"parentAgentId":""}`} {
		if subagentTurn([]byte(b)) {
			t.Errorf("%q read as a subagent's turn", b)
		}
	}
}

// What `copilot mcp add` writes (~/.copilot/mcp-config.json, as 1.0.106
// wrote it) reads back as set up.
func TestCheckReadsCopilotsConfig(t *testing.T) {
	home := t.TempDir()
	exe := "/home/u/.local/bin/reminal"
	writeJSON(t, filepath.Join(home, ".copilot", "mcp-config.json"), map[string]any{
		"mcpServers": map[string]any{"reminal": map[string]any{"tools": []string{"*"}, "type": "local", "command": exe, "args": []string{"mcp"}}},
	})
	c := checkIntegration(targetNamed(t, "copilot"), home, exe)
	if !c.Integrated || !c.Current || c.Hooks || !c.HooksWanted {
		t.Fatalf("copilot: %+v", c)
	}
	if err := applyHooks(targetNamed(t, "copilot").hooks, home, exe, false); err != nil {
		t.Fatal(err)
	}
	if c := checkIntegration(targetNamed(t, "copilot"), home, exe); !c.Hooks {
		t.Fatalf("copilot hooks not seen: %+v", c)
	}
}

// Through `reminal hook`, with Copilot CLI's own payloads: the session's
// turn starts and ends as it said, and a background subagent's turn
// starting and ending in between changes neither.
func TestCopilotSubagentTurnLeavesTheSessionsState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("REMINAL_SESSION", "COPI2345")
	payload := func(file string) string {
		b, err := os.ReadFile(filepath.Join("testdata", "copilot", file+".json"))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	state := func() string {
		if hs := session.ReadHookStateAnyAge("COPI2345"); hs != nil {
			return hs.State
		}
		return ""
	}
	hookWithPayload(t, "working", payload("userPromptSubmitted"))
	hookWithPayload(t, "done", payload("agentStop"))
	if got := state(); got != "done" {
		t.Fatalf("the session's turn ended: %q, want done", got)
	}
	hookWithPayload(t, "working", payload("userPromptSubmitted-subagent"))
	if got := state(); got != "done" {
		t.Fatalf("a subagent's turn began: %q, want done still", got)
	}
	hookWithPayload(t, "working", payload("userPromptSubmitted"))
	hookWithPayload(t, "done", payload("agentStop-subagent"))
	if got := state(); got != "working" {
		t.Fatalf("a subagent's turn ended inside the session's: %q, want working still", got)
	}
}

// Copilot CLI names its conversation sessionId (camelCase): what a session
// is resumed on, read from its own Stop.
func TestCopilotsConversationIsReadFromItsHook(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "copilot", "agentStop.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := convFromPayload(b); got != "184da7fe-1615-4edb-b2ea-1d66a4c42d1c" {
		t.Errorf("conversation %q, want its sessionId", got)
	}
}
