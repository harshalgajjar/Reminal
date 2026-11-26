// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !windows

package client

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// What reminal's start says it added is kept for the person and said once,
// at a shell's prompt: not while an agent is in front, and not twice.
func TestWhatTheStartAddedIsSaidAtTheShellsPrompt(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "reminal")
	body := "#!/bin/sh\n[ \"$1 $2\" = \"integrate --heal-plugins\" ] && echo 'reminal added its StopFailure hook for Qwen Code: it takes effect the next time Qwen Code starts'\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	oldSelf := healSelf
	t.Cleanup(func() { healSelf = oldSelf; healPluginsOnce = sync.Once{} })
	healSelf = func() (string, bool) { return script, true }
	healPluginsOnce = sync.Once{}
	healSaid.Lock()
	healSaid.lines = nil
	healSaid.Unlock()
	HealOwnPlugins()
	kept := func() int { healSaid.Lock(); defer healSaid.Unlock(); return len(healSaid.lines) }
	for i := 0; i < 300 && kept() == 0; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if kept() != 1 {
		t.Fatalf("%d line(s) kept of what the start said, want 1", kept())
	}
	if got := takeHealed("claude"); len(got) != 0 {
		t.Errorf("said over an agent's screen: %q", got)
	}
	got := takeHealed("zsh")
	if len(got) != 1 || got[0] != "reminal added its StopFailure hook for Qwen Code: it takes effect the next time Qwen Code starts" {
		t.Errorf("at the shell's prompt: %q", got)
	}
	if again := takeHealed("zsh"); len(again) != 0 {
		t.Errorf("said twice: %q", again)
	}
}

// A lock taken by one is waited for by the next, which then runs.
func TestWithFileLockRunsOneAtATime(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	in, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go WithFileLock("test-heal.lock", func() { close(in); <-release })
	<-in
	ran := false
	go func() { WithFileLock("test-heal.lock", func() { ran = true }); close(done) }()
	select {
	case <-done:
		t.Fatal("the second ran while the first held the lock")
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the second never ran after the first let go")
	}
	if !ran {
		t.Error("the second did not run")
	}
}

// Qwen Code answers --version with the version alone; Claude Code names
// itself. Another program's answer decides nothing.
func TestAnAgentsVersionAnswerIsItsOwn(t *testing.T) {
	for _, c := range []struct {
		fg, v string
		want  bool
	}{
		{"qwen", "0.26.9", true},
		{"qwen", "0.27.0-nightly.20261120", true},
		{"qwen", "v22.11.0 node", false},
		{"qwen", "Usage: qwen [options]", false},
		{"claude", "2.1.332 (Claude Code)", true},
		{"claude", "2.1.332", false},
	} {
		if got := agentVersionAnswer(c.fg, c.v); got != c.want {
			t.Errorf("%s answering %q: taken %v, want %v", c.fg, c.v, got, c.want)
		}
	}
}
