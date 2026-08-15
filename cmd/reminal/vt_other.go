// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !windows

package main

// enableVTConsole is a Windows-only concern (legacy conhost ships with ANSI
// processing off); Unix terminals speak VT natively.
func enableVTConsole() {}
