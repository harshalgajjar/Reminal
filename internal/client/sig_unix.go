// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !windows

package client

import (
	"os"
	"os/signal"
	"syscall"
)

// notifyAgentSignals registers the signals a long-running agent/tunnel reacts
// to: SIGINT/SIGTERM to shut down, SIGUSR1 to pause broadcasting (`reminal
// stop`). Windows has no SIGUSR1 — pause arrives over the control socket there
// (see sig_windows.go / control.go "pause").
func notifyAgentSignals(ch chan os.Signal) {
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM, syscall.SIGUSR1)
}

// isPauseSignal reports whether sig is the pause-broadcast request.
func isPauseSignal(sig os.Signal) bool { return sig == syscall.SIGUSR1 }

// watchResize delivers a tick whenever the host terminal's size may have
// changed (SIGWINCH here; a size poller on Windows). The returned stop func
// unregisters the watcher.
func watchResize() (<-chan os.Signal, func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	return ch, func() { signal.Stop(ch) }
}

// respawnDaemonAfterUpgrade is a no-op where a service manager (launchd
// KeepAlive / systemd Restart=always) restarts the exited daemon itself.
func respawnDaemonAfterUpgrade(exe string) {}
