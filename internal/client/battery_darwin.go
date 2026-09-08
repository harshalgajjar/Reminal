// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build darwin

package client

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// readBattery parses `pmset -g batt`, which is the same source the menu bar
// uses and costs ~8ms. IOKit would avoid the fork but needs cgo, and this runs
// at most once per batteryTTL.
//
// A machine with a battery prints a second line:
//
//	Now drawing from 'AC Power'
//	 -InternalBattery-0 (id=20512867)	100%; charged; 0:00 remaining present: true
//
// A desktop prints only the first line, so "no battery line" is exactly the
// signal we want and needs no special-casing.
func readBattery() *Battery {
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
