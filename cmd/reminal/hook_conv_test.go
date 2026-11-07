// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"os"
	"testing"

	"reminal/internal/session"
)

// The id is typed into a shell on restore: only an id-shaped value is taken.
func TestConvFromPayload(t *testing.T) {
	cases := map[string]string{
		`{"session_id":"3f2c9a10-7d4e-4b8a-9c11-0e5d6f7a8b9c","hook_event_name":"Stop"}`: "3f2c9a10-7d4e-4b8a-9c11-0e5d6f7a8b9c",
		`{"conversation_id":"chat_0123456789"}`:                                          "chat_0123456789",
		`{"conversationId":"agy_conversation_0042"}`:                                     "agy_conversation_0042",
		// Copilot CLI's sessionId beside the keys read before it: the one read
		// before keeps winning.
		`{"sessionId":"184da7fe-1615-4edb-b2ea-1d66a4c42d1c","conversation_id":"chat_0123456789"}`: "chat_0123456789",
		`{"session_id":"x; rm -rf ~"}`:  "",
		`{"session_id":"$(curl evil)"}`: "",
		`{"session_id":"short"}`:        "",
		`not json`:                      "",
		`{}`:                            "",
	}
	for in, want := range cases {
		if got := convFromPayload([]byte(in)); got != want {
			t.Errorf("%s: got %q, want %q", in, got, want)
		}
	}
}

// Claude Code's SessionStart says how its conversation began: one it went
// back to (resume, or compact: the same, compacted) is written already; one
// it begins (startup, clear) is not, until its first turn.
func TestASessionStartSaysWhetherItsConversationIsWritten(t *testing.T) {
	for src, want := range map[string]bool{"resume": true, "compact": true, "startup": false, "clear": false, "": false} {
		if got := sessionStartResumed([]byte(`{"session_id":"a1b2c3d4-e5f6","source":"` + src + `"}`)); got != want {
			t.Errorf("source %q: %v, want %v", src, got, want)
		}
	}
	if sessionStartResumed([]byte("not json")) {
		t.Error("an unreadable payload read as resumed")
	}
}

// A conversation a turn ran in is written whichever of its turn's hooks
// names it (its start, a question in it, its end, refused or not): a
// harness whose turn-start payload carries no id is still read right. An
// idle notification, which can come before any turn, never marks one.
func TestEveryHookOfATurnMarksItsConversationWritten(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("REMINAL_SESSION", "WRIT2345")
	old := convOwnSession
	convOwnSession = func(string) bool { return true }
	t.Cleanup(func() { convOwnSession = old })
	hook := func(arg, payload string) {
		t.Helper()
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.WriteString(payload)
		_ = w.Close()
		in := os.Stdin
		os.Stdin = r
		defer func() { os.Stdin = in }()
		if err := runHook([]string{arg}); err != nil {
			t.Fatal(err)
		}
	}
	convs := map[string]string{
		"working": "a1b2c3d4-0000-4000-8000-000000000001",
		"input":   "a1b2c3d4-0000-4000-8000-000000000002",
		"done":    "a1b2c3d4-0000-4000-8000-000000000003",
		"failed":  "a1b2c3d4-0000-4000-8000-000000000004",
		"notify":  "a1b2c3d4-0000-4000-8000-000000000005",
	}
	for arg, conv := range convs {
		hook(arg, `{"session_id":"`+conv+`","error":"rate_limit","message":"Claude is waiting for your input"}`)
		if got, want := session.ConvWritten("WRIT2345", conv), arg != "notify"; got != want {
			t.Errorf("%s: written %v, want %v", arg, got, want)
		}
	}
}
