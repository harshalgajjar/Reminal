// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"os"
	"syscall"
	"time"
)

// sizeReader reads a terminal's size: columns, rows.
type sizeReader func() (int, int, error)

// firstWorking is the first reader that can read a size now, or nil. On
// Windows only a console's output side has a size: asked of its input, the
// call fails every time, and a watcher that asked it never saw a resize.
func firstWorking(readers ...sizeReader) sizeReader {
	for _, r := range readers {
		if r == nil {
			continue
		}
		if _, _, err := r(); err == nil {
			return r
		}
	}
	return nil
}

// pollSize delivers a tick whenever read's size changes, sampling it every
// interval. The tick is a dummy signal: consumers use the channel as an edge
// and read the size themselves. A read that fails is skipped, not a change.
func pollSize(read sizeReader, every time.Duration) (<-chan os.Signal, func()) {
	ch := make(chan os.Signal, 1)
	stop := make(chan struct{})
	go func() {
		if read == nil {
			return // no size to watch: never a tick, as before
		}
		lastW, lastH, _ := read()
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				w, h, err := read()
				if err != nil || (w == lastW && h == lastH) {
					continue
				}
				lastW, lastH = w, h
				select {
				case ch <- syscall.Signal(0):
				default: // a pending tick already queued: coalesce
				}
			}
		}
	}()
	var once bool
	return ch, func() {
		if !once {
			once = true
			close(stop)
		}
	}
}
