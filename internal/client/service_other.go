// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !darwin && !linux && !windows

package client

import (
	"errors"
	"os/user"
)

// No login-service integration on this platform yet — the background host can
// still be run in the foreground with `reminal daemon`.

func installService(exe string, u *user.User) error {
	return errors.New("background host auto-start isn't supported on this OS yet")
}

func uninstallService(u *user.User) error { return nil }

func restartService(u *user.User) error { return nil }

func serviceInstalled(u *user.User) bool { return false }

// autoInstallDaemon is always false here: this platform has no login-service
// integration (installService returns an error), so nothing to auto-install. The
// reminal.app bundle model is darwin-only, so there is no runningFromBundle here.
func autoInstallDaemon(version string) bool { return false }
