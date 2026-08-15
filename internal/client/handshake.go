// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"
)

// handshakeWriter opens the child's channel back to the parent that spawned it
// detached: the inherited pipe fd on Unix (`--handshake-fd`), or a dial to the
// parent's loopback listener on Windows (`--handshake-addr`, authenticated by
// the REMINAL_HS_TOKEN preamble — see spawnutil_windows.go). The caller writes
// exactly one JSON line and closes.
func handshakeWriter(fd int, addr string) (io.WriteCloser, error) {
	if addr != "" {
		conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
		if err != nil {
			return nil, fmt.Errorf("dial handshake %s: %w", addr, err)
		}
		if _, err := io.WriteString(conn, "tok "+os.Getenv("REMINAL_HS_TOKEN")+"\n"); err != nil {
			_ = conn.Close()
			return nil, err
		}
		return conn, nil
	}
	if fd > 0 {
		f := os.NewFile(uintptr(fd), "handshake")
		if f == nil {
			return nil, fmt.Errorf("invalid handshake fd %d", fd)
		}
		return f, nil
	}
	return nil, errors.New("no handshake channel")
}

// ParseHandshakeAddr returns the value of --handshake-addr from args, or "".
// Windows counterpart of ParseHandshakeFD.
func ParseHandshakeAddr(args []string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == "--handshake-addr" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}
