// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"testing"

	"github.com/reminal/reminal/internal/session"
)

func TestClassifyNotify(t *testing.T) {
	// Isolate hook-state I/O to a temp HOME so the test never touches ~/.reminal
	// (session.WriteHookState/ReadHookState resolve under os.UserHomeDir()).
	t.Setenv("HOME", t.TempDir())
	const id = "TESTSESS"

	set := func(state string) {
		if state == "" {
			_ = session.ClearHookState(id)
			return
		}
		if err := session.WriteHookState(id, state); err != nil {
			t.Fatalf("seed hook state: %v", err)
		}
	}

	cases := []struct {
		name    string
		cur     string // current turn state to seed first
		payload string
		want    string
	}{
		// A real permission/approval request always needs you, regardless of turn.
		{"permission while done", "done", `{"message":"Claude needs your permission to use Bash"}`, "input"},
		{"permission no state", "", `{"message":"needs your approval"}`, "input"},

		// An idle "waiting for your input" ping is split by the turn state.
		{"idle after done", "done", `{"message":"Claude is waiting for your input"}`, "done"},
		{"idle no state", "", `{"message":"Claude is waiting for your input"}`, "done"},
		{"idle mid-turn (working)", "working", `{"message":"Claude is waiting for your input"}`, "input"},
		{"idle while already input", "input", `{"message":"Claude is waiting for your input"}`, "input"},

		// Unknown / unparseable payloads follow the same turn-state rule.
		{"empty after done", "done", `{}`, "done"},
		{"empty mid-turn", "working", ``, "input"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			set(c.cur)
			if got := classifyNotify([]byte(c.payload), id); got != c.want {
				t.Errorf("classifyNotify(%q, cur=%q) = %q, want %q", c.payload, c.cur, got, c.want)
			}
		})
	}
}

func TestQuoteExeFor(t *testing.T) {
	cases := []struct{ exe, goos, want string }{
		{`/usr/local/bin/reminal`, "darwin", `/usr/local/bin/reminal`},
		{`/Applications/reminal.app/Contents/MacOS/reminal`, "darwin", `/Applications/reminal.app/Contents/MacOS/reminal`}, // no space
		{`/Users/a b/reminal`, "darwin", `'/Users/a b/reminal'`},
		{`C:\Users\harshal\reminal.exe`, "windows", `C:\Users\harshal\reminal.exe`},
		{`C:\Program Files\reminal\reminal.exe`, "windows", `"C:\Program Files\reminal\reminal.exe"`},
		{`C:\Users\John Doe\reminal.exe`, "windows", `"C:\Users\John Doe\reminal.exe"`},
	}
	for _, c := range cases {
		if got := quoteExeFor(c.exe, c.goos); got != c.want {
			t.Errorf("quoteExeFor(%q, %q) = %q, want %q", c.exe, c.goos, got, c.want)
		}
	}
}
