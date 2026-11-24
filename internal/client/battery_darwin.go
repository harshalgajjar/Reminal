// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build darwin

package client

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The battery on macOS is read by one sampler for the whole machine
// (machineshare.go), as the CPU is: every session is its own process, and
// each running its own pmset every batteryTTL was several a second on a
// machine with dozens of sessions. The one process that holds the lock runs
// pmset every batteryTTL while a reading is wanted, and the others read what
// it wrote.
var (
	machineBattery = &machineShare{
		name:      "battery",
		sample:    sampleBattery,
		fresh:     3 * batteryTTL,
		wantEvery: 5 * time.Second,
		wantFor:   30 * time.Second,
		tryEvery:  2 * time.Second,
	}
	machineBatteryDir sync.Once
)

func sampleBattery(emit func(string) bool, _ func() bool) {
	for emit(batteryWord(pmsetBattery())) {
		time.Sleep(batteryTTL)
	}
}

// readBattery is the machine's shared battery reading. The sampler's first
// reading comes at once (pmset takes a few milliseconds), so a process that
// finds none waits a moment for it; with none even then (reminal's folder
// cannot be written, say), it reads the battery itself.
func readBattery() *Battery {
	machineBatteryDir.Do(func() {
		if dir, err := reminalDir(); err == nil {
			machineBattery.dir = dir
		}
	})
	if machineBattery.dir != "" {
		if w, ok := machineBattery.readWithin(400 * time.Millisecond); ok {
			if b, ok := batteryFromWord(w); ok {
				return b
			}
		}
	}
	return pmsetBattery()
}

// readBatteryFresh is the battery read now, not the shared reading (which
// may be batteryTTL old): for the moment right after a plug or unplug.
func readBatteryFresh() *Battery { return pmsetBattery() }

// pmsetBattery parses `pmset -g batt`, which is the same source the menu bar
// uses and costs ~8ms. IOKit would avoid the fork but needs cgo.
//
// A machine with a battery prints a second line:
//
//	Now drawing from 'AC Power'
//	 -InternalBattery-0 (id=20512867)	100%; charged; 0:00 remaining present: true
//
// A desktop prints only the first line, so "no battery line" is exactly the
// signal we want and needs no special-casing.
func pmsetBattery() *Battery {
	// Bounded: a wedged pmset must not hold up a directory reply.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "pmset", "-g", "batt").Output()
	if err != nil {
		return nil
	}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, "InternalBattery") {
			continue
		}
		// Everything after the tab is "<pct>%; <state>; <h:mm> remaining ...".
		tail := line
		if i := strings.IndexByte(line, '\t'); i >= 0 {
			tail = line[i+1:]
		}
		parts := strings.Split(tail, ";")
		if len(parts) < 2 {
			return nil
		}
		pctStr := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(parts[0]), "%"))
		pct, err := strconv.Atoi(pctStr)
		if err != nil || pct < 0 || pct > 100 {
			return nil
		}
		b := &Battery{Pct: &pct, State: strings.TrimSpace(parts[1])}
		// pmset says "charged" only when full on AC; anything else it calls
		// "charging"/"discharging"/"finishing charge"/"AC attached". Fold the
		// long tail onto the three states the wire format defines.
		switch {
		case b.State == "charged":
		case strings.Contains(b.State, "discharging"):
			b.State = "discharging"
		case strings.Contains(b.State, "charging"), strings.Contains(b.State, "AC attached"):
			b.State = "charging"
		default:
			b.State = "charging"
		}
		if len(parts) >= 3 {
			b.Mins = parseHMMRemaining(parts[2])
		}
		return b
	}
	return nil
}

// parseHMMRemaining reads pmset's " 4:32 remaining present: true" into minutes.
// "(no estimate)" — which pmset prints for a while after any power-source
// change — yields 0, meaning "the OS declined to guess".
func parseHMMRemaining(s string) int {
	f := strings.Fields(strings.TrimSpace(s))
	if len(f) == 0 {
		return 0
	}
	h, m, ok := strings.Cut(f[0], ":")
	if !ok {
		return 0
	}
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if err1 != nil || err2 != nil || hh < 0 || mm < 0 {
		return 0
	}
	return hh*60 + mm
}
