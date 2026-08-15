// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !windows

package client

import (
	"os"
	"path/filepath"
	"syscall"
)

// The directory-host lock ensures exactly ONE agent on this machine serves the
// owner directory channel at a time. Without it every session's directory host
// races for the same relay channel — and because the production relay
// *supersedes* an agent that reconnects with a matching credential (while the
// local relay *rejects* it), sibling sessions would ping-pong the channel on
// every reclaim, so the machine flaps online/offline and in-flight owner
// handshakes/spawns get dropped. flock ties the lock to the holder's process:
// if it crashes, the kernel releases it and another agent takes over.
// (Windows uses LockFileEx for the same semantics — see dirhostlock_windows.go.)

func dirHostLockPath() (string, error) {
	dir, err := reminalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "dirhost.lock"), nil
}

// tryLockDirHost attempts to become this machine's sole directory host WITHOUT
// blocking. On success it returns the held lock file — keep it open for the
// lock's lifetime and release it with unlockDirHost. Returns (nil, false) when
// another agent already holds the lock.
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
	// LOCK_NB → return immediately with EWOULDBLOCK if a sibling holds it,
	// rather than blocking this goroutine.
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, false
	}
	return f, true
}

// unlockDirHost releases the directory-host lock (closing the fd also drops it,
// but unlock explicitly so takeover doesn't wait on GC finalizing the file).
func unlockDirHost(f *os.File) {
	if f == nil {
		return
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	_ = f.Close()
}
