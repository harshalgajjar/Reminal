// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !darwin

package updater

// sameSigner has nothing to compare off macOS: Linux builds are unsigned, and
// what a Windows grant hangs on is not a code identity. What stands in for it
// there is the rest of a switch's checks — an owner's signature over the
// manifest and channel, the digest the manifest publishes, and the build
// saying, when run, which channel it follows.
func sameSigner(string) error { return nil }
