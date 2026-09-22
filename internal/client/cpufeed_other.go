// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !darwin

package client

// pushCPU is the alert watcher's CPU reading. Elsewhere cpuPercent is a delta
// of kernel counters, cheap enough to read every couple of seconds.
func pushCPU() (float64, bool) { return cpuPercent() }
