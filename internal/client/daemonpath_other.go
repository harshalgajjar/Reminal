// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !darwin

package client

// daemonBounceApplies is macOS-only for now: elsewhere the service manager
// (systemd Restart=always, the Windows Run key) owns the daemon's lifecycle
// and there is no equivalent of the app-bundle ambiguity this guards against.
// Returning false means the daemon picks the new binary up via
// watchBinaryAndExit, exactly as it did before this feature existed.
func daemonBounceApplies() bool { return false }
