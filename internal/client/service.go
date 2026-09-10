// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
)

// Service identifiers + the pure generators for the two platforms' service
// definitions. Kept here (build-tag-free) so they can be unit-tested on any OS —
// a malformed plist or a dropped systemd directive (e.g. KillMode=process) would
// otherwise only surface at install time on the target platform.
const (
	daemonLabel = "sh.reminal.daemon"      // macOS LaunchAgent label
	daemonUnit  = "reminal-daemon.service" // Linux systemd --user unit
)

// launchdPlist is the macOS LaunchAgent that runs `reminal daemon` at login and
// keeps it alive. exe/logPath are XML-escaped for the plist string values.
//
// Deliberately NO `ProcessType` key: ProcessType=Background clamps the daemon to
// low-priority efficiency-core QoS, and sessions it spawns INHERIT that QoS — so
// interactive terminals and window streaming spawned via the "+" ran throttled
// (~5 fps, sluggish startup). Omitting it leaves the daemon at Standard QoS, so
// spawned sessions run at normal priority like a terminal-started one.
func launchdPlist(exe, logPath string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>daemon</string>
	</array>
	<key>RunAtLoad</key><true/>
	<key>KeepAlive</key><true/>
	<key>StandardOutPath</key><string>%s</string>
	<key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, daemonLabel, xmlEscape(exe), xmlEscape(logPath), xmlEscape(logPath))
}

// systemdUnit is the Linux systemd --user unit that runs `reminal daemon`.
// KillMode=process is load-bearing: without it, stopping/restarting the daemon
// would SIGKILL the "+"-spawned sessions in its cgroup (setsid escapes the
// process group, not the cgroup) — see service_linux.go.
func systemdUnit(exe string) string {
	return fmt.Sprintf(`[Unit]
Description=reminal background host (keeps this machine reachable to its owners)
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=%s daemon
Restart=always
RestartSec=3
KillMode=process

[Install]
WantedBy=default.target
`, exe)
}

// xmlEscape escapes the characters that would break a plist string value.
func xmlEscape(s string) string {
	repl := map[rune]string{'&': "&amp;", '<': "&lt;", '>': "&gt;", '"': "&quot;", '\'': "&apos;"}
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if e, ok := repl[r]; ok {
			out = append(out, []rune(e)...)
		} else {
			out = append(out, r)
		}
	}
	return string(out)
}

// The background-host login service (a macOS LaunchAgent / Linux systemd --user
// unit) runs `reminal daemon` at login and restarts it on crash, so an owned
// machine keeps a presence — see RunDaemon. It's installed when the machine gains
// an owner and removed when it loses its last one, so the service exists exactly
// when it's needed. Users don't manage it directly.
//
// The subtlety these functions exist to handle: enrolling an owner writes the
// root-owned owner store, so `reminal add owner` runs as root (via sudo). But the
// daemon must run as the HUMAN user — it needs ~/.reminal's keys and spawns the
// user's sessions — and a per-user service lives in the user's home and login
// domain, never root's. So we resolve the *target* user (SUDO_USER when we're
// root) and the platform code creates + loads the service into that user's
// domain (launchctl asuser / systemctl --user), chowning files to them.

// InstallDaemonService installs and starts the background-host login service for
// the owning user. Idempotent. Safe to call right after enrolling an owner, as
// root (under sudo) or as the user.
func InstallDaemonService() error {
	u, err := targetUser()
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	// Follow symlinks so the service runs the REAL binary. On macOS the installed
	// CLI is a symlink into reminal.app; the daemon must exec the bundle's binary
	// directly so it runs as the sh.reminal identity — the one whose Screen
	// Recording grant covers the ("+") sessions the daemon spawns.
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}
	return installService(exe, u)
}

// UninstallDaemonService stops and removes the login service. Idempotent — a
// no-op (nil) when nothing is installed.
func UninstallDaemonService() error {
	u, err := targetUser()
	if err != nil {
		return err
	}
	return uninstallService(u)
}

// RestartDaemonService restarts the running background host so it re-execs a
// freshly-installed binary (e.g. after `reminal upgrade`). A no-op (nil) when the
// service isn't installed. Best-effort.
func RestartDaemonService() error {
	u, err := targetUser()
	if err != nil {
		return err
	}
	return restartService(u)
}

// EnsureDaemonInstalled makes the always-on background daemon exist on this
// machine, independent of whether any owner is enrolled: the daemon is the
// machine's presence + stats layer — it answers `reminal machines`, samples the
// CPU/memory vitals the cards read, and (on macOS) performs the one-grant screen
// capture + input injection for every session. Installing it here, not in the
// add-owner flow, is what lets a machine report its usage stats from the moment
// it boots — before, and whether or not, anyone owns it.
//
// Idempotent (a cheap stat of the login service) and a no-op on a build that
// must not register a service: a macOS bare/dev binary (not running from the
// reminal.app bundle) or an un-stamped "dev" build on Linux/Windows. Real
// releases carry a stamped version; those install. Safe to call on every startup
// and after an upgrade/migration, so a machine-without-daemon self-heals
// regardless of which version introduced the gap.
//
// It never reinstalls over a LIVE daemon: InstallDaemonService does a
// bootout+bootstrap (macOS) / enable --now that would tear down the running
// daemon serving capture + notes for EVERY session. The service-file check
// handles the normal case, but a transiently-missing file (a stat hiccup, or an
// upgrade mid-reinstall) must not trick a concurrent session — now that every
// session self-heals, including ones the daemon itself spawned — into that. A
// live pid short-circuits regardless of the file.
func EnsureDaemonInstalled(version string) {
	if DaemonServiceInstalled() || daemonAlive() || !autoInstallDaemon(version) {
		return
	}
	_ = InstallDaemonService()
}

// isReleaseBuild reports whether this is a stamped release (not an un-stamped
// local `go build`/`go run`, whose version defaults to "dev"). The Linux and
// Windows daemon gates key on it so development never registers a login service.
func isReleaseBuild(version string) bool {
	return version != "" && version != "dev"
}

// DaemonServiceInstalled reports whether the background-host login service is
// installed for the owning user. Lets callers decide whether to mention/refresh
// it (e.g. `reminal restart --all`). False on any lookup error.
func DaemonServiceInstalled() bool {
	u, err := targetUser()
	if err != nil {
		return false
	}
	return serviceInstalled(u)
}

// targetUser is the human user the service belongs to: SUDO_USER when we're root
// under sudo, otherwise the current user.
func targetUser() (*user.User, error) {
	if os.Geteuid() == 0 {
		if su := os.Getenv("SUDO_USER"); su != "" && su != "root" {
			return user.Lookup(su)
		}
		return nil, errors.New("run this as your user (or via sudo) so the background host is owned by you, not root")
	}
	return user.Current()
}
