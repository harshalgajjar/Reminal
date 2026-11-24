// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"strconv"
	"strings"
)

// A battery reading as one word, the form it is shared in between the
// reminal processes of a machine (machineshare.go): "85,charging,123" (per
// cent, state, minutes), or "none" for a machine with no battery, which is a
// reading too.
const batteryNone = "none"

func batteryWord(b *Battery) string {
	if b == nil || b.Pct == nil {
		return batteryNone
	}
	return strconv.Itoa(*b.Pct) + "," + b.State + "," + strconv.Itoa(b.Mins)
}

// batteryFromWord reads a word back. ok is false for anything that is not a
// reading (another version's, a damaged file): the caller reads the battery
// itself then.
func batteryFromWord(w string) (b *Battery, ok bool) {
	if w == batteryNone {
		return nil, true
	}
	f := strings.Split(w, ",")
	if len(f) != 3 {
		return nil, false
	}
	pct, err := strconv.Atoi(f[0])
	mins, err2 := strconv.Atoi(f[2])
	if err != nil || err2 != nil || pct < 0 || pct > 100 || mins < 0 {
		return nil, false
	}
	switch f[1] {
	case "charging", "discharging", "charged":
	default:
		return nil, false
	}
	return &Battery{Pct: &pct, State: f[1], Mins: mins}, true
}
