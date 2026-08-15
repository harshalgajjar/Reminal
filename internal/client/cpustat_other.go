// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !darwin && !linux && !windows

package client

// cpuPercent is unavailable on platforms without a sampler (e.g. Windows); the
// viewer falls back to a load-based indicator when utilization is unknown.
func cpuPercent() (float64, bool) { return 0, false }
