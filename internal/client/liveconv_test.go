// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"reminal/internal/session"
)

func writeClaudeSession(t *testing.T, dir string, pid int, body string, at time.Time) {
	t.Helper()
	p := filepath.Join(dir, "sessions", strconv.Itoa(pid)+".json")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
}

// Claude Code's own record of a process says which conversation it is in,
// whether or not a hook has fired. It is believed only for that pid, and
// only with an id that is safe to type.
func TestClaudeLiveConv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	now := time.Now()
	writeClaudeSession(t, dir, 4242, `{"pid":4242,"sessionId":"820b88ea-9c71-4351-91d6-5a0fbb3c6f90","cwd":"/w"}`, now)
	writeClaudeSession(t, dir, 4243, `{"pid":9999,"sessionId":"820b88ea-9c71-4351-91d6-5a0fbb3c6f90"}`, now)
	writeClaudeSession(t, dir, 4244, `{"pid":4244,"sessionId":"x; rm -rf ~"}`, now)
	writeClaudeSession(t, dir, 4245, `not json`, now)
	for pid, want := range map[int]string{4242: "820b88ea-9c71-4351-91d6-5a0fbb3c6f90", 4243: "", 4244: "", 4245: "", 4246: "", 0: ""} {
		if got, _ := claudeLiveConv(pid); got != want {
			t.Errorf("pid %d: got %q, want %q", pid, got, want)
		}
	}
}

// The newer of the hook's report and the agent's own record wins: the hook
// may be days old (nothing sent since a restart), or newer than a record the
// agent has not rewritten yet.
func TestAgentConvNewerWins(t *testing.T) {
	isolateHome(t)
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	const id = "AGCONV01"
	old, recent := time.Now().Add(-48*time.Hour), time.Now()

	// No hook report at all: the agent's record alone.
	writeClaudeSession(t, dir, 501, `{"pid":501,"sessionId":"live-conversation-1"}`, old)
	if got := agentConv("claude", 501, id); got != "live-conversation-1" {
		t.Fatalf("no hook: got %q", got)
	}

	// A hook report older than the agent's record: the record.
	if err := session.WriteConv(id, "hook-conversation-1"); err != nil {
		t.Fatal(err)
	}
	p := convFile(t, id)
	_ = os.Chtimes(p, old.Add(-time.Hour), old.Add(-time.Hour))
	if got := agentConv("claude", 501, id); got != "live-conversation-1" {
		t.Fatalf("stale hook: got %q", got)
	}

	// A hook report newer than the record: the hook.
	_ = os.Chtimes(p, recent, recent)
	if got := agentConv("claude", 501, id); got != "hook-conversation-1" {
		t.Fatalf("fresh hook: got %q", got)
	}

	// An agent with no record of its own: the hook, as before.
	if got := agentConv("codex", 501, id); got != "hook-conversation-1" {
		t.Fatalf("codex: got %q", got)
	}
}

func convFile(t *testing.T, id string) string {
	t.Helper()
	home, _ := os.UserHomeDir()
	var found string
	_ = filepath.Walk(filepath.Join(home, ".reminal"), func(p string, fi os.FileInfo, err error) error {
		if err == nil && fi.Name() == id+".conv" {
			found = p
		}
		return nil
	})
	if found == "" {
		t.Fatalf("no .conv file for %s", id)
	}
	return found
}

// One conversation is resumed in one session. Two records naming the same
// one came from an agent started with "continue the latest here" picking up
// another session's; the session that had it first keeps it, and the other
// opens the list, with a line saying why.
func TestResumePlanOneConversationOnce(t *testing.T) {
	useHelp(t)
	t0 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	const conv = "6d30e149-14d3-407d-b6a0-5e5a98c1e18c"
	first := session.Restore{ID: "CRVD779G", Fg: "claude", FgArgs: []string{"claude"}, Cwd: "/w", Conv: conv, ConvSince: t0}
	later := session.Restore{ID: "6NAAEXUY", Fg: "claude", FgArgs: []string{"claude"}, Cwd: "/w", Conv: conv, ConvSince: t0.Add(time.Hour)}
	other := session.Restore{ID: "LMQQEE74", Fg: "claude", FgArgs: []string{"claude"}, Cwd: "/w", Conv: "820b88ea-9c71-4351-91d6-5a0fbb3c6f90", ConvSince: t0}
	all := []session.Restore{later, other, first}

	byID := 0
	for _, x := range all {
		argv, _ := resumePlan(x, all)
		if reflect.DeepEqual(argv, []string{"claude", "--resume", conv}) {
			byID++
		}
	}
	if byID != 1 {
		t.Fatalf("%s resumed in %d sessions, want 1", conv, byID)
	}
	if argv, note := resumePlan(first, all); !reflect.DeepEqual(argv, []string{"claude", "--resume", conv}) || note != "" {
		t.Errorf("first holder: got %q %q", argv, note)
	}
	if argv, note := resumePlan(later, all); !reflect.DeepEqual(argv, []string{"claude", "--resume"}) || note == "" {
		t.Errorf("later holder: got %q %q, want the list and a note", argv, note)
	}
	if argv, _ := resumePlan(other, all); !reflect.DeepEqual(argv, []string{"claude", "--resume", other.Conv}) {
		t.Errorf("its own conversation: got %q", argv)
	}

	// Records from before ConvSince: one that says beats one that does not;
	// two that do not are settled by id, the same way from either side.
	noTime := later
	noTime.ConvSince = time.Time{}
	if ownsConv(noTime, []session.Restore{noTime, first}) || !ownsConv(first, []session.Restore{noTime, first}) {
		t.Error("a record with a time should own the conversation over one without")
	}
	a, b := first, later
	a.ConvSince, b.ConvSince = time.Time{}, time.Time{}
	if ownsConv(a, []session.Restore{a, b}) == ownsConv(b, []session.Restore{a, b}) {
		t.Error("exactly one of two undated records should own the conversation")
	}
	// A different agent with the same id string is not a clash.
	c := first
	c.ID, c.Fg = "ZZZZZZZZ", "codex"
	if !ownsConv(later, []session.Restore{later, c}) {
		t.Error("a codex record should not take a claude conversation")
	}
}
