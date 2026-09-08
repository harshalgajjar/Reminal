// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !windows

package client

import (
	"os"
	"path/filepath"
	"syscall"
)

// A single-holder lock file under ~/.reminal, used wherever exactly one process
// on this machine may be doing something at a time.
//
// flock ties the claim to the holder's process: if it crashes — or replaces
// itself, which is how an upgrade ends — the kernel releases the lock and the
// next contender takes over. That is the property both callers depend on, so
// they share one implementation rather than each growing their own.
// (Windows uses LockFileEx for the same semantics — see filelock_windows.go.)

func lockFilePath(name string) (string, error) {
	dir, err := reminalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

// tryLockFile claims name WITHOUT blocking. On success it returns the held
// file — keep it open for the lock's lifetime and release it with unlockFile.
//
// held is false when another process already holds the lock. err is non-nil
// only when the lock could not be attempted at all (an unwritable home, say),
// which is a different situation: callers that must still act when locking is
// impossible have to be able to tell the two apart.
func tryLockFile(name string) (f *os.File, held bool, err error) {
	path, err := lockFilePath(name)
	if err != nil {
		return nil, false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, false, err
	}
	f, err = os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	// LOCK_NB → return immediately with EWOULDBLOCK if a sibling holds it,
	// rather than blocking this goroutine.
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, false, nil // held by someone else — not a failure
	}
	return f, true, nil
}

// unlockFile releases the lock (closing the fd also drops it, but unlock
// explicitly so takeover doesn't wait on GC finalizing the file).
func unlockFile(f *os.File) {
	if f == nil {
		return
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	_ = f.Close()
}
