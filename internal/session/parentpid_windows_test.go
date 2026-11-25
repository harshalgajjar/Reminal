// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build windows

package session

import (
	"os"
	"os/exec"
	"testing"
)

// On a real Windows: a child's parent is this process (created before it),
// and the walk from the child reaches a session that is this process.
func TestParentOfAChildOnWindows(t *testing.T) {
	c := exec.Command("cmd", "/c", "ping -n 6 127.0.0.1 >NUL")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Process.Kill(); _ = c.Wait() }()
	if got := parentPID(c.Process.Pid); got != os.Getpid() {
		t.Fatalf("parent of the child: %d, want %d", got, os.Getpid())
	}
	if got := snapshotParent(c.Process.Pid); got != os.Getpid() {
		t.Fatalf("control: the snapshot's parent: %d", got)
	}
	if got := nearestOf(c.Process.Pid, map[int]int{os.Getpid(): os.Getpid()}); got != os.Getpid() {
		t.Fatalf("the session above the child: %d", got)
	}
	ps, pok := procStartTime(os.Getpid())
	cs, cok := procStartTime(c.Process.Pid)
	if !pok || !cok || ps.After(cs) {
		t.Fatalf("start times: parent %v %v, child %v %v", ps, pok, cs, cok)
	}
	// The order Windows made them in: read for both, this process first.
	pn, pnok := procOrder(os.Getpid())
	cn, cnok := procOrder(c.Process.Pid)
	if !pnok || !cnok || pn >= cn {
		t.Fatalf("order numbers: parent %d %v, child %d %v: want both read, the parent's the smaller", pn, pnok, cn, cnok)
	}
	// And it is that order: three more children, each made after the last.
	last := cn
	for i := 0; i < 3; i++ {
		k := exec.Command("cmd", "/c", "ping -n 3 127.0.0.1 >NUL")
		if err := k.Start(); err != nil {
			t.Fatal(err)
		}
		n, ok := procOrder(k.Process.Pid)
		_ = k.Process.Kill()
		_ = k.Wait()
		if !ok || n <= last {
			t.Fatalf("child %d: order number %d %v, not after %d", i, n, ok, last)
		}
		last = n
	}
	t.Logf("order numbers: this process %d, its children %d .. %d", pn, cn, last)
}
