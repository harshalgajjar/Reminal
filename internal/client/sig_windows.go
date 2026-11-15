// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build windows

package client

import (
	"errors"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/term"
)

// notifyAgentSignals registers the shutdown signals. SIGUSR1 doesn't exist on
// Windows; `reminal stop` delivers the pause request over the agent's control
// socket instead (control.go "pause" verb), so only INT/TERM are trapped here.
func notifyAgentSignals(ch chan os.Signal) {
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
}

// isPauseSignal: pause never arrives as a signal on Windows.
func isPauseSignal(sig os.Signal) bool { return false }

// execTunnelBinary: Windows has no exec-in-place; a port forward can't be
// hot-swapped, so the CLI tells the user to stop + re-expose instead.
func execTunnelBinary(exe string, args, env []string) error { return errPortRestartUnsupported }

// RestartPortForward: no SIGUSR1 on Windows — see execTunnelBinary.
func RestartPortForward(pid int) error { return errPortRestartUnsupported }

// resizePollInterval is how often the Windows resize watcher samples the
// console size. There is no SIGWINCH; conhost's window-resize console events
// would need a raw ReadConsoleInput loop that fights the stdin reader, so a
// cheap poll (two syscalls) is the robust choice.
const resizePollInterval = 400 * time.Millisecond

// watchResize delivers a tick whenever the host terminal's size changes,
// by polling. The tick value is a dummy signal — consumers only use the
// channel as an edge trigger and re-read the size themselves.
//
// The size is read off the console's output side: stdout, else stderr, else
// the console itself (CONOUT$). Read off stdin it failed every time, so a
// window resized never resized its session.
func watchResize() (<-chan os.Signal, func()) {
	read := firstWorking(consoleSize(os.Stdout), consoleSize(os.Stderr))
	if read == nil {
		// Both redirected: the console itself, kept open while watched.
		if f, err := os.OpenFile("CONOUT$", os.O_RDWR, 0); err == nil {
			if read = firstWorking(consoleSize(f)); read == nil {
				_ = f.Close()
			}
		}
	}
	return pollSize(read, resizePollInterval)
}

// consoleSize reads f's console size.
func consoleSize(f *os.File) sizeReader {
	return func() (int, int, error) { return term.GetSize(int(f.Fd())) }
}

// respawnDaemonAfterUpgrade hands the daemon off to the freshly-upgraded
// binary: nothing supervises the Windows daemon (the Run key only fires at
// logon), so the exiting instance starts its own replacement.
func respawnDaemonAfterUpgrade(exe string) {
	_ = spawnDetachedDaemon(exe)
}

// execDaemonInPlace is Unix's: Windows has no exec, and hands off to a fresh
// copy instead (respawnDaemonAfterUpgrade).
func execDaemonInPlace(exe string) error {
	return errors.New("no exec in place on Windows")
}
