// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !windows

package main

import "syscall"

// execSelf replaces this process image in place. File descriptors survive exec,
// so the client's stdio pipes stay connected and the new image keeps serving the
// same session without the client noticing anything happened.
//
// Returns only if the exec failed; the caller then stays on the old image.
func execSelf(exe string, argv, env []string) {
	_ = syscall.Exec(exe, argv, env)
}
