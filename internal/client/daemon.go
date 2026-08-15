// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// RunDaemon runs this machine's directory host in the foreground until it gets
// SIGINT/SIGTERM. It's the always-on presence layer: a machine's directory host
// otherwise lives only inside a running session, so a machine with no live
// session drops off `reminal machines` entirely and can't be spawned onto (the
// per-machine "+" sends its request to a host that isn't there). The login
// service (see InstallDaemonService) runs this so an OWNED machine stays
// reachable — listable and "+"-spawnable — even while idle.
//
// It shares the machine-local single-host flock with any session-embedded hosts,
// so exactly one process ever serves the channel; whichever holds the lock
// answers from the same on-disk session registry, so it doesn't matter which. A
// no-op while the machine is unowned (runDirectoryHost re-checks periodically),
// so it's harmless to leave running after every owner is revoked.
func RunDaemon() error {
	stop := make(chan struct{})
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		close(stop)
	}()
	// Record our pid so platforms without a service manager (Windows: a
	// registry Run key starts us, nothing supervises us) can find and manage
	// this process — see service_windows.go. Written everywhere for
	// consistency; launchd/systemd simply never read it.
	writeDaemonPID()
	defer clearDaemonPID()
	go watchBinaryAndExit(stop)
	if runtime.GOOS == "darwin" {
		// The daemon is the single granted process that does all window/desktop
		// capture + input injection; sessions delegate to it over mirror.sock so
		// one reminal.app grant covers every session.
		go serveMirror(stop)
	}
	runDirectoryHost(stop, true)
	return nil
}

// binaryWatchInterval is how often the daemon checks whether its own executable
// changed on disk. Upgrades are rare, so a coarse poll is plenty.
const binaryWatchInterval = 30 * time.Second

// daemonPIDPath is where the running daemon records its pid. Only Windows
// reads it (its Run-key autostart has no supervisor to ask "is it running?"),
// but it's maintained on every platform for a consistent on-disk layout.
func daemonPIDPath() (string, error) {
	dir, err := reminalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "daemon.pid"), nil
}

func writeDaemonPID() {
	path, err := daemonPIDPath()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600)
}

func clearDaemonPID() {
	path, err := daemonPIDPath()
	if err != nil {
		return
	}
	// Only remove our own record — a crashed daemon's stale file is fine to
	// clobber, but a NEWER daemon's live record must survive our exit.
	if b, err := os.ReadFile(path); err == nil {
		if pid, _ := strconv.Atoi(strings.TrimSpace(string(b))); pid != os.Getpid() {
			return
		}
	}
	_ = os.Remove(path)
}

// watchBinaryAndExit exits the daemon when its own on-disk binary is replaced, so
// the service manager (launchd KeepAlive / systemd Restart=always) restarts it
// onto the new image. `reminal upgrade` and `restart --all` bounce the service
// explicitly, but this backstops every OTHER path that swaps the binary without
// telling us — most importantly a background *critical* (security) auto-update,
// after which a long-lived daemon would otherwise keep serving from the old,
// still-vulnerable binary. The upgrade is an atomic rename, so a stat always sees
// either the whole old or the whole new file — never a partial write.
func watchBinaryAndExit(stop <-chan struct{}) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	base, err := os.Stat(exe)
	if err != nil {
		return
	}
	t := time.NewTicker(binaryWatchInterval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			fi, err := os.Stat(exe)
			if err != nil {
				continue // transient (mid-rename or briefly absent) — re-check next tick
			}
			if fi.Size() != base.Size() || !fi.ModTime().Equal(base.ModTime()) {
				// Replaced (e.g. by an upgrade). Exit cleanly; the service manager
				// starts a fresh instance from the new binary. Flock and sockets are
				// released by the OS on exit, so no cleanup is needed here.
				//
				// Windows has no supervising service manager (the Run key only
				// fires at logon), so hand off to a fresh copy of the new binary
				// ourselves before exiting.
				respawnDaemonAfterUpgrade(exe)
				clearDaemonPID()
				os.Exit(0)
			}
		}
	}
}
