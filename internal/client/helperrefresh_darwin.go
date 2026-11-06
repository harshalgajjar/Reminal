// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build darwin

package client

import (
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// helperRefreshes reports whether the capture helper sends a static window's
// picture on its own: its once-a-second idle refresh needs macOS 14
// (SCScreenshotManager). Before that a static window sends nothing at all.
func helperRefreshes() bool {
	v, err := unix.Sysctl("kern.osproductversion")
	if err != nil {
		return false
	}
	major, err := strconv.Atoi(strings.SplitN(v, ".", 2)[0])
	return err == nil && major >= 14
}
