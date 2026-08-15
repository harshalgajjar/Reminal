// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !windows

package client

// writeClipboardNative has no non-Windows implementation — macOS/Linux go
// through pbcopy/xclip subprocesses in agent.go, so callers fall through.
func writeClipboardNative(text string) bool { return false }
