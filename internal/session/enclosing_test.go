// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package session

import (
	"os"
	"testing"
	"time"
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
	if got := nearestOf(self, map[int]int{parent: parent, grand: grand}); got != parent {
		t.Fatalf("nested: %d, want the inner session %d (outer %d)", got, parent, grand)
	}
	if got := nearestOf(self, map[int]int{grand: grand}); got != grand {
		t.Fatalf("one session above: %d, want %d", got, grand)
	}
	if got := nearestOf(self, map[int]int{}); got != 0 {
		t.Fatalf("no session above: %d", got)
	}
	if got := nearestOf(self, map[int]int{self: self, parent: parent}); got != self {
		t.Fatalf("itself a session: %d", got)
	}
}

// A session is found by its shell where the process serving it is not above
// the shell: on Windows after a hot restart, the shell hangs under a holder
// the session's first process started, and the process now serving it is a
// new one. What runs under the shell was in no session then, and was turned
// away from its own session's browser.
func TestASessionIsFoundByItsShellWhenItsProcessIsNotAbove(t *testing.T) {
	isolateHome(t)
	self, shell := os.Getpid(), os.Getppid() // this process runs "in the shell"
	server := deadPID(t) + 100000            // serving the session, above nothing here
	is := ProcessIdentity(shell)
	if is == "" {
		t.Skip("no way to tell which process a pid is on this system")
	}
	all := []Active{{ID: "RESTARTD", PID: server, ShellPID: shell, ShellIdent: is}}
	if got := nearestOf(self, sessionPIDs(all)); got != server {
		t.Fatalf("under the session's shell: %d, want the process serving it (%d)", got, server)
	}
	if got := nearestOf(self, sessionPIDs([]Active{{ID: "RESTARTD", PID: server}})); got != 0 {
		t.Fatalf("no shell recorded and its process not above: %d, want none", got)
	}
	// The shell ended and its pid went to another process: the record still
	// names the pid, but the process there is not the one recorded, and
	// what runs under it is in no session. Nor is a record that does not
	// say which process its shell was.
	for _, stale := range []string{"t1", "0:1", ""} {
		reused := []Active{{ID: "RESTARTD", PID: server, ShellPID: shell, ShellIdent: stale}}
		if got := nearestOf(self, sessionPIDs(reused)); got != 0 {
			t.Fatalf("a shell pid now another process's (recorded as %q): %d, want none", stale, got)
		}
	}
	// A session started inside that shell is the nearer one, by its own
	// process or by its own shell.
	inner := []Active{{ID: "RESTARTD", PID: server, ShellPID: parentPID(shell), ShellIdent: ProcessIdentity(parentPID(shell))}, {ID: "INNER123", PID: shell}}
	if g := parentPID(shell); g > 1 {
		if got := nearestOf(self, sessionPIDs(inner)); got != shell {
			t.Fatalf("a session inside the shell: %d, want the inner one (%d)", got, shell)
		}
	}
	// A pid that serves a session is never taken as another's shell.
	both := sessionPIDs([]Active{{ID: "ONE12345", PID: shell}, {ID: "TWO12345", PID: server, ShellPID: shell, ShellIdent: is}})
	if both[shell] != shell {
		t.Errorf("a session's own process was taken as another session's shell: %v", both)
	}

	// Enclosing reads the same from the records.
	if err := WriteActive(Active{ID: "RESTARTD", PID: self, ShellPID: shell, ShellIdent: is, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if got := NearestSessionPID(shell); got != self {
		t.Errorf("from the recorded shell: session process %d, want %d", got, self)
	}
}
