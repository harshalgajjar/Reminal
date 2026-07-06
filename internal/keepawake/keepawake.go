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
	"strings"
)

// ReapOrphans kills leftover caffeinate inhibitors THIS process previously
// started — matched by `-w <our-pid>` in their args — that outlived their owner.
// A hot-restart (`reminal restart`, via syscall.Exec) keeps the same PID but
// never runs the stop funcs, so the old image's caffeinate children survive
// (their `-w <pid>` still points at us) and pile up across restarts, silently
// pinning the display awake. Call this ONCE at startup, BEFORE Start()/
// StartDisplay() spawn fresh inhibitors — at that point every `-w <our-pid>`
// caffeinate is a leftover from a previous incarnation, so reaping them both
// prevents accumulation and cleans up any mess an older (leaky) build left
// behind on the next restart. Orphans of a genuinely-exited PID already
// self-terminate via `-w`, so we only ever match our own live PID. macOS only.
func ReapOrphans() {
	if runtime.GOOS != "darwin" {
		return
	}
	self := strconv.Itoa(os.Getpid())
	out, err := exec.Command("pgrep", "-x", "caffeinate").Output()
	if err != nil {
		return
	}
	for _, f := range strings.Fields(string(out)) {
		cpid, err := strconv.Atoi(f)
		if err != nil {
			continue
		}
		args, err := exec.Command("ps", "-o", "args=", "-p", f).Output()
		if err != nil {
			continue
		}
		fields := strings.Fields(string(args))
		for i := 0; i+1 < len(fields); i++ {
			if fields[i] == "-w" && fields[i+1] == self {
				if p, err := os.FindProcess(cpid); err == nil {
					_ = p.Kill()
				}
				break
			}
		}
	}
}

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

// StartDisplay additionally prevents the DISPLAY from idle-sleeping, which is
// what stops the Mac from auto-locking (screensaver / display-off both trigger
// the lock, and macOS drops synthetic input behind the lock screen — so a
// locked host looks live but can't be controlled). Heavier than Start (the
// screen stays lit), so callers hold it only while a window is actively being
// mirrored/controlled, not for plain terminal sharing. Same best-effort +
// REMINAL_NO_KEEP_AWAKE opt-out contract as Start.
func StartDisplay() (stop func()) {
	noop := func() {}
	if os.Getenv("REMINAL_NO_KEEP_AWAKE") == "1" {
		return noop
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		if _, err := exec.LookPath("caffeinate"); err != nil {
			return noop
		}
		// -d: prevent display idle sleep (also inhibits the screensaver), so the
		// host can't idle-lock. -w: exit when reminal's pid exits.
		cmd = exec.Command("caffeinate", "-d", "-w", strconv.Itoa(os.Getpid()))
	default:
		// Linux idle-lock behaviour is desktop-environment specific; the base
		// idle inhibitor from Start already covers the common cases.
		return noop
	}
	if err := cmd.Start(); err != nil {
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
