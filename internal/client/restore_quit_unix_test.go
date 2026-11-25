// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !windows

package client

import (
	"testing"
	"time"

	"reminal/internal/pty"
	"reminal/internal/session"
)

// A session asked to end (reminal kill, Ctrl-]) settles its record while its
// shell is still running: the terminal is closed after. It waited two seconds
// for that shell to say how it ended, so every kill found the process still
// there at its own two seconds and went on to terminate it. Ended on purpose
// is known without asking the shell: it is forgotten at once.
func TestASessionAskedToEndDoesNotWaitForItsShell(t *testing.T) {
	isolateHome(t)
	term, err := pty.Start("/bin/sh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { term.Hangup(); _ = term.Close() })
	saveRecord(t, "QUIT0001")
	a := &Agent{sessionID: "QUIT0001", term: term, hostEscape: make(chan struct{})}
	a.requestShutdown()
	start := time.Now()
	a.settleRestore()
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Fatalf("a session asked to end took %s to settle: it waited for its running shell", took)
	}
	if _, err := session.ReadRestore("QUIT0001"); err == nil {
		t.Fatal("a session ended on purpose was kept to come back")
	}
}
