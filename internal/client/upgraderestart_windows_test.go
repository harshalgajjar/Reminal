// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build windows

package client

import (
	"os/exec"
	"testing"

	"reminal/internal/session"
)

// TestRestartOtherSessionsSkipsForwardOnWindows: Windows cannot hot-swap a
// port forward, and that must not count as a session left behind. It did, so
// any Windows host with a live `reminal expose` reported every upgrade as
// failed — after the binary had already been replaced — and skipped the
// restart of the session the viewer was on.
func TestRestartOtherSessionsSkipsForwardOnWindows(t *testing.T) {
	home := isolateHome(t)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir reads this, not HOME, on Windows

	// A live pid that is not ours, so the record survives ReadAllActive's
	// liveness prune and is not skipped as "self".
	child := exec.Command("ping", "-n", "30", "127.0.0.1")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()

	if err := session.WriteActive(session.Active{
		ID: "PORTFWD1", Kind: session.KindPort, Port: 8765, PID: child.Process.Pid, Version: "3.13.18",
	}); err != nil {
		t.Fatal(err)
	}
	if n := countRestartableSessions(); n != 0 {
		t.Fatalf("a forward counted as %d restartable session(s), want 0", n)
	}
	if err := restartOtherSessions(); err != nil {
		t.Fatalf("an un-hot-swappable forward was reported as a failure: %v", err)
	}
}
