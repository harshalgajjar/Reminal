// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !darwin

package client

// readBatteryFresh is the battery read now. Reading it is one cheap call
// here, so it is readBattery (macOS shares its reading: battery_darwin.go).
func readBatteryFresh() *Battery { return readBattery() }
