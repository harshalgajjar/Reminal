// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build darwin

package client

import (
	"os/exec"
	"regexp"
	"strconv"
)

// idleRe pulls the idle percentage out of top's summary line:
//
//	CPU usage: 11.44% user, 7.39% sys, 81.17% idle
var idleRe = regexp.MustCompile(`([0-9.]+)% idle`)

// topCPUPercent is one CPU reading (0..100) from `top`. There's no sysctl for
// aggregate CPU ticks on macOS (kern.cp_time doesn't exist here) and the true
// source — mach host_statistics — needs cgo, which reminal's static
// CGO_ENABLED=0 build can't use. `top -l1 -n0` prints one current reading
// (verified to track live load, not a since-boot average) and skips the
// process list, but costs about 0.6 s of CPU: it is never the usual way to
// read the CPU (cpuPercent, cpufeed_darwin.go), only the machine's one
// sampler's fallback. Utilization = 100 − idle. Any failure → unknown.
func topCPUPercent() (float64, bool) {
	out, err := exec.Command("/usr/bin/top", "-l1", "-n0").Output()
	if err != nil {
		return 0, false
	}
	m := idleRe.FindSubmatch(out)
	if m == nil {
		return 0, false
	}
	idle, err := strconv.ParseFloat(string(m[1]), 64)
	if err != nil {
		return 0, false
	}
	busy := 100 - idle
	if busy < 0 {
		busy = 0
	}
	if busy > 100 {
		busy = 100
	}
	return busy, true
}
