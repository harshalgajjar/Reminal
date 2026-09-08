// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build linux

package client

// readBattery reads sysfs, the interface every Linux power daemon reads. No
// shell-out and no D-Bus dependency, so it behaves the same on a headless
// server — which simply has no Battery entry and reports nothing — as on a
// laptop. The parsing lives in battery_sysfs.go so it is testable off-Linux.
func readBattery() *Battery { return readBatterySysfs(sysfsPowerRoot) }
