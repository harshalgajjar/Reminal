// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import "testing"

func TestClassifyNotify(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    string
	}{
		// Idle-timeout notifications are "done" (finished, sitting at the prompt),
		// not "needs you" — otherwise every quiet session turns yellow after a
		// minute. This is the ONLY notification that fires under bypass-permissions.
		{"claude idle", `{"message":"Claude is waiting for your input"}`, "done"},
		{"idle variant", `{"message":"Agent is idle, waiting for input"}`, "done"},
		// A real permission/approval prompt genuinely needs the user.
		{"claude permission", `{"message":"Claude needs your permission to use Bash"}`, "input"},
		// Unknown / unparseable payloads default to "input" so a genuine
		// attention request is never silently dropped.
		{"empty object", `{}`, "input"},
		{"not json", `something happened`, "input"},
		{"empty", ``, "input"},
		// Match on the raw body when there is no message field.
		{"raw idle text", `notification: waiting for your input now`, "done"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyNotify([]byte(c.payload)); got != c.want {
				t.Errorf("classifyNotify(%q) = %q, want %q", c.payload, got, c.want)
			}
		})
	}
}
