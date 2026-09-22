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

// The alert watcher reads CPU every couple of seconds. cpuPercent's `top`
// costs ~0.6s of CPU per call, which at that rate would itself be the load
// the alert warns about. `iostat -w 1` is one long-lived process that prints a
// fresh us/sy/id line every second for next to nothing, so the watcher reads
// the latest line instead.

var (
	cpuFeedOnce sync.Once
	cpuFeedMu   sync.Mutex
	cpuFeedVal  float64
	cpuFeedAt   time.Time
	cpuTopAt    time.Time // last fallback to top, to keep it rare
)

// pushCPU is the watcher's CPU reading: the stream's latest, or — while the
// stream is down — cpuPercent at most every pushTick.
func pushCPU() (float64, bool) {
	cpuFeedOnce.Do(func() { go runCPUFeed() })
	cpuFeedMu.Lock()
	defer cpuFeedMu.Unlock()
	if time.Since(cpuFeedAt) < 5*time.Second {
		return cpuFeedVal, true
	}
	if time.Since(cpuTopAt) < pushTick {
		return 0, false
	}
	cpuTopAt = time.Now()
	return cpuPercent()
}

func runCPUFeed() {
	for {
		cmd := exec.Command("/usr/sbin/iostat", "-n0", "-w", "1")
		out, err := cmd.StdoutPipe()
		if err == nil {
			err = cmd.Start()
		}
		if err == nil {
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
				cpuFeedMu.Lock()
				cpuFeedVal, cpuFeedAt = v, time.Now()
				cpuFeedMu.Unlock()
			}
			_ = cmd.Wait()
		}
		time.Sleep(30 * time.Second)
	}
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
