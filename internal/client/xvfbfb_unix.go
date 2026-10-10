// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !windows

package client

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// ownerOf is the uid that owns fi.
func ownerOf(fi os.FileInfo) (uint32, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return st.Uid, true
}

// trustedUID: a process (or file) of root's, or of this process's own user,
// may serve frames to it — never another user's, who could set its argv to
// anything (a fake "Xvfb ... -fbdir") and have this process capture their
// pixels.
func trustedUID(uid uint32) bool { return uid == 0 || uid == uint32(os.Geteuid()) }

// openFramebuffer opens an Xvfb's framebuffer file for reading, refusing
// anything but a regular file owned by owner and written by nobody else: not
// a link (O_NOFOLLOW), and never blocking on a FIFO put in its place
// (O_NONBLOCK, then the regular-file check).
func openFramebuffer(path string, owner uint32) (*os.File, os.FileInfo, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	f := os.NewFile(uintptr(fd), path)
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	uid, ok := ownerOf(fi)
	switch {
	case !fi.Mode().IsRegular():
		err = errors.New("not a regular file")
	case !ok || uid != owner:
		err = fmt.Errorf("owned by uid %d, not its X server's (%d)", uid, owner)
	case fi.Mode().Perm()&0o022 != 0:
		err = fmt.Errorf("writable by others (%v)", fi.Mode().Perm())
	}
	if err != nil {
		f.Close()
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	return f, fi, nil
}
