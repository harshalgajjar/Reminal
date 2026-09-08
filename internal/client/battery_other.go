// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !darwin && !linux && !windows

package client

// readBattery has no implementation on platforms without a power API. Nil
// means "no battery to show", which is the same thing a desktop reports.
func readBattery() *Battery { return nil }
