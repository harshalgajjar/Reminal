// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"fmt"
	"strings"
	"testing"

	"reminal/internal/crypto"
)

// A flurry of resizes with no output between them (a phone folded and
// unfolded, the keyboard opened and closed) takes the screen's copy through
// every size, and some of those are lossy: a width shrink cuts rows, a
// height shrink drops rows under the cursor. The output ring once kept only
// the last of such a run, so a restart's replay of the ring went through one
// size where the copy went through four, and drew a history the copy did
// not hold. No cut of the dumped history then matched, and the restart put
// all of it back on top of the replay: the same text twice. The ring keeps
// every resize now, and a restart after a flurry brings the history back
// once.
func TestARestartAfterAResizeFlurryKeepsTheHistoryOnce(t *testing.T) {
	key, _ := crypto.NewSessionKey()
	box, _ := crypto.NewBox(key)
	a := handoverAgent(t, box)
	var b strings.Builder
	for i := 1; i <= 60; i++ {
		fmt.Fprintf(&b, "flurry %02d the quick brown fox jumps over the lazy dog and keeps on running far away\r\n", i)
	}
	a.record([]byte(b.String()))
	a.record([]byte("\x1b[6A")) // the cursor up, as a frame being repainted
	for _, s := range [][2]uint16{{55, 20}, {120, 30}, {55, 20}, {120, 30}} {
		a.resizeScreen(s[0], s[1])
	}
	a.record([]byte("after the flurry\r\n"))
	a.screenMu.Lock()
	before := stripANSI(buildSnapshot(a.screen, a.snapshotHistory(), nil, maxSnapshotPlaintext, false))
	a.screenMu.Unlock()

	path, k, err := a.writeScrollbackDump()
	if err != nil || path == "" {
		t.Fatalf("dump: %q %v", path, err)
	}
	r := handoverAgent(t, box)
	r.resumeDump = takeScrollbackDump(path, k, a.sessionID)
	if r.resumeDump == nil {
		t.Fatal("the dump did not come back")
	}
	markers := 0
	for _, e := range r.resumeDump.Entries {
		if e.Cols > 0 {
			markers++
		}
	}
	if markers != 4 {
		t.Errorf("the ring holds %d resizes of the flurry, want all 4", markers)
	}
	r.restoreResumedScrollback()
	r.screenMu.Lock()
	after := stripANSI(buildSnapshot(r.screen, r.snapshotHistory(), nil, maxSnapshotPlaintext, false))
	r.screenMu.Unlock()
	for _, want := range []string{"flurry 01 the", "flurry 30 the", "after the flurry"} {
		if o, n := strings.Count(before, want), strings.Count(after, want); n != o {
			t.Errorf("%q: %d times before the restart, %d after", want, o, n)
		}
	}
}
