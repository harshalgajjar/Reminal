// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"os"
	"testing"
	"time"

	"reminal/internal/session"
)

// TestRestartOnMachineRoundTrip drives a remote restart against a real relay and
// a real directory host: the owner handshake, the dir_query carrying the restart
// request, the host acting on it, and the RestartOK/RestartCount answer coming
// back. It proves the request reaches the host and is reported honestly, which
// is the part a caller cannot verify any other way — a host that ignored the
// field would answer with a plain session list and RestartOK false.
//
// The seeded session is this test's own pid, which restartOtherSessions skips
// (it never restarts the process serving the request), so nothing is actually
// signalled — the round trip is what's under test, not the signal.
func TestRestartOnMachineRoundTrip(t *testing.T) {
	isolateHome(t)
	startTestRelay(t)

	id, err := MyOwnerID()
	if err != nil {
		t.Fatalf("owner id: %v", err)
	}
	if _, _, err := AddOwner(id, "self"); err != nil {
		t.Fatalf("add owner: %v", err)
	}
	machinePub, err := MachinePub()
	if err != nil {
		t.Fatalf("machine pub: %v", err)
	}
	if err := session.WriteActive(session.Active{
		ID: "RSTRT001", Name: "work", PIN: "123456", Cwd: "/home/x", PID: os.Getpid(),
	}); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	defer close(stop)
	go runDirectoryHost(stop, true, "test")

	deadline := time.Now().Add(6 * time.Second)
	for {
		count, ok, rerr := RestartOnMachine(machinePub, DirectoryTimeout)
		if rerr == nil && ok {
			if count < 1 {
				t.Fatalf("restart reported ok but counted %d sessions, want the seeded one", count)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("remote restart never succeeded: ok=%v err=%v", ok, rerr)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestRestartRequestIsNotEmpty guards the wire contract: a restart-only request
// must not be treated as an empty query, or it would be sent with no payload at
// all and the host would silently just list sessions.
func TestRestartRequestIsNotEmpty(t *testing.T) {
	if (dirQueryReq{Restart: true}).empty() {
		t.Fatal("a restart-only request must not count as empty — it would be sent with no payload")
	}
	if !(dirQueryReq{}).empty() {
		t.Fatal("a zero request should still be empty")
	}
}
