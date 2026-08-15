// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
)

// needsSudoRetry reports whether err is a permission failure writing the
// admin-owned owner store (/etc/reminal, %ProgramData%\reminal) while we're
// not elevated — i.e. the exact same command would succeed re-run with
// privileges. Owner mutations are atomic, so nothing was applied when the
// write was denied and re-running is safe.
func needsSudoRetry(err error) bool {
	return err != nil && errors.Is(err, os.ErrPermission) && os.Geteuid() != 0
}

// sudoReexec re-runs this exact reminal invocation under sudo, wiring the
// terminal through so sudo can prompt for a password interactively. The child
// does the work and prints its own output; we return its exit status, so the
// caller should return immediately after.
//
// Windows has no sudo-with-shared-stdio: UAC elevation spawns a NEW console
// the user can't see our prompts in. So instead of elevating silently, tell
// the user to re-run from an Administrator terminal.
func sudoReexec() error {
	if runtime.GOOS == "windows" {
		return errors.New("writing the machine owner store needs elevation — re-run this command from an Administrator terminal (right-click Terminal → Run as administrator)")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command("sudo", append([]string{exe}, os.Args[1:]...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}
