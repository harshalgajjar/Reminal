// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package pty

import (
	"os"
	"strings"
)

// shellEnv builds the inner shell's environment. It forces TERM and DISABLES
// macOS Terminal's shell session save/restore: a reminal shell isn't a
// Terminal.app window, but it inherits Terminal's SHELL_SESSION_FILE, so every
// spawned shell would source (and re-save) the same session file — which
// corrupts it and surfaces as "…/.zsh_sessions/….session: command not found:
// Saving" at the top of a new session. SHELL_SESSIONS_DISABLE=1 is Apple's
// documented off-switch; it's a harmless no-op on Linux and Windows.
func shellEnv(extra []string) []string {
	base := os.Environ()
	out := make([]string, 0, len(base)+len(extra)+2)
	for _, kv := range base {
		if strings.HasPrefix(kv, "SHELL_SESSION_FILE=") || strings.HasPrefix(kv, "SHELL_SESSIONS_DISABLE=") {
			continue // drop the inherited pointer + any prior flag; set a clean one below
		}
		out = append(out, kv)
	}
	out = append(out, "TERM=xterm-256color", "SHELL_SESSIONS_DISABLE=1")
	return append(out, extra...)
}
