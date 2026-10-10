// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !windows

package client

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The live repro: close Chromium while a pane mirrors it, and the in-flight
// `import -window <id>` never returns. It sleeps in poll holding the X server
// grab, so wmctrl and every other X client stall behind it. The stand-in
// import below blocks the same way and also leaves a child holding stdout, as
// a grab-holding helper would. runRaw must give up within captureCeiling and
// kill the whole process group, so no orphan keeps the grab.
func TestRunRawKillsAHungImportAndItsGroup(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	shim := "#!/bin/sh\nsleep 60 &\necho $! > " + pidFile + "\nsleep 60\n"
	if err := os.WriteFile(filepath.Join(dir, "import"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	old := captureCeiling
	captureCeiling = time.Second
	t.Cleanup(func() { captureCeiling = old })

	start := time.Now()
	done := make(chan error, 1)
	go func() {
		_, err := runRaw("import", "-window", "0x00400003", "jpeg:-")
		done <- err
	}()
	var err error
	select {
	case err = <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("runRaw is still waiting on a hung import after 15s: no ceiling")
	}
	el := time.Since(start)
	if err == nil || !strings.Contains(err.Error(), "gave up after") {
		t.Fatalf("runRaw on a hung import = %v, want a 'gave up after' error", err)
	}
	// Killing the group closes stdout at once. Killing only the shim would
	// leave the child holding it until WaitDelay (1s) gives up on the pipe.
	if el > captureCeiling+700*time.Millisecond {
		t.Fatalf("runRaw took %s for a %s ceiling: the child kept stdout open, so the group was not killed", el, captureCeiling)
	}

	b, rerr := os.ReadFile(pidFile)
	if rerr != nil {
		t.Fatalf("stand-in never recorded its child: %v", rerr)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	t.Cleanup(func() { syscall.Kill(pid, syscall.SIGKILL) })
	deadline := time.Now().Add(2 * time.Second)
	for alive(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("child %d of the hung import is still running: an orphan would keep the X grab", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// alive reports whether pid is a running process; a zombie (killed, not yet
// reaped by whoever inherited it) counts as gone.
func alive(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	if st, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat"); err == nil {
		if f := strings.Fields(string(st)); len(f) > 2 && f[2] == "Z" {
			return false
		}
	}
	return true
}
