// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import "testing"

// The answer for each agent is the one the evidence gave, an agent nobody
// looked at is false, and every name answered true is one integrate.go uses
// (a renamed agent would otherwise fall to false in silence).
func TestAgentReloadsTools(t *testing.T) {
	want := map[string]bool{
		"Claude Code":     true,
		"Gemini CLI":      true,
		"Kilo Code CLI":   true,
		"Copilot CLI":     true,
		"pi":              true,
		"Codex CLI":       false,
		"OpenCode":        false,
		"Qwen Code":       false,
		"Cursor CLI":      false,
		"Amp":             false,
		"Antigravity CLI": false,
	}
	known := map[string]bool{}
	for _, a := range agentTargets() {
		known[a.Name] = true
		if got, listed := agentReloadsTools(a.Name), want[a.Name]; got != listed {
			t.Errorf("agentReloadsTools(%q) = %v, want %v", a.Name, got, listed)
		}
	}
	for name := range want {
		if !known[name] {
			t.Errorf("%q is not an agent integrate.go knows", name)
		}
	}
	for _, name := range []string{"", "claude", "Some New Agent"} {
		if agentReloadsTools(name) {
			t.Errorf("agentReloadsTools(%q) = true for an agent with no evidence", name)
		}
	}
}
