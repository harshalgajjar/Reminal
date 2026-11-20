// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !linux && !darwin

package client

import (
	"errors"
	"net"
)

// peerPID: not read on this platform yet, so no request is taken here (the
// hub is not served on Windows in this first version).
func peerPID(net.Conn) (int, error) {
	return 0, errors.New("not supported on this platform yet")
}
