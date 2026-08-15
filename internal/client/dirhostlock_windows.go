// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build windows

package client

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// Windows twin of the flock-based directory-host lock: LockFileEx gives the
// same process-tied exclusive semantics (the kernel releases the lock when the
// holder dies), and LOCKFILE_FAIL_IMMEDIATELY matches LOCK_NB.

func dirHostLockPath() (string, error) {
	dir, err := reminalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "dirhost.lock"), nil
}

func tryLockDirHost() (*os.File, bool) {
	path, err := dirHostLockPath()
	if err != nil {
		return nil, false
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, false
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false
	}
	ol := new(windows.Overlapped)
	if err := windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, ol); err != nil {
		_ = f.Close()
		return nil, false
	}
	return f, true
}

func unlockDirHost(f *os.File) {
	if f == nil {
		return
	}
	ol := new(windows.Overlapped)
	_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, ol)
	_ = f.Close()
}
