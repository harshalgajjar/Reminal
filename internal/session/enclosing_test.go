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

// The nearest session above a process is the one it runs in: with a session
// started inside another (this process's parent inside its grandparent),
// this process is in the inner one only; with none above, it is in none.
func TestNearestSessionIsTheInnerOne(t *testing.T) {
	self, parent := os.Getpid(), os.Getppid()
	grand := parentPID(parent)
	if grand <= 1 {
		t.Skip("no grandparent to stand in for an outer session")
	}
	if got := nearestOf(self, map[int]bool{parent: true, grand: true}); got != parent {
		t.Fatalf("nested: %d, want the inner session %d (outer %d)", got, parent, grand)
	}
	if got := nearestOf(self, map[int]bool{grand: true}); got != grand {
		t.Fatalf("one session above: %d, want %d", got, grand)
	}
	if got := nearestOf(self, map[int]bool{}); got != 0 {
		t.Fatalf("no session above: %d", got)
	}
	if got := nearestOf(self, map[int]bool{self: true, parent: true}); got != self {
		t.Fatalf("itself a session: %d", got)
	}
}
