// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !darwin

package client

import (
	"strconv"
	"time"
)

// powerPoll is how often power is re-read where the OS has no change stream
// we can use without cgo. The reads are a few sysfs files (Linux) or one
// syscall (Windows), so this is cheap enough to do often.
const powerPoll = 2 * time.Second

// watchPowerChanges pokes kick when the power state changes: on AC or not,
// and the charge percent.
func watchPowerChanges(stop <-chan struct{}, kick chan<- struct{}) {
	if readBattery() == nil {
		return // no battery, nothing will ever change
	}
	last := ""
	t := time.NewTicker(powerPoll)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		key := ""
		if b := readBattery(); b != nil {
			key = b.State
			if b.Pct != nil {
				key += strconv.Itoa(*b.Pct)
			}
		}
		if key != last && last != "" {
			select {
			case kick <- struct{}{}:
			default:
			}
		}
		last = key
	}
}
