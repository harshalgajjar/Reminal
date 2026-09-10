// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"time"
)

// macOS background host: a per-user LaunchAgent that runs `reminal daemon`.
//
// Loading it into the user's GUI login domain is the fiddly part. When we're the
// user we can `launchctl bootstrap gui/<uid>` directly; when we're root (a sudo'd
// enroll) we must reach into the user's domain with `launchctl asuser <uid>` and
// chown the plist to them — plain `sudo -u` doesn't join their launchd session.

// installService writes the LaunchAgent plist into u's home and (re)loads it into
// u's GUI domain, starting it immediately. Idempotent.
func installService(exe string, u *user.User) error {
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	amRoot := os.Geteuid() == 0

	laDir := filepath.Join(u.HomeDir, "Library", "LaunchAgents")
	if err := os.MkdirAll(laDir, 0o755); err != nil {
		return err
	}
	if amRoot {
		// ~/Library/LaunchAgents often doesn't exist until the first agent is
		// installed; if root's MkdirAll just created it, give it back to the user
		// so their own future installs (and launchd) can use it.
		_ = os.Chown(laDir, uid, gid)
	}
	plistPath := filepath.Join(laDir, daemonLabel+".plist")
	logPath := filepath.Join(u.HomeDir, ".reminal", "daemon.log")

	plist := launchdPlist(exe, logPath)

	if err := os.WriteFile(plistPath, []byte(plist), 0o644); err != nil {
		return err
	}
	if amRoot {
		// A user LaunchAgent must be owned by that user, not root.
		_ = os.Chown(plistPath, uid, gid)
	}

	domain := fmt.Sprintf("gui/%d", uid)
	target := fmt.Sprintf("gui/%d/%s", uid, daemonLabel)
	// Reload cleanly: drop any prior instance, then bootstrap the new plist.
	// launchd needs a beat to release the label after bootout — bootstrapping too
	// soon fails with "5: Input/output error" and would leave the daemon DOWN — so
	// retry with a short backoff. Fall back to the legacy load verb on older
	// launchds.
	_ = launchctl(uid, amRoot, "bootout", target).Run()
	var out []byte
	var err error
	for attempt := 0; attempt < 4; attempt++ {
		time.Sleep(time.Duration(attempt) * 400 * time.Millisecond)
		if out, err = launchctl(uid, amRoot, "bootstrap", domain, plistPath).CombinedOutput(); err == nil {
			break
		}
	}
	if err != nil {
		if out2, err2 := launchctl(uid, amRoot, "load", "-w", plistPath).CombinedOutput(); err2 != nil {
			return fmt.Errorf("launchctl bootstrap: %v: %s", err, firstNonEmpty(out, out2))
		}
	}
	// Make "start now" explicit (bootstrap honours RunAtLoad; kickstart is a
	// harmless no-op if it's already up).
	_ = launchctl(uid, amRoot, "kickstart", "-k", target).Run()
	return nil
}

// uninstallService stops the agent and removes its plist. Idempotent.
func uninstallService(u *user.User) error {
	uid, _ := strconv.Atoi(u.Uid)
	amRoot := os.Geteuid() == 0
	plistPath := filepath.Join(u.HomeDir, "Library", "LaunchAgents", daemonLabel+".plist")

	_ = launchctl(uid, amRoot, "bootout", fmt.Sprintf("gui/%d/%s", uid, daemonLabel)).Run()
	_ = launchctl(uid, amRoot, "unload", "-w", plistPath).Run() // legacy fallback
	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// serviceInstalled reports whether the LaunchAgent plist exists for u.
func serviceInstalled(u *user.User) bool {
	_, err := os.Stat(filepath.Join(u.HomeDir, "Library", "LaunchAgents", daemonLabel+".plist"))
	return err == nil
}

// runningFromBundle reports whether this binary is running from the reminal.app
// bundle — the condition under which the always-on capture daemon must exist (so
// EnsureDaemonInstalled auto-installs it). A bare/dev build returns false and keeps
// using the terminal's own grants.
func runningFromBundle() bool { return bundlePath() != "" }

// autoInstallDaemon gates EnsureDaemonInstalled on macOS: install whenever we run
// from the reminal.app bundle (a real install), which is also the only build that
// can perform the sh.reminal-identity capture the daemon exists for. version is
// unused here — the bundle, not the stamp, is what marks a real install.
func autoInstallDaemon(version string) bool { return runningFromBundle() }

// restartService bounces the agent so it re-execs the binary at the plist's
// path. No-op when the service isn't installed.
func restartService(u *user.User) error {
	plistPath := filepath.Join(u.HomeDir, "Library", "LaunchAgents", daemonLabel+".plist")
	if _, err := os.Stat(plistPath); err != nil {
		return nil // not installed
	}
	uid, _ := strconv.Atoi(u.Uid)
	amRoot := os.Geteuid() == 0
	_ = launchctl(uid, amRoot, "kickstart", "-k", fmt.Sprintf("gui/%d/%s", uid, daemonLabel)).Run()
	return nil
}

// launchctl builds a launchctl command targeting uid's login domain. As the user
// we can talk to gui/<uid> directly; as root we must wrap the whole invocation in
// `launchctl asuser <uid> …` to enter the user's per-login launchd context.
func launchctl(uid int, amRoot bool, args ...string) *exec.Cmd {
	if amRoot {
		return exec.Command("launchctl", append([]string{"asuser", strconv.Itoa(uid), "launchctl"}, args...)...)
	}
	return exec.Command("launchctl", args...)
}

func firstNonEmpty(a, b []byte) string {
	if len(a) > 0 {
		return string(a)
	}
	return string(b)
}
