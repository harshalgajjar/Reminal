// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"testing"

	"reminal/internal/session"
)

// A notification says nothing of what runs in the background: what the
// turn's end said stands through it. Qwen Code sends an idle Notification
// right after its Stop, Claude Code one a minute after; either wiped the
// count the Stop had just given, and a monitor left running read as gone.
func TestANotificationKeepsWhatTheTurnsEndSaid(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("REMINAL_SESSION", "NOTE2345")
	hookWithPayload(t, "done", `{"hook_event_name":"Stop","background_tasks":[{"id":"m1","type":"monitor","status":"running"},{"id":"s1","type":"shell","status":"running"}]}`)
	for _, note := range []string{
		`{"hook_event_name":"Notification","notification_type":"idle_prompt","message":"Qwen Code is waiting for your input"}`,
		`{"hook_event_name":"Notification","message":"Claude is waiting for your input"}`,
	} {
		hookWithPayload(t, "notify", note)
		hs := session.ReadHookStateAnyAge("NOTE2345")
		if hs == nil || hs.State != "done" || hs.Background == nil || hs.Background.Shells != 1 || hs.Background.Other["monitor"] != 1 {
			t.Fatalf("after %s: %+v", note, hs)
		}
	}
	// A turn begins: nothing of the last one's stands, and a notification
	// then keeps that.
	hookWithPayload(t, "working", `{"hook_event_name":"UserPromptSubmit"}`)
	hookWithPayload(t, "notify", `{"hook_event_name":"Notification","message":"Claude needs your permission to use Bash"}`)
	if hs := session.ReadHookStateAnyAge("NOTE2345"); hs == nil || hs.State != "input" || hs.Background != nil {
		t.Fatalf("a permission asked mid-turn: %+v", hs)
	}
}
