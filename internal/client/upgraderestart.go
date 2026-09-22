// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/reminal/reminal/internal/protocol"
	"github.com/reminal/reminal/internal/session"
)

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// sessionCountTTL caches the count between reads. host_info is polled every
// 1.5s while the Host panel is open and ReadAllActive parses one file per
// session, so an uncached count meant hundreds of file reads a minute on a
// machine with a dozen sessions — for a number that changes when someone
// opens or closes a shell.
const sessionCountTTL = 5 * time.Second

var (
	sessCountMu   sync.Mutex
	sessCountVal  int
	sessCountRead time.Time
)

// countRestartableSessions counts the shells a restart would move. Port
// forwards are skipped — they hold no PTY and `restart --all` skips them too.
func countRestartableSessions() int {
	sessCountMu.Lock()
	defer sessCountMu.Unlock()
	if !sessCountRead.IsZero() && time.Since(sessCountRead) < sessionCountTTL {
		return sessCountVal
	}
	all, err := session.ReadAllActive()
	if err != nil {
		return sessCountVal // keep the last good answer rather than claiming zero
	}
	n := 0
	for i := range all {
		if !all[i].IsPort() && all[i].PID > 0 {
			n++
		}
	}
	sessCountVal, sessCountRead = n, time.Now()
	return n
}

// applyLocalRestart hot-restarts this machine's sessions in answer to a
// directory query that asked for it, recording the outcome on the response.
//
// Served by the machine channel's agent — the daemon — which is not itself a
// session, so restartOtherSessions moves every shell and leaves the channel
// carrying this request intact. That is what lets the host answer at all: a
// restart that took the daemon with it could never report back.
func applyLocalRestart(resp *protocol.DirResponse) {
	if resp == nil {
		return
	}
	n := countRestartableSessions()
	if err := restartOtherSessions(); err != nil {
		resp.RestartError = err.Error()
		return
	}
	resp.RestartOK = true
	resp.RestartCount = n
}

// restartOtherSessions hot-restarts every session on this host except the one
// serving this request. Best-effort per session: one agent that refuses (an
// old binary, a wedged socket) must not strand the rest on the old version.
func restartOtherSessions() error {
	all, err := session.ReadAllActive()
	if err != nil {
		return fmt.Errorf("listing sessions: %w", err)
	}
	self := os.Getpid()
	var failed int
	for i := range all {
		a := &all[i]
		if a.PID <= 0 || a.PID == self {
			continue
		}
		if a.IsPort() {
			// Forwards hot-swap in place (same id/PIN/URL) so an upgrade reaches
			// them too. Skip a forward whose record has no version: it predates
			// hot-swap and would read the signal as shutdown. It stays on the old
			// code until the user re-exposes — better than killing its URL.
			if a.Version == "" {
				continue
			}
			if err := RestartPortForward(a.PID); err != nil {
				// Windows cannot hot-swap a forward at all. That is the same
				// outcome as the versionless case above — the forward keeps
				// serving on the old code — not a failure: counting it as one
				// made every upgrade on a Windows box with a live `expose`
				// report "failed" after the binary had already been replaced.
				if errors.Is(err, errPortRestartUnsupported) {
					continue
				}
				failed++
			}
			continue
		}
		if _, err := sendControlTo(a.PID, "restart"); err != nil {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d session%s did not restart; they stay on the old binary until restarted by hand", failed, plural(failed))
	}
	return nil
}
