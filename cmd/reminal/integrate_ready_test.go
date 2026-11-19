// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Which agents say they are up, at their prompt, before a first message
// ("ready"), and by which of their events. Voice types into an agent with no
// box mark on screen only once its hooks have said something since it
// started: one listed here gets voice at once, the others after their first
// typed message.
//   - Claude Code, Qwen Code, Gemini CLI: SessionStart (startup, resume,
//     clear), at their prompt.
//   - Copilot CLI: sessionStart, with its first prompt.
//   - Cursor CLI: sessionStart, as a new chat starts.
//   - Antigravity CLI: its SessionStart comes with the first message, just
//     before PreInvocation: nothing "working" does not say already.
//   - Codex CLI: none written; its hooks run in an app-server shared by every
//     Codex, as the first one's session, and "ready" there would be another
//     session's.
//   - Amp: no hooks; its plugin events are a turn's (agent.start), not a start.
//   - pi: its extension reports it on session_start (internal/piext).
func TestWhichAgentsSayTheyAreReady(t *testing.T) {
	want := map[string]string{
		"claude":       "SessionStart",
		"qwen":         "SessionStart",
		"gemini":       "SessionStart",
		"copilot":      "sessionStart",
		"cursor-agent": "sessionStart",
	}
	for _, a := range agentTargets() {
		got := ""
		if a.hooks != nil {
			for _, e := range a.hooks.events {
				if e.State == "ready" {
					got = e.Event
				}
			}
		}
		if got != want[a.Bin] {
			t.Errorf("%s: ready on %q, want %q", a.Name, got, want[a.Bin])
		}
	}
}

// Gemini CLI: its SessionStart, beside its other hooks, once however often
// integrate runs; --check reads it back, and an integration from before it
// reads as hooks missing, so the Machines view offers to set it up again.
func TestGeminiSaysWhenItIsReady(t *testing.T) {
	home := t.TempDir()
	exe := "/usr/local/bin/reminal"
	g := targetNamed(t, "gemini")
	path := filepath.Join(home, ".gemini", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	// Set up by a reminal from before: its three events, and one of the
	// person's own at SessionStart.
	before := `{"hooks":{` +
		`"BeforeAgent":[{"hooks":[{"type":"command","command":"/usr/local/bin/reminal hook working"}],"reminalHook":"working"}],` +
		`"Notification":[{"hooks":[{"type":"command","command":"/usr/local/bin/reminal hook input"}],"reminalHook":"input"}],` +
		`"AfterAgent":[{"hooks":[{"type":"command","command":"/usr/local/bin/reminal hook done"}],"reminalHook":"done"}],` +
		`"SessionStart":[{"hooks":[{"type":"command","command":"echo hi"}]}]}}`
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	if hooksInstalled(g.hooks, home) {
		t.Error("hooks from before ready read as set up")
	}
	for i := 0; i < 3; i++ {
		if err := applyHooks(g.hooks, home, exe, false); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := os.ReadFile(path)
	var root struct {
		Hooks map[string][]map[string]any `json:"hooks"`
	}
	if err := json.Unmarshal(b, &root); err != nil {
		t.Fatal(err)
	}
	starts := root.Hooks["SessionStart"]
	ours := 0
	for _, e := range starts {
		if ourHookEntry(e) {
			ours++
			if e[hookMarker] != "ready" || !strings.Contains(string(mustJSON(t, e)), exe+" hook ready") {
				t.Errorf("our SessionStart entry: %v", e)
			}
		}
	}
	if ours != 1 || len(starts) != 2 {
		t.Errorf("SessionStart: %d of ours in %d, want 1 in 2: %s", ours, len(starts), b)
	}
	if !hooksInstalled(g.hooks, home) {
		t.Error("check does not read it back")
	}
	if err := applyHooks(g.hooks, home, exe, true); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	if strings.Contains(string(b), "reminal hook") || !strings.Contains(string(b), "echo hi") {
		t.Errorf("after --remove: %s", b)
	}
}

// Cursor CLI: a ~/.cursor/hooks.json of its shape ({version: 1, hooks:
// {sessionStart: [{command}]}}), the person's own hooks in it kept, once
// however often integrate runs, and taken out again by --remove.
func TestCursorSaysWhenItIsReady(t *testing.T) {
	exe := "/usr/local/bin/reminal"
	c := targetNamed(t, "cursor-agent")
	if c.hooks == nil {
		t.Fatal("Cursor CLI has no hooks")
	}
	// Nothing there yet.
	home := t.TempDir()
	if err := applyHooks(c.hooks, home, exe, false); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".cursor", "hooks.json")
	b, _ := os.ReadFile(path)
	var fresh map[string]any
	if err := json.Unmarshal(b, &fresh); err != nil {
		t.Fatal(err)
	}
	if v, _ := fresh["version"].(float64); v != 1 {
		t.Errorf("no version 1: %s", b)
	}
	hooks, _ := fresh["hooks"].(map[string]any)
	list, _ := hooks["sessionStart"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["command"] != exe+" hook ready" {
		t.Errorf("sessionStart: %s", b)
	}

	// The person's own hooks file.
	home = t.TempDir()
	path = filepath.Join(home, ".cursor", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	theirs := `{"version":1,"hooks":{"sessionStart":[{"command":"./audit.sh"}],"stop":[{"command":"say done"}]}}`
	if err := os.WriteFile(path, []byte(theirs), 0o600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := applyHooks(c.hooks, home, exe, false); err != nil {
			t.Fatal(err)
		}
	}
	b, _ = os.ReadFile(path)
	if n := strings.Count(string(b), "reminal hook ready"); n != 1 {
		t.Errorf("ours %d times, want 1: %s", n, b)
	}
	for _, keep := range []string{"./audit.sh", "say done"} {
		if !strings.Contains(string(b), keep) {
			t.Errorf("the person's %q is gone: %s", keep, b)
		}
	}
	if !hooksInstalled(c.hooks, home) {
		t.Error("check does not read it back")
	}
	if err := applyHooks(c.hooks, home, exe, true); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	if strings.Contains(string(b), "reminal hook") || !strings.Contains(string(b), "./audit.sh") || !strings.Contains(string(b), "say done") {
		t.Errorf("after --remove: %s", b)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
