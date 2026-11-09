// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"path/filepath"
	"strings"
	"testing"

	"reminal/internal/crypto"
)

// Two recorded sessions' restart dumps (testdata/restart), written by the
// screen copy their rings are replayed with here: one young, its ring from
// its first output, and one already restored once. A restart brings each
// history back once. The welcome banner and a line from the middle of the
// history are in the result as many times as in the dump, and the result
// is no longer than the dump's history and a screen.
func TestARecordedSessionsRestartBringsItsHistoryBackOnce(t *testing.T) {
	for _, path := range ringFixtures(t) {
		t.Run(filepath.Base(path), func(t *testing.T) {
			d := readRingFixture(t, path)
			if len(d.History) < 100 {
				t.Fatalf("fixture: %d history lines, want a session's worth", len(d.History))
			}
			key, _ := crypto.NewSessionKey()
			box, _ := crypto.NewBox(key)
			r := handoverAgent(t, box)
			r.resumeDump = d
			r.restoreResumedScrollback()
			r.screenMu.Lock()
			after := append(append([]string(nil), r.renderedHistory()...), renderedScreenRows(r.screen)...)
			rows := r.screen.Height()
			r.screenMu.Unlock()

			count := func(lines []string, s string) int {
				n := 0
				for _, l := range lines {
					if strings.Contains(stripANSI(l), s) {
						n++
					}
				}
				return n
			}
			probes := []string{"Welcome"}
			for i := len(d.History) / 2; i < len(d.History); i++ {
				if s := strings.TrimSpace(stripANSI(d.History[i])); len(s) > 20 {
					probes = append(probes, s)
					break
				}
			}
			for _, p := range probes {
				if before, got := count(d.History, p), count(after, p); got != before {
					t.Errorf("%q: %d times in the dump's history, %d after the restart", p, before, got)
				}
			}
			if max := len(d.History) + rows; len(after) > max {
				t.Errorf("%d lines after the restart, the dump held %d (at most %d expected)", len(after), len(d.History), max)
			}
		})
	}
}
