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
//   - Cursor CLI: sessionStart, as a new chat starts (and its turns:
//     beforeSubmitPrompt, stop).
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

// Cursor CLI: a turn's start and end and its own start, in a
// ~/.cursor/hooks.json of its shape ({version: 1, hooks: {event:
// [{command}]}}). Merged into the person's own file: their hooks and their
// version field kept, ours once however often integrate runs. --remove takes
// out only ours, and deletes the file only if reminal made it and nothing
// else is in it. The plan line says it is merged.
func TestCursorHooksMergeIntoThePersonsFile(t *testing.T) {
	exe := "/usr/local/bin/reminal"
	c := targetNamed(t, "cursor-agent")
	if c.hooks == nil {
		t.Fatal("Cursor CLI has no hooks")
	}
	if !strings.Contains(c.hooks.planNote, "~/.cursor/hooks.json") || !strings.Contains(c.hooks.planNote, "merged") {
		t.Errorf("plan line: %q", c.hooks.planNote)
	}
	want := map[string]string{"beforeSubmitPrompt": "working", "stop": "done", "sessionStart": "ready"}
	read := func(path string) map[string]any {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]any{}
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("%v: %s", err, b)
		}
		return m
	}
	ours := func(m map[string]any) map[string]int {
		n := map[string]int{}
		hooks, _ := m["hooks"].(map[string]any)
		for ev, v := range hooks {
			list, _ := v.([]any)
			for _, item := range list {
				if e, ok := item.(map[string]any); ok && e["command"] == exe+" hook "+want[ev] {
					n[ev]++
				}
			}
		}
		return n
	}
	apply := func(home string, remove bool, times int) {
		for i := 0; i < times; i++ {
			if err := applyHooks(c.hooks, home, exe, remove); err != nil {
				t.Fatal(err)
			}
		}
	}

	// Nothing there: made, then gone with ours (after several runs, so a
	// backup of reminal's own file is there too).
	home := t.TempDir()
	path := filepath.Join(home, ".cursor", "hooks.json")
	apply(home, false, 3)
	m := read(path)
	if v, _ := m["version"].(float64); v != 1 {
		t.Errorf("no version 1: %v", m)
	}
	if n := ours(m); len(n) != 3 || n["beforeSubmitPrompt"] != 1 || n["stop"] != 1 || n["sessionStart"] != 1 {
		t.Errorf("ours: %v", n)
	}
	if !hooksInstalled(c.hooks, home) {
		t.Error("check does not read it back")
	}
	apply(home, true, 1)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("a file reminal made is still there after --remove: %v", err)
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Errorf("its backup is still there: %v", err)
	}

	// Made by reminal, and the person added a hook of their own since: kept.
	home = t.TempDir()
	path = filepath.Join(home, ".cursor", "hooks.json")
	apply(home, false, 1)
	m = read(path)
	m["hooks"].(map[string]any)["afterFileEdit"] = []any{map[string]any{"command": "./fmt.sh"}}
	writeJSONFile(t, path, m)
	apply(home, true, 1)
	if m := read(path); len(ours(m)) != 0 || !strings.Contains(string(mustJSON(t, m)), "./fmt.sh") {
		t.Errorf("after --remove, with a hook of theirs: %v", m)
	}

	// The person's own file, its version theirs: merged into, and left as it
	// was by --remove (not deleted, though only an empty list of theirs is
	// in it).
	for name, theirs := range map[string]string{
		"hooks of theirs": `{"version":1,"hooks":{"sessionStart":[{"command":"./audit.sh"}],"stop":[{"command":"say done"}]}}`,
		"another version": `{"version":2,"hooks":{"stop":[{"command":"say done"}]}}`,
		"nothing in it":   `{"version":1,"hooks":{}}`,
	} {
		home := t.TempDir()
		path := filepath.Join(home, ".cursor", "hooks.json")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(theirs), 0o600); err != nil {
			t.Fatal(err)
		}
		apply(home, false, 3)
		m := read(path)
		if n := ours(m); len(n) != 3 || n["stop"] != 1 {
			t.Errorf("%s: ours %v", name, n)
		}
		before := map[string]any{}
		_ = json.Unmarshal([]byte(theirs), &before)
		if m["version"] != before["version"] {
			t.Errorf("%s: version %v, theirs was %v", name, m["version"], before["version"])
		}
		for _, keep := range []string{"./audit.sh", "say done"} {
			if strings.Contains(theirs, keep) && !strings.Contains(string(mustJSON(t, m)), keep) {
				t.Errorf("%s: their %q is gone", name, keep)
			}
		}
		apply(home, true, 1)
		after := read(path)
		if len(ours(after)) != 0 || after["version"] != before["version"] {
			t.Errorf("%s: after --remove: %v", name, after)
		}
		for _, keep := range []string{"./audit.sh", "say done"} {
			if strings.Contains(theirs, keep) && !strings.Contains(string(mustJSON(t, after)), keep) {
				t.Errorf("%s: after --remove, their %q is gone", name, keep)
			}
		}
	}
}

func writeJSONFile(t *testing.T, path string, v any) {
	t.Helper()
	if err := os.WriteFile(path, mustJSON(t, v), 0o600); err != nil {
		t.Fatal(err)
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

// An agent whose hooks say "ready" says when its turns start ("working")
// too: one that said only "ready" would read as at its prompt for the whole
// of its life, mid-turn and at its own questions as well.
func TestReadyComesWithTurns(t *testing.T) {
	for _, a := range agentTargets() {
		if a.hooks == nil {
			continue
		}
		has := map[string]bool{}
		for _, e := range a.hooks.events {
			has[e.State] = true
		}
		if has["ready"] && !has["working"] {
			t.Errorf("%s: says ready, never working", a.Name)
		}
	}
}
