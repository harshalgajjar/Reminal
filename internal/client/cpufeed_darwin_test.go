// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build darwin

package client

import "testing"

func TestParseIostatCPU(t *testing.T) {
	for line, want := range map[string]float64{" 22 20 58  15.42 15.00 13.64": 42, "  0  0 100  1.00 1.00 1.00": 0} {
		if v, ok := parseIostatCPU(line); !ok || v != want {
			t.Errorf("%q → %v %v, want %v", line, v, ok, want)
		}
	}
	for _, line := range []string{"      cpu    load average", " us sy id   1m   5m   15m", ""} {
		if _, ok := parseIostatCPU(line); ok {
			t.Errorf("%q parsed as a reading", line)
		}
	}
}
