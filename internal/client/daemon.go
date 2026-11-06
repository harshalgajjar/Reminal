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
	"sync"
	"syscall"
	"time"

	"reminal/internal/proc"
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
func RunDaemon(version string) error {
	// The directory-host lock, held still, when this is a daemon re-exec'd in
	// place onto a new binary (execDaemonInPlace): taken first, before any
	// child is started.
	adoptDirHostLock()
	// Same reason as Agent.Run: the daemon outlives every session it spawns, so
	// it is the other process that can carry a backlog across a restart.
	reapInherited()
	// Its own agent plugins, as this binary writes them: it is started anew on
	// every upgrade (execDaemonInPlace).
	HealOwnPlugins()

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
	go sweepStaleSockets()
	go watchBinaryAndExit(stop)
	if runtime.GOOS == "darwin" {
		// The daemon is the single granted process that does all window/desktop
		// capture + input injection; sessions delegate to it over mirror.sock so
		// one reminal.app grant covers every session.
		go serveMirror(stop)
		// And it owns the closed-lid display census, for the same reason it
		// owns the notes: one machine, one singleton. Ten sessions each running
		// their own ~100ms osascript every 12s is ~50 AppleScript spawns a
		// minute on an idle laptop, all but one of them answering a question
		// that has a single machine-wide answer.
		go vdisplayLoop(stop, true)
	}
	// The daemon also owns the window notes and the one badge helper, so the N
	// `reminal mcp` processes a machine runs can't each hold a private copy and
	// overwrite each other. Served on every platform: the badge is macOS-only
	// today, but mirroring notes to a phone is not.
	go ServeNotes(stop)
	// Uploads carry an "auto-delete in …" promise made to whoever sent the
	// file. The session that took it is usually gone long before the deadline,
	// so the always-on process is what actually keeps the promise.
	go sweepUploadsLoop(stop)
	// Phone alerts (CPU, battery, charger) for every owner phone that asked.
	// Idle — no sampling at all — until one has.
	go runPushWatcher(stop)
	// Sessions a restart ended come back as they were — the daemon starts at
	// login, so this is the moment after a reboot. See restore.go.
	go restoreAtStart()
	runDirectoryHost(stop, true, version)
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

// daemonAlive reports whether the pid recorded in daemon.pid is a live
// process — the "is anyone actually home?" check behind the directory-host
// deferral. Missing or unparsable pid file counts as dead.
func daemonAlive() bool {
	pid := daemonPIDRecorded()
	return pid > 0 && proc.Alive(pid)
}

// daemonPIDRecorded is the pid daemon.pid names, or 0.
func daemonPIDRecorded() int {
	path, err := daemonPIDPath()
	if err != nil {
		return 0
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}

// daemonRunning is daemonAlive, and — where the system says (Linux) — the
// process daemon.pid names a daemon still: a file left by one that crashed,
// its pid since another program's, never stands for one. A session that
// took it for one would leave the machine served by nobody.
func daemonRunning() bool {
	pid := daemonPIDRecorded()
	if pid <= 0 || !proc.Alive(pid) {
		return false
	}
	if runtime.GOOS != "linux" {
		return true
	}
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return false
	}
	args := strings.Split(string(b), "\x00")
	return len(args) > 1 && args[1] == "daemon"
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
				// Where no service manager would (a Linux machine with no user
				// manager, whose daemon was started by hand; another Unix), it
				// starts itself on the new binary, in place: the same process, its
				// sockets let go by the exec and taken again by the new image, and
				// the directory-host lock handed to it, held (execDaemonInPlace).
				// Exited, it would never have come back.
				if runtime.GOOS != "windows" && !daemonServiceManaged() {
					runDaemonExitHooks()
					_ = execDaemonInPlace(exe) // returns only when it could not
				}
				// Windows has no supervising service manager (the Run key only
				// fires at logon), so hand off to a fresh copy of the new binary
				// ourselves before exiting.
				respawnDaemonAfterUpgrade(exe)
				clearDaemonPID()
				runDaemonExitHooks()
				os.Exit(0)
			}
		}
	}
}

// Exit hooks: work that must happen before the daemon's os.Exit on an
// upgrade — asking the badge helper to quit, so the next daemon's helper is
// the only one on screen.
var (
	exitHooksMu sync.Mutex
	exitHooks   []func()
)

func onDaemonExit(f func()) {
	exitHooksMu.Lock()
	exitHooks = append(exitHooks, f)
	exitHooksMu.Unlock()
}

func runDaemonExitHooks() {
	// Each once: an exec in place that failed falls back to the exit, which
	// runs them again.
	exitHooksMu.Lock()
	hooks := exitHooks
	exitHooks = nil
	exitHooksMu.Unlock()
	for _, f := range hooks {
		f()
	}
}
