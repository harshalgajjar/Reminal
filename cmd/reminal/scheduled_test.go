// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"reminal/internal/client"
	"reminal/internal/protocol"
	"reminal/internal/session"
)

func scheduledPayload(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "scheduled", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// What a harness has set to run later, from its Stop as it really sends it
// (testdata/scheduled: Claude Code 2.1.319's session_crons and Qwen Code
// 0.26.9's crons, two jobs each, recorded in Docker): counted apart from the
// work running, said at the time it was read, with the soonest next run
// when the harness gives one.
func TestAHookReadsWhatIsSetToRunLater(t *testing.T) {
	now := time.Date(2026, 11, 6, 21, 40, 0, 0, time.UTC)
	for _, name := range []string{"claude-stop", "qwen-stop"} {
		s := hookScheduled([]byte(scheduledPayload(t, name)), now)
		if s == nil || s.Count != 2 || !s.At.Equal(now) || !s.Next.IsZero() {
			t.Errorf("%s: %+v, want 2 said at %v, no next run", name, s, now)
		}
		bg := hookBackground([]byte(scheduledPayload(t, name)), "")
		if bg == nil || bg.Scheduled == nil || bg.Scheduled.Count != 2 || bg.Shells+bg.Agents+session.CountOther(bg.Other) != 0 {
			t.Errorf("%s: background %+v, want nothing running and 2 scheduled", name, bg)
		}
	}
	next := `{"crons":[{"id":"a","schedule":"*/5 * * * *","next_run":"2026-11-06T22:00:00.000Z","enabled":true},` +
		`{"id":"b","schedule":"0 15 * * *","next_run":"2026-11-06T21:45:00.000Z","enabled":true},` +
		`{"id":"c","schedule":"0 9 * * *","next_run":"2026-11-06T21:41:00.000Z","enabled":false}]}`
	if s := hookScheduled([]byte(next), now); s == nil || s.Count != 2 || !s.Next.Equal(time.Date(2026, 11, 6, 21, 45, 0, 0, time.UTC)) {
		t.Errorf("next runs: %+v, want 2 (one not enabled), the soonest 21:45", s)
	}
	for _, none := range []string{`{"session_crons":[]}`, `{"crons":[]}`, `{"background_tasks":[]}`, `not json`} {
		if s := hookScheduled([]byte(none), now); s != nil {
			t.Errorf("%s: %+v, want none", none, s)
		}
	}
}

// Through `reminal hook`: a Stop that names jobs leaves them with its done;
// a later Stop naming none clears them.
func TestScheduledJobsStayWithTheTurnThatSetThem(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("REMINAL_SESSION", "SCHD2345")
	hookWithPayload(t, "done", scheduledPayload(t, "claude-stop"))
	hs := session.ReadHookStateAnyAge("SCHD2345")
	if hs == nil || hs.State != "done" || hs.Background == nil || hs.Background.Scheduled == nil || hs.Background.Scheduled.Count != 2 {
		t.Fatalf("after Stop: %+v", hs)
	}
	hookWithPayload(t, "done", `{"session_id":"x","hook_event_name":"Stop","background_tasks":[],"session_crons":[]}`)
	if hs := session.ReadHookStateAnyAge("SCHD2345"); hs == nil || hs.Background == nil || hs.Background.Scheduled != nil {
		t.Fatalf("after a Stop with none: %+v", hs)
	}
}

// `reminal list` says them last, never in the running count: "done · 1
// scheduled" is a finished turn with a job to come.
func TestListSaysScheduledApart(t *testing.T) {
	for _, c := range []struct {
		a     session.Active
		parts []string
		n     int
	}{
		{session.Active{Scheduled: &session.Scheduled{Count: 1}}, []string{"1 scheduled"}, 0},
		{session.Active{Shells: 1, Scheduled: &session.Scheduled{Count: 3}}, []string{"1 shell", "3 scheduled"}, 1},
		{session.Active{Scheduled: &session.Scheduled{Count: 0}}, nil, 0},
		{session.Active{Scheduled: &session.Scheduled{Count: 5_000_000}}, []string{"1000000 scheduled"}, 0},
	} {
		parts, n := listBackground(c.a)
		if !slices.Equal(parts, c.parts) || n != c.n {
			t.Errorf("%+v: %v, %d; want %v, %d", c.a.Scheduled, parts, n, c.parts, c.n)
		}
	}
}

// list_sessions carries them apart from the work running, a remote count
// held to the bounds every count is, the soonest next run as RFC3339.
func TestFleetMachineRowCarriesScheduled(t *testing.T) {
	next := time.Date(2026, 11, 7, 15, 7, 0, 0, time.UTC)
	m := client.FleetMachine{ID: "m", Sessions: []protocol.DirSession{
		{ID: "SCHD2345", Attn: "done", Scheduled: 2, ScheduledNext: next.Unix()},
		{ID: "HUGE2345", Scheduled: 9_000_000},
		{ID: "NONE2345"},
	}}
	rows := fleetMachineRow(m, "").Sessions
	if r := rows[0]; r.Scheduled != 2 || r.ScheduledNext != next.Local().Format(time.RFC3339) || r.Shells+r.Agents != 0 || r.Other != nil {
		t.Errorf("row: %+v", r)
	}
	if rows[1].Scheduled != 1_000_000 {
		t.Errorf("a remote count past the bound: %d", rows[1].Scheduled)
	}
	if rows[2].Scheduled != 0 || rows[2].ScheduledNext != "" {
		t.Errorf("none: %+v", rows[2])
	}
}

// A SubagentStop says what runs now and nothing of the jobs set to run later
// (Claude Code's carries no session_crons): what the Stop said of them
// stands beside the new count. One that names an empty list clears them.
func TestASubagentFinishLeavesTheScheduledJobs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("REMINAL_SESSION", "SCHD3456")
	hookWithPayload(t, "done", scheduledPayload(t, "claude-stop"))
	hookWithPayload(t, "subagent", `{"session_id":"x","hook_event_name":"SubagentStop","background_tasks":[{"id":"s1","type":"shell","status":"running"}]}`)
	hs := session.ReadHookStateAnyAge("SCHD3456")
	if hs == nil || hs.State != "done" || hs.Background == nil || hs.Background.Shells != 1 || hs.Background.Scheduled == nil || hs.Background.Scheduled.Count != 2 {
		t.Fatalf("after a SubagentStop with no word of them: %+v", hs)
	}
	hookWithPayload(t, "subagent", `{"session_id":"x","hook_event_name":"SubagentStop","background_tasks":[],"session_crons":[]}`)
	if hs := session.ReadHookStateAnyAge("SCHD3456"); hs == nil || hs.Background == nil || hs.Background.Scheduled != nil {
		t.Fatalf("after a SubagentStop naming none: %+v", hs)
	}
}
