// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"reminal/internal/session"
)

// otherProcess is a live process that is not this one, for a record to name.
func otherProcess(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Skip("no sleep to stand in for another agent:", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd.Process.Pid
}

func restorable(t *testing.T) []string {
	t.Helper()
	rs, err := Restorable()
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range rs {
		ids = append(ids, r.ID)
	}
	sort.Strings(ids)
	return ids
}

func saveRecord(t *testing.T, id string) {
	t.Helper()
	if err := session.WriteRestore(session.Restore{ID: id, PIN: "123456", Cwd: t.TempDir(), SavedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

// A restored agent's REMINAL_RESTORE named its own session, and its shell —
// with everything run there — inherited it: a `reminal new` in a restored
// session started a second agent for THAT session (a second shell, the same
// conversation resumed twice, the two taking the session's record and seat
// from each other). The id is taken out as it is read, and never handed on.
func TestARestoredSessionDoesNotHandItsIDOn(t *testing.T) {
	t.Setenv(envRestore, "9FT4RXH2")
	t.Setenv(envNewName, "Innovator 2")
	if id := RestoreEnvID(); id != "9FT4RXH2" {
		t.Fatalf("read %q", id)
	}
	if v, ok := os.LookupEnv(envRestore); ok {
		t.Fatalf("still in the environment after it was read, for the shell to inherit: %q", v)
	}
	t.Setenv(envRestore, "9FT4RXH2") // a shell an older agent started
	for _, kv := range spawnEnv("X=1") {
		if strings.HasPrefix(kv, envRestore+"=") || strings.HasPrefix(kv, envNewName+"=") {
			t.Fatalf("a spawned session would inherit %s", kv)
		}
	}
	env := spawnEnv(envRestore + "=OTHER")
	if env[len(env)-1] != envRestore+"=OTHER" {
		t.Fatal("what the spawn itself sets was dropped")
	}
}

// A session whose agent is alive is never restored — nor one whose record
// cannot be read: not knowing is not gone.
func TestOnlyASessionThatIsGoneIsRestorable(t *testing.T) {
	isolateHome(t)
	for _, id := range []string{"LIVE0001", "GONE0001", "BAD00001", "DEAD0001", "CUT00001"} {
		saveRecord(t, id)
	}
	if err := session.WriteActive(session.Active{ID: "LIVE0001", PID: otherProcess(t), StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	dead := exec.Command("true")
	_ = dead.Run()
	if err := session.WriteActive(session.Active{ID: "DEAD0001", PID: dead.Process.Pid, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	if err := os.WriteFile(filepath.Join(home, ".reminal", "active-BAD00001.json"), []byte(`{"id":"BAD0`), 0o600); err != nil {
		t.Fatal(err)
	}
	// Cut short by a power cut: as unreadable, but from before this boot —
	// no process of this boot can be running it, and it must come back.
	cut := filepath.Join(home, ".reminal", "active-CUT00001.json")
	if err := os.WriteFile(cut, []byte(`{"id":"CUT0`), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(cut, old, old); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(restorable(t), " "); got != "CUT00001 DEAD0001 GONE0001" {
		t.Fatalf("restorable: %q, want the three that are gone", got)
	}
}

// Another process holding a session's id — an agent serving it — keeps it
// from being restored, and the restored agent itself refuses to start.
func TestASessionHeldElsewhereIsNotRestoredTwice(t *testing.T) {
	isolateHome(t)
	saveRecord(t, "HELD0001")
	p, err := lockFilePath(liveLockName("HELD0001"))
	if err != nil {
		t.Fatal(err)
	}
	// Another process's claim: taken outside this process's own table.
	f, ok, err := tryLockFile(liveLockName("HELD0001"))
	if err != nil || !ok {
		t.Fatalf("lock %s: %v %v", p, ok, err)
	}
	if got := restorable(t); len(got) != 0 {
		t.Fatalf("a session held by a running agent is restorable: %v", got)
	}
	if _, _, err := LoadRestoreState("HELD0001"); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("restoring a session held by a running agent: %v", err)
	}
	unlockFile(f)
	if got := restorable(t); len(got) != 1 {
		t.Fatalf("let go, the session is restorable again: %v", got)
	}
	if _, _, err := LoadRestoreState("HELD0001"); err != nil {
		t.Fatalf("restoring a session nothing holds: %v", err)
	}
	// This process now holds it: a second restorer cannot take it.
	if err := claimForRestore("HELD0001"); err != nil {
		t.Fatalf("the holder itself was refused: %v", err)
	}
	g, ok, _ := tryLockFile(liveLockName("HELD0001"))
	if ok {
		unlockFile(g)
		t.Fatal("a second restorer took the id while it was held")
	}
	releaseLive("HELD0001")
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("the lock file was left behind when the session ended")
	}
}

// An agent ending takes away its own record only: another agent running
// under the same id keeps its record, and the session keeps coming back.
func TestAnEndingAgentLeavesAnotherAgentsRecord(t *testing.T) {
	isolateHome(t)
	other := otherProcess(t)
	if err := session.WriteActive(session.Active{ID: "TWIN0001", PID: other, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := session.ClearActiveIfOwn("TWIN0001", os.Getpid()); err != nil {
		t.Fatal(err)
	}
	if session.ActiveState("TWIN0001") != session.Running {
		t.Fatal("an ending agent removed another live agent's record")
	}
	saveRecord(t, "TWIN0001")
	a := &Agent{sessionID: "TWIN0001"}
	a.settleRestore()
	if _, err := session.ReadRestore("TWIN0001"); err != nil {
		t.Fatal("an ending agent forgot a session another agent still serves")
	}
	if err := session.ClearActiveIfOwn("TWIN0001", other); err != nil {
		t.Fatal(err)
	}
	if session.ActiveState("TWIN0001") != session.Gone {
		t.Fatal("an agent's own record was kept")
	}
	a.settleRestore()
	if _, err := session.ReadRestore("TWIN0001"); err == nil {
		t.Fatal("a session ended on purpose, with no other agent, was kept to come back")
	}
}

// A restored session starts with nothing its dead harness reported: a
// "working" from a turn the restart cut short made the restored harness's
// first idle ping read as "needs you" (hook.go: classifyNotify).
func TestARestoredSessionForgetsWhatItsDeadHarnessSaid(t *testing.T) {
	isolateHome(t)
	if err := session.WriteHookState("CUT00001", "working"); err != nil {
		t.Fatal(err)
	}
	a := &Agent{sessionID: "CUT00001", restoring: true}
	a.forgetDeadHarness()
	if hs := session.ReadHookStateAnyAge("CUT00001"); hs != nil {
		t.Fatalf("a restored session kept its dead harness's %q", hs.State)
	}
	if err := session.WriteHookState("HOT00001", "working"); err != nil {
		t.Fatal(err)
	}
	(&Agent{sessionID: "HOT00001"}).forgetDeadHarness()
	if hs := session.ReadHookStateAnyAge("HOT00001"); hs == nil {
		t.Fatal("a session that is not being restored lost what its running harness said")
	}
}
