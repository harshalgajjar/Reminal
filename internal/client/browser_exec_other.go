// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !windows

package client

// browserExecPath is the program a process runs, or "".
func browserExecPath(pid int) string { return processExecPath(pid) }
