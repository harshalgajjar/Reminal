// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import "os/exec"

// asSessionUser: sessions on Windows run as whoever starts them (no machine
// names a session user there).
func asSessionUser(*exec.Cmd) error { return nil }
