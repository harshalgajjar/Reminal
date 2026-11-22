// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build windows

package main

import (
	"strings"

	"golang.org/x/sys/windows"
)

// browserRealPath is the name Windows itself has for the file p opens: its
// long name, on its own volume, with every link and junction on the way
// followed, whatever name it was asked for by (a short 8.3 name, a dot or a
// space after it, a \\?\ path). The file is opened to ask, so the answer is
// about the file that would be read.
func browserRealPath(p string) (string, error) {
	name, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return "", err
	}
	// No access asked for (only its name is read); a folder can be opened
	// this way too, and is turned down later as not a file.
	h, err := windows.CreateFile(name, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, 1024)
	for {
		n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), 0)
		if err != nil {
			return "", err
		}
		if int(n) < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]uint16, n+1)
	}
	real := windows.UTF16ToString(buf)
	switch {
	case strings.HasPrefix(real, `\\?\UNC\`):
		real = `\\` + real[len(`\\?\UNC\`):]
	case strings.HasPrefix(real, `\\?\`):
		real = real[len(`\\?\`):]
	}
	return real, nil
}

// browserNetworkDrive: p is on a drive letter that is a share on another
// computer (Z: mapped to \\server\share). Asked of Windows' own table of
// drives: the other computer is not reached to answer.
func browserNetworkDrive(p string) bool {
	p = strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(p, `\\?\`), `\\.\`), `\??\`)
	if len(p) < 2 || p[1] != ':' {
		return false
	}
	root, err := windows.UTF16PtrFromString(p[:2] + `\`)
	if err != nil {
		return false
	}
	return windows.GetDriveType(root) == windows.DRIVE_REMOTE
}
