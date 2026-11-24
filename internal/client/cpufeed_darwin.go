// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build darwin

package client

import (
	"bufio"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The CPU on macOS is read by one sampler for the whole machine (machineshare.go):
// `iostat -w 1`, one long-lived process that prints a fresh us/sy/id line
// every second for next to nothing. `top -l1` (topCPUPercent) costs about
// 0.6 s of CPU a call, so it is only what the sampler falls back to when
// iostat cannot run, and then at most once every cpuTopEvery: one process on
// the machine, never one per session.

const cpuTopEvery = 10 * time.Second

var machineCPU = &machineShare{
	name: "cpu",
	sample: func(emit func(string) bool, wanted func() bool) {
		sampleCPU(func(pct float64) bool { return emit(strconv.FormatFloat(pct, 'f', 1, 64)) }, wanted)
	},
	fresh:     5 * time.Second,
	wantEvery: 5 * time.Second,
	wantFor:   30 * time.Second,
	tryEvery:  2 * time.Second,
}

var machineCPUDir sync.Once

// cpuPercent is the machine's CPU utilization (0..100): the shared reading.
// Unknown until the sampler's first reading is in (about a second after the
// first process asks), and a caller keeps its last value meanwhile.
func cpuPercent() (float64, bool) {
	machineCPUDir.Do(func() {
		if dir, err := reminalDir(); err == nil {
			machineCPU.dir = dir
		}
	})
	if machineCPU.dir == "" {
		return 0, false
	}
	w, ok := machineCPU.read()
	if !ok {
		return 0, false
	}
	pct, err := strconv.ParseFloat(w, 64)
	if err != nil || pct < 0 || pct > 100 {
		return 0, false
	}
	return pct, true
}

// pushCPU is the alert watcher's CPU reading: the same shared one.
func pushCPU() (float64, bool) { return cpuPercent() }

// sampleCPU is the machine's sampler, run by the one process that holds the
// lock: iostat's readings while it runs, and while it cannot, one top every
// cpuTopEvery before iostat is tried again.
func sampleCPU(emit func(float64) bool, wanted func() bool) {
	for wanted() {
		if !iostatCPU(emit) {
			return
		}
		if v, ok := topCPUPercent(); ok && !emit(v) {
			return
		}
		time.Sleep(cpuTopEvery)
	}
}

// iostatCPU emits iostat's reading each second until emit answers false
// (then it answers false too), or iostat ends or cannot be started (true:
// the caller falls back).
func iostatCPU(emit func(float64) bool) (more bool) {
	cmd := exec.Command("/usr/sbin/iostat", "-n0", "-w", "1")
	out, err := cmd.StdoutPipe()
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		return true
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	sc := bufio.NewScanner(out)
	first := true
	for sc.Scan() {
		v, ok := parseIostatCPU(sc.Text())
		if !ok {
			continue
		}
		// The first line is the average since boot, not now.
		if first {
			first = false
			continue
		}
		if !emit(v) {
			return false
		}
	}
	return true
}

// parseIostatCPU reads " 22 20 58  15.42 15.00 13.64" (us sy id, load avgs)
// as 100 − idle. Header lines do not parse and are skipped.
func parseIostatCPU(line string) (float64, bool) {
	f := strings.Fields(line)
	if len(f) < 3 {
		return 0, false
	}
	idle, err := strconv.Atoi(f[2])
	if err != nil || idle < 0 || idle > 100 {
		return 0, false
	}
	if _, err := strconv.Atoi(f[0]); err != nil {
		return 0, false
	}
	return float64(100 - idle), true
}
