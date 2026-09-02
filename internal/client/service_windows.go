// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build windows

package client

import (
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows/registry"

	"github.com/reminal/reminal/internal/proc"
)

// Windows background host: a per-user Run-key entry that launches
// `reminal daemon` at logon. Windows has no per-user service manager in the
// launchd/systemd sense (real Services need admin and run in session 0, cut off
// from the user's profile and keys), so the HKCU Run key is the idiomatic
// "start this for me at login" mechanism. It has no keep-alive, and it only
// fires at the NEXT logon — so install also starts the daemon immediately,
// detached, and liveness is tracked through the daemon's own pid file.
//
// Explorer (Win10 1703+) also gates Run-key apps through StartupApproved\Run.
// Writing only the Run value leaves reminal invisible to Task Manager → Startup
// and skipped at logon — which is why a reboot left this machine offline until
// `reminal new` spawned the daemon itself. install writes both.
//
// The sudo/target-user dance the other platforms do doesn't apply here: there's
// no SUDO_USER model on Windows and HKCU is by definition the current user's
// hive, so u is accepted for signature parity but the registry write always
// lands on whoever is running us.

const (
	runKeyPath   = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValueName = "reminal-daemon"
	// startupApprovedPath is Explorer's enable/disable list for HKCU Run values.
	// Same value name as the Run key. Missing = Explorer often skips the launch.
	startupApprovedPath = `Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run`
)

// filetimeEpochOffset is 100-ns ticks between 1601-01-01 and the Unix epoch.
const filetimeEpochOffset = 116444736000000000

// startupApprovedEnabled is the 12-byte REG_BINARY Explorer treats as "enabled":
// 02 00 00 00 + FILETIME of when we approved it. 03 00 00 00… is user-disabled.
func startupApprovedEnabled() []byte {
	b := make([]byte, 12)
	b[0] = 0x02
	ft := uint64(time.Now().UTC().UnixNano()/100 + filetimeEpochOffset)
	binary.LittleEndian.PutUint64(b[4:], ft)
	return b
}

func approveLogonStartup() error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, startupApprovedPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetBinaryValue(runValueName, startupApprovedEnabled())
}

func revokeLogonStartup() {
	k, err := registry.OpenKey(registry.CURRENT_USER, startupApprovedPath, registry.SET_VALUE)
	if err != nil {
		return
	}
	defer k.Close()
	_ = k.DeleteValue(runValueName)
}

// installService writes the Run-key entry (and Explorer's StartupApproved
// enable-bit) and starts the daemon now (the key alone would only take effect
// at next logon). Idempotent: re-writing the values is harmless and an
// already-live daemon (per its pid file) isn't double-spawned.
func installService(exe string, u *user.User) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	// Quote the path: Run-key values are parsed like command lines, and an
	// unquoted `C:\Program Files\…` splits at the space.
	if err := k.SetStringValue(runValueName, `"`+exe+`" daemon`); err != nil {
		return err
	}
	if err := approveLogonStartup(); err != nil {
		return err
	}

	if pid, ok := daemonPID(); ok && proc.Alive(pid) {
		return nil // already running; the Run key covers future logons
	}
	return spawnDetachedDaemon(exe)
}

// uninstallService deletes the Run-key entry (missing = already uninstalled)
// and best-effort stops the running daemon via its pid file.
func uninstallService(u *user.User) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err == nil {
		derr := k.DeleteValue(runValueName)
		k.Close()
		if derr != nil && !errors.Is(derr, registry.ErrNotExist) {
			return derr
		}
	} else if !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	revokeLogonStartup()

	// No service manager to stop it for us: kill the pid the daemon recorded.
	if pid, ok := daemonPID(); ok {
		_ = proc.Kill(pid)
	}
	if p, err := daemonPIDPath(); err == nil {
		_ = os.Remove(p)
	}
	return nil
}

// serviceInstalled reports whether the Run-key entry exists.
func serviceInstalled(u *user.User) bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(runValueName)
	return err == nil
}

// runningFromBundle is false on Windows: there's no reminal.app, so
// EnsureDaemonInstalled's bundle-implies-daemon rule never applies here.
func runningFromBundle() bool { return false }

// restartService kills the pid-file daemon (best-effort) and spawns a fresh one
// so it re-execs a newly-installed binary. The Run key has no keep-alive, so
// unlike launchd/systemd we must do the respawn ourselves. The exe comes from
// the installed Run-key value — that's the path a logon would launch — falling
// back to the current binary if the value doesn't parse. No-op (nil) when the
// service isn't installed.
func restartService(u *user.User) error {
	if !serviceInstalled(u) {
		return nil
	}
	exe := runKeyExe()
	if exe == "" {
		var err error
		if exe, err = os.Executable(); err != nil {
			return err
		}
	}
	if pid, ok := daemonPID(); ok {
		_ = proc.Kill(pid)
	}
	// Heal machines that got the Run key from an older reminal but never
	// an Explorer approval — otherwise the next logon skips the daemon again.
	_ = approveLogonStartup()
	return spawnDetachedDaemon(exe)
}

// spawnDetachedDaemon starts `exe daemon` fully detached: no console window, its
// own process group (console Ctrl+C never reaches it), stdio on NUL, and the
// handle released so we never hold a reference to it. The daemon writes its own
// pid file once up — we don't record anything here.
func spawnDetachedDaemon(exe string) error {
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer devnull.Close()

	cmd := exec.Command(exe, "daemon")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = devnull, devnull, devnull
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow: true,
		// detachedProcess (see spawnutil_windows.go) severs the console; the new
		// process group keeps a console Ctrl+C from ever reaching the daemon.
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | detachedProcess,
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// daemonPID reads the recorded daemon pid. (0, false) when the file is missing
// or malformed — callers then treat the daemon as not running.
func daemonPID() (int, bool) {
	p, err := daemonPIDPath()
	if err != nil {
		return 0, false
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// runKeyExe extracts the executable path from the installed Run-key value —
// `"C:\path\reminal.exe" daemon` — or "" if the value is missing or doesn't
// look like our quoted form.
func runKeyExe() string {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	val, _, err := k.GetStringValue(runValueName)
	if err != nil || !strings.HasPrefix(val, `"`) {
		return ""
	}
	if end := strings.Index(val[1:], `"`); end >= 0 {
		return val[1 : 1+end]
	}
	return ""
}
