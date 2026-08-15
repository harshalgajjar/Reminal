// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build windows

package client

import (
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

// resizePollInterval is how often the Windows resize watcher samples the
// console size. There is no SIGWINCH; conhost's window-resize console events
// would need a raw ReadConsoleInput loop that fights the stdin reader, so a
// cheap poll (two syscalls) is the robust choice.
const resizePollInterval = 400 * time.Millisecond

// watchResize delivers a tick whenever the host terminal's size changes,
// by polling. The tick value is a dummy signal — consumers only use the
// channel as an edge trigger and re-read the size themselves.
func watchResize() (<-chan os.Signal, func()) {
	ch := make(chan os.Signal, 1)
	stop := make(chan struct{})
	go func() {
		fd := int(os.Stdin.Fd())
		lastW, lastH, _ := term.GetSize(fd)
		t := time.NewTicker(resizePollInterval)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				w, h, err := term.GetSize(fd)
				if err != nil || (w == lastW && h == lastH) {
					continue
				}
				lastW, lastH = w, h
				select {
				case ch <- syscall.Signal(0):
				default: // a pending tick already queued — coalesce
				}
			}
		}
	}()
	var once bool
	return ch, func() {
		if !once {
			once = true
			close(stop)
		}
	}
}

// respawnDaemonAfterUpgrade hands the daemon off to the freshly-upgraded
// binary: nothing supervises the Windows daemon (the Run key only fires at
// logon), so the exiting instance starts its own replacement.
func respawnDaemonAfterUpgrade(exe string) {
	_ = spawnDetachedDaemon(exe)
}
