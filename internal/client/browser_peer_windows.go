// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build windows

package client

import (
	"errors"
	"net"
	"unsafe"

	"golang.org/x/sys/windows"
)

// sioAFUnixGetPeerPID asks a unix socket for the process at its other end
// (afunix.h: _WSAIOR(IOC_VENDOR, 256); Windows 10 1803 and later).
const sioAFUnixGetPeerPID = 0x58000100

// peerPID is the process at the other end of a unix socket
// (SIO_AF_UNIX_GETPEERPID). A Windows that cannot say gives an error, and
// no request is taken from a caller that cannot be told.
func peerPID(c net.Conn) (int, error) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return 0, errors.New("not a unix socket")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, err
	}
	var pid, n uint32
	var serr error
	if err := raw.Control(func(fd uintptr) {
		serr = windows.WSAIoctl(windows.Handle(fd), sioAFUnixGetPeerPID, nil, 0, (*byte)(unsafe.Pointer(&pid)), uint32(unsafe.Sizeof(pid)), &n, nil, 0)
	}); err != nil {
		return 0, err
	}
	if serr != nil {
		return 0, serr
	}
	// (Windows fills the pid in and reports no bytes written, so the count
	// is not what says whether it answered: the pid is.)
	if pid == 0 {
		return 0, errors.New("the socket did not say which process is at its other end")
	}
	return int(pid), nil
}
