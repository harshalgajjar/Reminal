// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// enableVTConsole switches the attached console to VT (ANSI) processing so
// escape-sequence output — the viewer's remote screen, colours, the QR code —
// renders instead of printing raw. Windows Terminal has this on by default;
// legacy conhost doesn't, and enabling it there is harmless when already on.
// Best-effort: a redirected/absent console just leaves everything as-is.
func enableVTConsole() {
	for _, f := range []*os.File{os.Stdout, os.Stderr} {
		h := windows.Handle(f.Fd())
		var mode uint32
		if err := windows.GetConsoleMode(h, &mode); err != nil {
			continue
		}
		_ = windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING)
	}
}
