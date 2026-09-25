// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"reminal/internal/session"
	"strings"
	"testing"
	"time"
)

// A report is kept on this machine, listed oldest first, exported as text
// with what the agent said and where it was — and nothing goes anywhere.
func TestAReportIsKeptHereAndExported(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if issues, _, err := ListIssues(); err != nil || len(issues) != 0 {
		t.Fatalf("no reports yet: %v %v", issues, err)
	}
	if _, err := SaveIssue(Issue{What: "no title"}); err == nil {
		t.Fatal("a report needs a title")
	}
	t0 := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	for i, title := range []string{"second", "first"} {
		if _, err := SaveIssue(Issue{At: t0.Add(time.Duration(1-i) * time.Minute), Session: "S1",
			Title: title, What: "the watchdog said lead stopped 4520m ago", Expected: "a number no older than the task",
			Version: "1.0.0", Screen: "line one\nline two"}); err != nil {
			t.Fatal(err)
		}
	}
	issues, paths, err := ListIssues()
	if err != nil || len(issues) != 2 || len(paths) != 2 {
		t.Fatalf("two reports: %d %d %v", len(issues), len(paths), err)
	}
	if issues[0].Title != "first" || issues[1].Title != "second" {
		t.Fatalf("oldest first: %q, %q", issues[0].Title, issues[1].Title)
	}
	md := IssueMarkdown(issues[0])
	for _, want := range []string{"## first", "- reminal: 1.0.0", "- session: S1",
		"4520m ago", "**Expected**", "line two"} {
		if !strings.Contains(md, want) {
			t.Fatalf("export lacks %q:\n%s", want, md)
		}
	}
	if n, err := ClearIssues(); err != nil || n != 2 {
		t.Fatalf("clear: %d %v", n, err)
	}
	if issues, _, _ := ListIssues(); len(issues) != 0 {
		t.Fatal("cleared")
	}
}

// The report names the program that filed it, from the session's own record.
func TestHarnessOfReadsTheSessionRecord(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := session.WriteActive(session.Active{ID: "HARN0001", PID: 1, Fg: "claude"}); err != nil {
		t.Fatal(err)
	}
	if got := HarnessOf("harn0001"); got != "claude" {
		t.Fatalf("HarnessOf = %q, want claude", got)
	}
	if got := HarnessOf("NOSUCH01"); got != "" {
		t.Fatalf("HarnessOf(unknown) = %q, want empty", got)
	}
}
