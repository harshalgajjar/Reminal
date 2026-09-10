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
)

// Linux background host: a systemd --user unit that runs `reminal daemon`. Linger
// is enabled so it survives with no active login session — essential on a
// headless owned server. When we're root (a sudo'd enroll) we drive the target
// user's manager via `sudo -u <user>` with their XDG_RUNTIME_DIR; as the user we
// call systemctl --user directly.

func installService(exe string, u *user.User) error {
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	amRoot := os.Geteuid() == 0

	configDir := filepath.Join(u.HomeDir, ".config")
	unitDir := filepath.Join(configDir, "systemd", "user")
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		return err
	}
	if amRoot {
		// MkdirAll ran as root, so any levels it just created are root-owned;
		// hand the whole ~/.config/systemd/user chain back to the user (a no-op
		// when a level already belonged to them).
		for _, d := range []string{configDir, filepath.Join(configDir, "systemd"), unitDir} {
			_ = os.Chown(d, uid, gid)
		}
	}
	unitPath := filepath.Join(unitDir, daemonUnit)
	unit := systemdUnit(exe)
	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		return err
	}
	if amRoot {
		_ = os.Chown(unitPath, uid, gid)
	}

	// Linger lets the user manager (and the daemon) run without an active login.
	_ = exec.Command("loginctl", "enable-linger", u.Username).Run()

	if out, err := userSystemctl(u, amRoot, "daemon-reload").CombinedOutput(); err != nil {
		// Non-fatal: the unit is written and will load at next login even if we
		// can't reach the manager right now.
		_ = out
	}
	if out, err := userSystemctl(u, amRoot, "enable", "--now", daemonUnit).CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl --user enable --now: %v: %s", err, out)
	}
	return nil
}

func uninstallService(u *user.User) error {
	amRoot := os.Geteuid() == 0
	unitPath := filepath.Join(u.HomeDir, ".config", "systemd", "user", daemonUnit)
	_ = userSystemctl(u, amRoot, "disable", "--now", daemonUnit).Run()
	if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	_ = userSystemctl(u, amRoot, "daemon-reload").Run()
	return nil
}

// serviceInstalled reports whether the systemd --user unit exists for u.
func serviceInstalled(u *user.User) bool {
	_, err := os.Stat(filepath.Join(u.HomeDir, ".config", "systemd", "user", daemonUnit))
	return err == nil
}

// autoInstallDaemon gates EnsureDaemonInstalled on Linux: install the always-on
// presence + stats daemon for any real release, independent of ownership, so a
// machine reports its vitals from boot. Only an un-stamped "dev" build is held
// back, so development never leaves a stray systemd --user unit behind.
func autoInstallDaemon(version string) bool { return isReleaseBuild(version) }

// restartService bounces the unit so it re-execs the binary at ExecStart's path.
// No-op when the service isn't installed.
func restartService(u *user.User) error {
	unitFile := filepath.Join(u.HomeDir, ".config", "systemd", "user", daemonUnit)
	if _, err := os.Stat(unitFile); err != nil {
		return nil // not installed
	}
	amRoot := os.Geteuid() == 0
	_ = userSystemctl(u, amRoot, "restart", daemonUnit).Run()
	return nil
}

// userSystemctl builds a `systemctl --user …` command for u's manager. As the
// user, call it directly (the session env is already set). As root, hop into the
// user with sudo + `env` to set XDG_RUNTIME_DIR to their runtime dir so `--user`
// resolves their manager. Note the explicit `env`: `sudo -u u VAR=val cmd` treats
// VAR=val as an argument to cmd (sudo won't set it unless sudoers permits
// setenv), so we set it with env instead.
func userSystemctl(u *user.User, amRoot bool, args ...string) *exec.Cmd {
	full := append([]string{"--user"}, args...)
	if amRoot {
		env := "XDG_RUNTIME_DIR=/run/user/" + u.Uid
		return exec.Command("sudo", append([]string{"-u", u.Username, "env", env, "systemctl"}, full...)...)
	}
	return exec.Command("systemctl", full...)
}
