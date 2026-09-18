// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build darwin

package keepawake

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// childState returns the ps STAT letter for pid, or "" once it is truly gone.
// A reaped child disappears; an unreaped one lingers as Z (defunct).
func childState(pid int) string {
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func caffeinateChild(t *testing.T) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		out, _ := exec.Command("pgrep", "-P", strconv.Itoa(os.Getpid()), "-x", "caffeinate").Output()
		for _, f := range strings.Fields(string(out)) {
			if pid, err := strconv.Atoi(f); err == nil {
				return pid
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return 0
}

// TestInhibitorIsReapedWhenItExitsOnItsOwn is the leak this guards against.
// caffeinate is started with `-w <our pid>`, so it is BUILT to exit by itself;
// Wait used to live only inside stop(), so anything that exited without stop()
// being called stayed defunct for the life of the agent. 752 of them had piled
// up across eleven long-running sessions on one Mac.
func TestInhibitorIsReapedWhenItExitsOnItsOwn(t *testing.T) {
	t.Setenv("REMINAL_NO_KEEP_AWAKE", "")
	if _, err := exec.LookPath("caffeinate"); err != nil {
		t.Skip("no caffeinate")
	}
	stop := Start()
	defer stop()

	pid := caffeinateChild(t)
	if pid == 0 {
		t.Skip("keep-awake did not spawn a child here")
	}

	// Simulate it exiting on its own, which is its normal end.
	if err := exec.Command("kill", strconv.Itoa(pid)).Run(); err != nil {
		t.Fatalf("kill: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		st := childState(pid)
		if st == "" {
			return // reaped
		}
		if !strings.HasPrefix(st, "Z") {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		// Defunct: give the reaper a moment, then fail if it persists.
		time.Sleep(300 * time.Millisecond)
		if st2 := childState(pid); strings.HasPrefix(st2, "Z") {
			t.Fatalf("caffeinate %d is still defunct (%s) — it exited on its own and nothing reaped it", pid, st2)
		}
		return
	}
	t.Fatalf("caffeinate %d never went away (state %q)", pid, childState(pid))
}

// TestStopIsIdempotentAndLeaksNothing covers the two ways the reaper could go
// wrong now that Wait lives in a goroutine: stop() called twice must not block
// on an already-closed channel or panic, and churning inhibitors must not leave
// defunct children behind. Both inhibitors are exercised, since Start and
// StartDisplay spawn the pair that was accumulating.
func TestStopIsIdempotentAndLeaksNothing(t *testing.T) {
	if _, err := exec.LookPath("caffeinate"); err != nil {
		t.Skip("no caffeinate")
	}
	for i := 0; i < 20; i++ {
		stop := Start()
		stop()
		stop() // must be a no-op, not a hang or a panic
	}
	for i := 0; i < 8; i++ {
		base, display := Start(), StartDisplay()
		display()
		base()
	}
	out, _ := exec.Command("ps", "-axo", "ppid,stat,comm").Output()
	defunct := 0
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) == 3 && f[0] == strconv.Itoa(os.Getpid()) &&
			strings.HasPrefix(f[1], "Z") && strings.Contains(f[2], "caffeinate") {
			defunct++
		}
	}
	if defunct > 0 {
		t.Fatalf("%d defunct caffeinate left behind after churn", defunct)
	}
}
