// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"reminal/internal/session"
)

// A session whose agent set jobs to run later sleeps saying so — they won't
// fire, and a wake won't bring them back — but they are not its background
// work, nor a reason it can't sleep.
func TestSleepNamesScheduledJobsItWouldDrop(t *testing.T) {
	idle := sleepFacts{Headless: true, Saved: true, Fg: "claude", Conv: "conv-0123456789ab", Attn: "done", BgSaid: true, ConvThere: true, Turned: true}
	for n, want := range map[int]string{1: "its 1 scheduled job, which won't fire", 3: "its 3 scheduled jobs, which won't fire"} {
		f := idle
		f.Scheduled = n
		block, losses := sleepVerdict(f)
		if block != "" || !slices.Contains(losses, want) {
			t.Errorf("%d: block %q, losses %v; want %q", n, block, losses, want)
		}
		if slices.ContainsFunc(losses, func(l string) bool { return strings.Contains(l, "background work") }) {
			t.Errorf("%d: read as background work: %v", n, losses)
		}
	}
	if _, losses := sleepVerdict(idle); len(losses) != 0 {
		t.Errorf("none: %v", losses)
	}
}

// info --json and the directory row this machine answers with carry them,
// apart from the work running; a session with none carries nothing.
func TestInfoAndDirectoryCarryScheduled(t *testing.T) {
	at := time.Date(2026, 11, 6, 21, 40, 0, 0, time.UTC)
	next := at.Add(25 * time.Minute)
	a := &session.Active{ID: "SCHD2345", PIN: "123456", OpenURL: "http://r/", Attn: "done", Scheduled: &session.Scheduled{Count: 2, Next: next, At: at}}
	b, _ := json.Marshal(InfoJSON(a))
	var got struct {
		Scheduled *session.Scheduled `json:"scheduled"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if s := got.Scheduled; s == nil || s.Count != 2 || !s.Next.Equal(next) || !s.At.Equal(at) || strings.Contains(string(b), `"other"`) {
		t.Errorf("info: %s", b)
	}
	if b, _ := json.Marshal(InfoJSON(&session.Active{ID: "NONE2345", PIN: "1", OpenURL: "http://r/", Scheduled: &session.Scheduled{}})); strings.Contains(string(b), "scheduled") {
		t.Errorf("none: %s", b)
	}

	isolateHome(t)
	a.PID = os.Getpid()
	if err := session.WriteActive(*a); err != nil {
		t.Fatal(err)
	}
	rows := localDirSessions()
	if len(rows) != 1 || rows[0].Scheduled != 2 || rows[0].ScheduledNext != next.Unix() || rows[0].ScheduledAt != at.Unix() || rows[0].Other != nil {
		t.Fatalf("directory: %+v", rows)
	}
}
