// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"strings"
	"testing"
	"time"
)

// A session restarted in place is the same run of it: its start time comes
// through whole, so what is named by it (browserRun) keeps its name from the
// first restart on. It came through in seconds, and the name changed once.
func TestARestartKeepsTheStartTimeWhole(t *testing.T) {
	started := time.Unix(1795000000, 123456789)
	env := map[string]string{}
	for _, kv := range resumeStartedAtEnv(started) {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	get := func(k string) string { return env[k] }
	got := resumeStartedAt(get)
	if !got.Equal(started) {
		t.Fatalf("handed on %s, read back %s", started.Format(time.RFC3339Nano), got.Format(time.RFC3339Nano))
	}
	before := (&Agent{startedAt: started}).browserRun()
	if after := (&Agent{startedAt: got}).browserRun(); after != before {
		t.Fatalf("the run was %s and is %s after a restart", before, after)
	}
	// Again and again: the same.
	for _, kv := range resumeStartedAtEnv(got) {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	if again := resumeStartedAt(get); !again.Equal(started) {
		t.Fatalf("a second restart read %s", again.Format(time.RFC3339Nano))
	}
	// From a reminal of before: the seconds alone, as they were.
	old := map[string]string{envResumeStartedAt: "1795000000"}
	if got := resumeStartedAt(func(k string) string { return old[k] }); !got.Equal(time.Unix(1795000000, 0)) {
		t.Fatalf("seconds alone read as %s", got)
	}
	// The two disagreeing (a stale one left in the environment): the seconds.
	odd := map[string]string{envResumeStartedAt: "1795000000", envResumeStartedAtNanos: "1700000000123456789"}
	if got := resumeStartedAt(func(k string) string { return odd[k] }); !got.Equal(time.Unix(1795000000, 0)) {
		t.Fatalf("disagreeing values read as %s", got)
	}
	// Neither: now.
	if got := resumeStartedAt(func(string) string { return "" }); time.Since(got) > time.Minute {
		t.Fatalf("nothing handed on read as %s", got)
	}
}
