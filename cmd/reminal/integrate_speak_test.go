// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
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

func allowOf(m map[string]any) []any {
	p, _ := m["permissions"].(map[string]any)
	l, _ := p["allow"].([]any)
	return l
}

// Claude Code: speak goes into the person's permissions.allow beside what
// they allowed themselves, once however often integrate runs; --remove takes
// out that entry and nothing of theirs.
func TestClaudeAllowsSpeakKeepingThePersonsOwn(t *testing.T) {
	home := t.TempDir()
	c := targetNamed(t, "claude")
	path := filepath.Join(home, ".claude", "settings.json")
	theirs := `{"model":"opus","permissions":{"allow":["Bash(git status)","mcp__other__tool"],"deny":["Bash(rm *)"],"defaultMode":"default"}}`
	writeFile(t, path, theirs)
	for run := 0; run < 3; run++ {
		if err := c.speak.apply(home, false); err != nil {
			t.Fatal(err)
		}
	}
	m := readJSON(t, path)
	if got, want := allowOf(m), []any{"Bash(git status)", "mcp__other__tool", "mcp__reminal__speak"}; !reflect.DeepEqual(got, want) {
		t.Errorf("allow after three runs: %v, want %v", got, want)
	}
	p := m["permissions"].(map[string]any)
	if m["model"] != "opus" || p["defaultMode"] != "default" || !reflect.DeepEqual(p["deny"], []any{"Bash(rm *)"}) {
		t.Errorf("the person's settings changed: %v", m)
	}
	if b, _ := os.ReadFile(path + ".bak"); string(b) != theirs {
		t.Errorf("backup: %s", b)
	}
	if !c.speak.allowed(home) {
		t.Error("check does not read it back")
	}
	if err := c.speak.apply(home, true); err != nil {
		t.Fatal(err)
	}
	if got, want := allowOf(readJSON(t, path)), []any{"Bash(git status)", "mcp__other__tool"}; !reflect.DeepEqual(got, want) {
		t.Errorf("allow after --remove: %v, want %v", got, want)
	}
	if c.speak.allowed(home) {
		t.Error("check still reads it after --remove")
	}
}

// With no settings file, or one with no permissions, integrate makes what
// it needs; an allow list already naming speak is not rewritten.
func TestClaudeAllowsSpeakFromNothing(t *testing.T) {
	home := t.TempDir()
	c := targetNamed(t, "claude")
	if err := c.speak.apply(home, false); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	if got := allowOf(readJSON(t, path)); !reflect.DeepEqual(got, []any{"mcp__reminal__speak"}) {
		t.Errorf("allow: %v", got)
	}
	// Hooks are written into the same file: the two keep each other.
	if err := applyHooks(c.hooks, home, "/usr/local/bin/reminal", false); err != nil {
		t.Fatal(err)
	}
	if err := c.speak.apply(home, false); err != nil {
		t.Fatal(err)
	}
	m := readJSON(t, path)
	if got := allowOf(m); !reflect.DeepEqual(got, []any{"mcp__reminal__speak"}) {
		t.Errorf("allow beside the hooks: %v", got)
	}
	if _, ok := m["hooks"].(map[string]any)["Stop"]; !ok {
		t.Errorf("hooks lost: %v", m)
	}

	already := `{"permissions":{"allow":["mcp__reminal__speak"]}}`
	writeFile(t, path, already)
	if err := c.speak.apply(home, false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != already {
		t.Errorf("an allow list already naming speak was rewritten: %s", b)
	}
	// A removal with no file is nothing to do, and makes none.
	empty := t.TempDir()
	if err := c.speak.apply(empty, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(empty, ".claude", "settings.json")); !os.IsNotExist(err) {
		t.Errorf("--remove made a settings file: %v", err)
	}
}

// A file it cannot read as settings is left exactly as it was, and said.
func TestSpeakLeavesAnUnreadableSettingsFileAlone(t *testing.T) {
	c := targetNamed(t, "claude")
	for name, body := range map[string]string{
		"not JSON":             "{ // a comment\n}",
		"allow not a list":     `{"permissions":{"allow":"mcp__x"}}`,
		"permissions a string": `{"permissions":"none"}`,
	} {
		home := t.TempDir()
		path := filepath.Join(home, ".claude", "settings.json")
		writeFile(t, path, body)
		if err := c.speak.apply(home, false); err == nil {
			t.Errorf("%s: no error", name)
		}
		if b, _ := os.ReadFile(path); string(b) != body {
			t.Errorf("%s: changed to %s", name, b)
		}
		_ = c.speak.apply(home, true) // an error or nothing to do: either way untouched
		if b, _ := os.ReadFile(path); string(b) != body {
			t.Errorf("%s: --remove changed it to %s", name, b)
		}
	}
}

// Qwen Code: reminal's entry loads its tools up front (not deferred behind
// tool_search, where the model never sees speak), and speak is allowed;
// the person's own servers and allow entries stay. An entry an older reminal
// wrote reads back as speak not set up.
func TestQwenLoadsReminalsToolsAndAllowsSpeak(t *testing.T) {
	home := t.TempDir()
	exe := "/usr/local/bin/reminal"
	q := targetNamed(t, "qwen")
	path := filepath.Join(home, ".qwen", "settings.json")
	writeFile(t, path, `{"security":{"auth":{"selectedType":"openai"}},"mcpServers":{"mine":{"command":"x"},"reminal":{"command":"/old/reminal","args":["mcp"]}},"permissions":{"allow":["ShellTool"]}}`)
	if q.speak.allowed(home) {
		t.Error("an entry an older reminal wrote reads as speak set up")
	}
	for run := 0; run < 2; run++ {
		if err := applyViaFile(q, home, exe, false); err != nil {
			t.Fatal(err)
		}
		if err := q.speak.apply(home, false); err != nil {
			t.Fatal(err)
		}
	}
	m := readJSON(t, path)
	servers := m["mcpServers"].(map[string]any)
	e := servers[mcpServerName].(map[string]any)
	if e["command"] != exe || e["alwaysLoadTools"] != true {
		t.Errorf("reminal's entry: %v", e)
	}
	if _, ok := servers["mine"]; !ok {
		t.Errorf("the person's server is gone: %v", servers)
	}
	if got := allowOf(m); !reflect.DeepEqual(got, []any{"ShellTool", "mcp__reminal__speak"}) {
		t.Errorf("allow: %v", got)
	}
	if st := checkIntegration(q, home, exe); !st.Integrated || !st.Current || !st.SpeakWanted || !st.Speak {
		t.Errorf("check: %+v", st)
	}
	if err := applyViaFile(q, home, exe, true); err != nil {
		t.Fatal(err)
	}
	if err := q.speak.apply(home, true); err != nil {
		t.Fatal(err)
	}
	m = readJSON(t, path)
	if _, ok := m["mcpServers"].(map[string]any)[mcpServerName]; ok {
		t.Error("reminal's entry stayed after --remove")
	}
	if got := allowOf(m); !reflect.DeepEqual(got, []any{"ShellTool"}) {
		t.Errorf("allow after --remove: %v", got)
	}
}

// Gemini CLI: one rule of its policy engine, for speak only, in a file of
// reminal's own beside the person's policies; --remove deletes that file.
func TestGeminiAllowsOnlySpeakInAPolicyOfItsOwn(t *testing.T) {
	home := t.TempDir()
	exe := "/usr/local/bin/reminal"
	g := targetNamed(t, "gemini")
	mine := filepath.Join(home, ".gemini", "policies", "mine.toml")
	writeFile(t, mine, "[[rule]]\ntoolName = \"run_shell_command\"\ndecision = \"ask_user\"\npriority = 10\n")
	for run := 0; run < 2; run++ {
		if err := applyViaFile(g, home, exe, false); err != nil {
			t.Fatal(err)
		}
		if err := g.speak.apply(home, false); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(filepath.Join(home, ".gemini", "policies", "reminal.toml"))
	if err != nil {
		t.Fatal(err)
	}
	rules := strings.Count(string(b), "[[rule]]")
	for _, want := range []string{`mcpName = "reminal"`, `toolName = "speak"`, `decision = "allow"`, "priority = 950"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("policy has no %s:\n%s", want, b)
		}
	}
	if rules != 1 || strings.Contains(string(b), "*") {
		t.Errorf("the policy allows more than speak:\n%s", b)
	}
	// Not by trusting the whole server, which allows every reminal tool.
	e := readJSON(t, filepath.Join(home, ".gemini", "settings.json"))["mcpServers"].(map[string]any)[mcpServerName].(map[string]any)
	if _, ok := e["trust"]; ok {
		t.Errorf("reminal's server is trusted: %v", e)
	}
	if st := checkIntegration(g, home, exe); !st.SpeakWanted || !st.Speak {
		t.Errorf("check: %+v", st)
	}
	if err := g.speak.apply(home, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".gemini", "policies", "reminal.toml")); !os.IsNotExist(err) {
		t.Errorf("policy still there after --remove: %v", err)
	}
	if _, err := os.Stat(mine); err != nil {
		t.Errorf("the person's policy: %v", err)
	}
	if err := g.speak.apply(home, true); err != nil {
		t.Errorf("a second --remove: %v", err)
	}
}

// Only the agents that ask before each tool call get speak allowed; the
// plan line says so for them.
func TestSpeakAllowedOnlyWhereItAsks(t *testing.T) {
	for _, tg := range agentTargets() {
		want := tg.Bin == "claude" || tg.Bin == "gemini" || tg.Bin == "qwen"
		if (tg.speak != nil) != want {
			t.Errorf("%s: speak allowance %v, want %v", tg.Name, tg.speak != nil, want)
		}
	}
	if !strings.HasPrefix(speakPermission, "mcp__reminal__") || speakPermission != "mcp__reminal__speak" {
		t.Errorf("speakPermission %q", speakPermission)
	}
}
