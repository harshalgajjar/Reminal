// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !windows

package client

// newNativeWindowBackend returns the build-tagged native backend for this OS,
// or nil to fall through to newWindowBackend's exec-based backends. Only
// Windows has one today (Win32 needs syscalls that must stay out of other
// builds); macOS/Linux shell out and stay untagged.
func newNativeWindowBackend() windowBackend { return nil }
