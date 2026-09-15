// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build windows

package main

// execSelf is deliberately a no-op on Windows: there is no exec that replaces a
// process in place, and spawning a replacement to inherit the client's pipes
// while this one exits reads as a crashed server to the client — which may then
// never restart it, losing the tools entirely.
//
// Windows therefore degrades to the staleness warning plus "pass the parameter
// anyway", which still works whenever the running image supports the parameter.
func execSelf(exe string, argv, env []string) {}
