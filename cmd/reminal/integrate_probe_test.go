// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build linux || darwin

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"reminal/internal/session"
)

// integrate registers itself through an agent's own CLI (`<agent> mcp add`)
// as a probe: no REMINAL_ variable, the probe's mark — an agent that does
// not know the subcommand starts up instead, and its hooks are no session's.
func TestAnAgentsCLIIsRunAsAProbe(t *testing.T) {
	t.Setenv("REMINAL_SESSION", "AAAA2345")
	dir := t.TempDir()
	out := filepath.Join(dir, "env")
	agent := filepath.Join(dir, "fakeagent")
	if err := os.WriteFile(agent, []byte("#!/bin/sh\nenv > '"+out+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	step := planStep{target: agentTarget{Name: "fake", cliAdd: []string{"mcp", "add", "reminal", "%CMD%"}}, binPath: agent}
	if err := applyViaCLI(step, "/bin/reminal", false); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("the agent's CLI was not run: %v", err)
	}
	env := "\n" + string(b)
	if !strings.Contains(env, "\n"+session.HookProbeEnv+"=1\n") {
		t.Error("run without the probe's mark")
	}
	if strings.Contains(env, "\nREMINAL_SESSION=") {
		t.Error("run with the session's REMINAL_SESSION")
	}
}

// An agent's CLI that never answers (it waits on a sign-in, a question, a
// broken install) is given up on after cliAddWait and said, by name:
// integrate used to wait on it for good, and set up no agent after it.
func TestAnAgentsCLIThatDoesNotAnswerIsGivenUpOn(t *testing.T) {
	old := cliAddWait
	cliAddWait = 300 * time.Millisecond
	t.Cleanup(func() { cliAddWait = old })
	dir := t.TempDir()
	agent := filepath.Join(dir, "slowagent")
	if err := os.WriteFile(agent, []byte("#!/bin/sh\necho started\nsleep 600\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	step := planStep{target: agentTarget{Name: "Slow", Bin: "slowagent", cliAdd: []string{"mcp", "add", "reminal", "%CMD%"}}, binPath: agent}
	start := time.Now()
	err := applyViaCLI(step, "/bin/reminal", false)
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("waited %v on a CLI that does not answer", took)
	}
	if err == nil || err.Error() != "slowagent did not answer" {
		t.Fatalf("a CLI that does not answer: %v, want it said by name", err)
	}
}
