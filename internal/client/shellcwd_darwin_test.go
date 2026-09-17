// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build darwin

package client

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestProcCwdDarwinMatchesOurOwn is the offset check: the struct layout is
// hard-coded, and a wrong cdirPathOffset would read neighbouring memory and
// return a plausible-looking but wrong path. Our own cwd is known exactly, so
// it is the one answer that cannot be fudged.
func TestProcCwdDarwinMatchesOurOwn(t *testing.T) {
	want, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	got := procCwdDarwin(os.Getpid())
	if got == "" {
		t.Fatal("no cwd from the syscall — offset or flavor is wrong for this kernel")
	}
	if rg, _ := filepath.EvalSymlinks(got); rg != "" {
		got = rg
	}
	if rw, _ := filepath.EvalSymlinks(want); rw != "" {
		want = rw
	}
	if got != want {
		t.Fatalf("cwd = %q, want %q", got, want)
	}
}

// TestProcCwdDarwinReadsAnotherProcess covers the case that matters in
// production: the directory of a DIFFERENT process (a session's shell), not our
// own. Run in a temp dir so the answer cannot coincide with the test's cwd.
func TestProcCwdDarwinReadsAnotherProcess(t *testing.T) {
	dir := t.TempDir()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		real = dir
	}
	cmd := exec.Command("sleep", "30")
	cmd.Dir = real
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }()

	var got string
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if got = procCwdDarwin(cmd.Process.Pid); got != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if rg, _ := filepath.EvalSymlinks(got); rg != "" {
		got = rg
	}
	if got != real {
		t.Fatalf("child cwd = %q, want %q", got, real)
	}
}

// TestProcCwdDarwinSurvivesADeadPid: a session whose shell just exited must
// yield "" rather than an error or a stale path, since shellCwd is called on a
// poll and the pid can vanish underneath it.
func TestProcCwdDarwinSurvivesADeadPid(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if got := procCwdDarwin(cmd.Process.Pid); got != "" {
		t.Fatalf("dead pid gave %q, want empty", got)
	}
}
