// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

// Package keepawake prevents the host machine from sleeping while a reminal
// agent is running — the whole point of leaving reminal up is so you can come
// back to it from another device, which doesn't work if the laptop sleeps the
// moment you walk away.
//
// Best-effort: macOS uses `caffeinate`, Linux uses `systemd-inhibit` when
// available. Other platforms and missing tools are silently skipped.
package keepawake

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
)

// Start launches a sleep inhibitor as a child process and returns a stop
// function. Safe to call unconditionally; stop is always non-nil. Respects
// REMINAL_NO_KEEP_AWAKE=1 as an opt-out.
func Start() (stop func()) {
	noop := func() {}
	if os.Getenv("REMINAL_NO_KEEP_AWAKE") == "1" {
		return noop
	}
	cmd := command()
	if cmd == nil {
		return noop
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "  reminal: keep-awake disabled (%v)\n", err)
		return noop
	}
	return func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}
}

func command() *exec.Cmd {
	switch runtime.GOOS {
	case "darwin":
		if _, err := exec.LookPath("caffeinate"); err != nil {
			return nil
		}
		// -i: prevent idle sleep. -w: exit when this pid exits, so we
		// auto-clean even if reminal is killed without running our stop fn.
		return exec.Command("caffeinate", "-i", "-w", strconv.Itoa(os.Getpid()))
	case "linux":
		if _, err := exec.LookPath("systemd-inhibit"); err != nil {
			return nil
		}
		// systemd-inhibit holds the lock for as long as its child runs, so
		// we tail an infinite sleep and kill it from Stop().
		return exec.Command("systemd-inhibit",
			"--what=idle:sleep",
			"--who=reminal",
			"--why=Sharing terminal session",
			"--mode=block",
			"sleep", "infinity")
	}
	return nil
}
