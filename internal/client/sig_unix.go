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

// execTunnelBinary replaces the current process image with exe (a port
// forward's hot-swap, see Tunnel.execRestart). Never returns on success.
func execTunnelBinary(exe string, args, env []string) error {
	return syscall.Exec(exe, args, env)
}

// RestartPortForward asks a running port forward (by pid) to hot-swap onto the
// binary now on disk, keeping its session id and PIN. The forward treats
// SIGUSR1 as that request (the shell agent uses the same signal for "pause").
func RestartPortForward(pid int) error {
	return syscall.Kill(pid, syscall.SIGUSR1)
}

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
