// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package session

import (
	"os"
	"testing"
)

func TestParentPIDMatchesGetppid(t *testing.T) {
	if got, want := parentPID(os.Getpid()), os.Getppid(); got != want {
		t.Fatalf("parentPID(self) = %d, want %d", got, want)
	}
}

func TestAncestryTerminatesAndIsAcyclic(t *testing.T) {
	chain := selfAncestry(os.Getpid())
	if len(chain) == 0 || chain[0] != os.Getpid() {
		t.Fatalf("ancestry does not start at self: %v", chain)
	}
	if len(chain) > enclosingMaxDepth {
		t.Fatalf("ancestry walked past the depth cap: %d", len(chain))
	}
	seen := map[int]bool{}
	for _, pid := range chain {
		if seen[pid] {
			t.Fatalf("ancestry revisited pid %d — a cycle would hang the walk: %v", pid, chain)
		}
		seen[pid] = true
	}
}

func TestEnclosingFindsAnAncestorAgent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	// No agents recorded: a process that is not inside a session must say so
	// rather than guessing.
	if got := Enclosing(); got != "" {
		t.Fatalf("Enclosing with no agents = %q, want empty", got)
	}
	// Record THIS process's own parent as an agent. The walk should reach it,
	// which is the same shape as an MCP helper reaching the agent above it.
	if err := WriteActive(Active{ID: "ANCESTOR", PID: os.Getppid()}); err != nil {
		t.Fatal(err)
	}
	if got := Enclosing(); got != "ANCESTOR" {
		t.Fatalf("Enclosing = %q, want ANCESTOR", got)
	}
	// An agent that is not an ancestor must not match — otherwise a helper in
	// one session would answer for another session on the same machine.
	if err := ClearActive("ANCESTOR"); err != nil {
		t.Fatal(err)
	}
	if err := WriteActive(Active{ID: "STRANGER", PID: 999999}); err != nil {
		t.Fatal(err)
	}
	if got := Enclosing(); got != "" {
		t.Fatalf("Enclosing matched an unrelated agent: %q", got)
	}
}
