// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"os"
	"path/filepath"
	"testing"
)

// Each agent that sleeps has its conversation's file found where it keeps
// it, by id alone; an agent whose files are not known never sleeps.
func TestConvStores(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("PI_CODING_AGENT_DIR", "")
	const conv = "51580c55-63f5-43c1-bbea-4d0425f09835"
	files := map[string]string{
		"claude":  filepath.Join(home, ".claude", "projects", "-work", conv+".jsonl"),
		"qwen":    filepath.Join(home, ".qwen", "projects", "-work", "chats", conv+".jsonl"),
		"pi":      filepath.Join(home, ".pi", "agent", "sessions", "--work--", "2026-10-29T17-51-38-000Z_"+conv+".jsonl"),
		"copilot": filepath.Join(home, ".copilot", "session-state", conv, "events.jsonl"),
	}
	for agent, p := range files {
		if convSize(agent, conv) != 0 {
			t.Fatalf("%s: found before written", agent)
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := convSize(agent, conv); got != 3 {
			t.Errorf("%s: size %d, want 3", agent, got)
		}
		if convSize(agent, "../"+conv) != 0 {
			t.Errorf("%s: a pattern read as an id", agent)
		}
		if !sleepsExactly(agent) {
			t.Errorf("%s does not sleep", agent)
		}
	}
	for agent, rs := range resumers {
		if _, tested := files[agent]; rs.store.file != nil && !tested {
			t.Errorf("%s keeps files this test does not look for", agent)
		}
	}
	if !sleepsExactly("opencode") {
		t.Error("opencode, asked for its conversations, does not sleep")
	}
	if sleepsExactly("gemini") || convSize("gemini", conv) != 0 {
		t.Error("gemini, which resumes only the latest, sleeps")
	}
	// Codex runs its hooks from an app-server shared by every Codex on the
	// machine: which session a conversation is in can't be told.
	if sleepsExactly("codex") {
		t.Error("codex sleeps")
	}
}

// Where an agent keeps its files is read as the person's own shell has it:
// a variable their rc sets reaches a check made by the daemon, which has
// none of it, and so does their PATH.
func TestAgentEnvFollowsTheLoginShell(t *testing.T) {
	dir, bin := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "opencode"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	old := loginEnv
	t.Cleanup(func() { loginEnv = old })
	loginEnv = func() map[string]string { return map[string]string{"CLAUDE_CONFIG_DIR": dir, "PATH": bin} }
	if got := claudeConfigDir(); got != dir {
		t.Errorf("claude's folder %q, want the login shell's %q", got, dir)
	}
	if p, err := agentLookPath("opencode"); err != nil || p != filepath.Join(bin, "opencode") {
		t.Errorf("opencode found at %q (%v), want on the login PATH", p, err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", "/own")
	if got := claudeConfigDir(); got != "/own" {
		t.Errorf("this process's own value %q not taken first", got)
	}
}

// A conversation is gone only when where its agent keeps them is there to
// look in: not seeing that folder at all says nothing.
func TestConvThereUnknownWhereItCantLook(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	const conv = "51580c55-63f5-43c1-bbea-4d0425f09835"
	if there, known := convThere("qwen", conv, ""); there || known {
		t.Errorf("no ~/.qwen at all: there=%v known=%v, want unknown", there, known)
	}
	if err := os.MkdirAll(filepath.Join(home, ".qwen", "projects", "-work", "chats"), 0o700); err != nil {
		t.Fatal(err)
	}
	if there, known := convThere("qwen", conv, ""); there || !known {
		t.Errorf("its folder there, the file not: there=%v known=%v, want gone", there, known)
	}
}
