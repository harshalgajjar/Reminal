// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"errors"
	"os"
)

// No Xvfb here: nothing is ever trusted to serve frames (xvfbfb.go).

func ownerOf(os.FileInfo) (uint32, bool) { return 0, false }

func trustedUID(uint32) bool { return false }

func openFramebuffer(string, uint32) (*os.File, os.FileInfo, error) {
	return nil, nil, errors.New("no X server framebuffers on this OS")
}
