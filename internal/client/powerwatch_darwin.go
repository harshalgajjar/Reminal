// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build darwin

package client

import (
	"bufio"
	"os/exec"
	"strings"
	"time"
)

// watchPowerChanges pokes kick the moment macOS reports a power change: a
// charger plugged or pulled, the battery ticking a percent, or the machine
// waking from sleep (a charger pulled while the lid was shut is news the
// moment it opens). `pmset -g pslog` is the OS's own notification stream —
// it prints only when something changes — so this costs one idle child
// process instead of a fork every few seconds, and needs no cgo for IOKit.
func watchPowerChanges(stop <-chan struct{}, kick chan<- struct{}) {
	for {
		cmd := exec.Command("pmset", "-g", "pslog")
		out, err := cmd.StdoutPipe()
		if err == nil {
			err = cmd.Start()
		}
		if err == nil {
			done := make(chan struct{})
			go func() {
				select {
				case <-stop:
					_ = cmd.Process.Kill()
				case <-done:
				}
			}()
			sc := bufio.NewScanner(out)
			for sc.Scan() {
				if l := sc.Text(); strings.Contains(l, "InternalBattery") || strings.Contains(l, "Wake") {
					select {
					case kick <- struct{}{}:
					default:
					}
				}
			}
			_ = cmd.Wait()
			close(done)
		}
		// pmset exited (or never started): the watcher's tick still covers
		// power, just slower, until it comes back.
		select {
		case <-stop:
			return
		case <-time.After(30 * time.Second):
		}
	}
}
