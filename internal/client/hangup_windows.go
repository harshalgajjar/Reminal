// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build windows

package client

import "os"

// termHost: a console closing reaches a Windows session another way, so its
// host is not watched (see hangup_unix.go).
type termHost struct{}

func findTermHost() termHost        { return termHost{} }
func (termHost) alive() bool        { return false }
func notifyHangup(chan os.Signal)   {}
func isHangupSignal(os.Signal) bool { return false }
