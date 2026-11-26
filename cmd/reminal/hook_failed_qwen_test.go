// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reminal/internal/session"
)

// Qwen Code's StopFailure, in Stop's place when an error ended the turn:
// "done" at once, with the error it names kept beside it, for each of its
// codes (the payload is the one Qwen Code 0.26.9 wrote, captured in
// Docker). A plain Stop after it reads as done with no error.
func TestQwenTurnEndedByAnErrorIsReadAtOnce(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("REMINAL_SESSION", "QWEN2345")
	const head = `{"session_id":"579068ac-6416-4145-a98f-ba3552fed5c0","transcript_path":"/root/.qwen/projects/-/chats/579068ac-6416-4145-a98f-ba3552fed5c0.jsonl","cwd":"/","hook_event_name":"StopFailure","timestamp":"2026-11-26T11:51:28.422Z","permission_mode":"yolo","prompt_id":"579068ac-6416-4145-a98f-ba3552fed5c0########0",`
	for _, c := range []struct{ code, rest string }{
		{"billing_error", `"error_details":"402 Insufficient balance","last_assistant_message":"[API Error: 402 Insufficient balance]"`},
		{"rate_limit", `"error_details":"429 Rate limit exceeded","last_assistant_message":"[API Error: 429 Rate limit exceeded]"`},
		{"authentication_failed", `"error_details":"401 Unauthorized"`},
		{"loop_detected", `"error_details":"consecutive_identical_tool_calls"`},
		{"invalid_request", `"error_details":"400 Bad request"`},
		{"server_error", `"error_details":"503 Service unavailable"`},
		{"max_output_tokens", `"error_details":"max_tokens"`},
		{"unknown", `"error_details":"socket hang up"`},
	} {
		hookWithPayload(t, "working", `{"hook_event_name":"UserPromptSubmit"}`)
		if hs := session.ReadHookStateAnyAge("QWEN2345"); hs == nil || hs.State != "working" {
			t.Fatalf("%s: a turn begun reads %+v", c.code, hs)
		}
		hookWithPayload(t, "failed", head+`"error":"`+c.code+`",`+c.rest+`}`)
		hs := session.ReadHookStateAnyAge("QWEN2345")
		if hs == nil || hs.State != "done" || hs.Failure != c.code {
			t.Errorf("%s: after StopFailure the turn reads %+v, want done with that error", c.code, hs)
		}
	}
	// A plain Stop still reads as done, and names no error.
	hookWithPayload(t, "working", `{"hook_event_name":"UserPromptSubmit"}`)
	hookWithPayload(t, "done", `{"hook_event_name":"Stop","session_id":"579068ac-6416-4145-a98f-ba3552fed5c0"}`)
	if hs := session.ReadHookStateAnyAge("QWEN2345"); hs == nil || hs.State != "done" || hs.Failure != "" {
		t.Errorf("a plain Stop reads %+v, want done with no error", hs)
	}
}

// The StopFailure hook is written for a Qwen Code that has the event
// (0.14.4 on), with the state it maps to, and not for an older one or one
// whose version was not read. An install made before reminal knew the
// event is read as needing a setup again, and has it after that.
func TestQwenStopFailureIsWrittenForAQwenThatHasIt(t *testing.T) {
	exe := "/usr/local/bin/reminal"
	hooks := func(home string) map[string]any {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(home, ".qwen", "settings.json"))
		if err != nil {
			t.Fatal(err)
		}
		var root struct {
			Hooks map[string]any `json:"hooks"`
		}
		if err := json.Unmarshal(b, &root); err != nil {
			t.Fatal(err)
		}
		return root.Hooks
	}
	apply := func(home, version string, spec hookSpec) {
		t.Helper()
		spec.version = version
		if err := applyHooks(&spec, home, exe, false); err != nil {
			t.Fatal(err)
		}
	}
	now := *targetNamed(t, "qwen").hooks
	for version, want := range map[string]bool{"0.26.9": true, "0.14.4": true, "0.14.3": false, "0.9.0": false, "": false} {
		home := t.TempDir()
		apply(home, version, now)
		h := hooks(home)
		if _, got := h["StopFailure"]; got != want {
			t.Errorf("%q: StopFailure written %v, want %v", version, got, want)
		}
		for _, ev := range []string{"UserPromptSubmit", "Notification", "Stop", "SessionStart"} {
			if _, ok := h[ev]; !ok {
				t.Errorf("%q: %s not written", version, ev)
			}
		}
		if want {
			raw, _ := json.Marshal(h["StopFailure"])
			if !json.Valid(raw) || !strings.Contains(string(raw), "hook failed") {
				t.Errorf("%q: the StopFailure entry does not run `reminal hook failed`: %s", version, raw)
			}
		}
	}
	// An install from before: every event but this one.
	before := now
	before.events = nil
	for _, ev := range now.events {
		if ev.Event != "StopFailure" {
			before.events = append(before.events, ev)
		}
	}
	home := t.TempDir()
	apply(home, "0.26.9", before)
	if _, got := hooks(home)["StopFailure"]; got {
		t.Fatal("the install from before already has the event")
	}
	if !hookVersionMismatch(&now, home, "0.26.9") {
		t.Error("an install from before, with a Qwen Code that has the event, is not read as needing a setup again")
	}
	if hookVersionMismatch(&now, home, "0.14.3") {
		t.Error("an install from before, with a Qwen Code that lacks the event, is read as needing a setup again")
	}
	apply(home, "0.26.9", now)
	if _, got := hooks(home)["StopFailure"]; !got || hookVersionMismatch(&now, home, "0.26.9") {
		t.Error("a setup run again did not write the event")
	}
}
