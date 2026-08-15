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

// runningFromBundle is always false off macOS — the reminal.app daemon model is
// darwin-only, so EnsureDaemonInstalled never auto-installs here.
func runningFromBundle() bool { return false }
