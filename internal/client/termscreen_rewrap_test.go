// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"fmt"
	"strings"
	"testing"
)

// The carried count of the history's lines stays exact while the scrollback
// is at its byte budget and its oldest pages go: 40 rounds of 1,500 lines
// longer than the width, the width stepping down by 3 every third round (200
// to 161). Counted only from the rows added, the count was one too many in 8
// of them (a page let go mid-line); the history's first line changing now
// says the top went, and it is counted again.
func TestHistoryLineCountAfterRewrapOfLongLines(t *testing.T) {
	s := testScreen(t, 200, 50)
	s.SetMaxLines(100000)
	full := func() int { out, _ := s.t.History(); return len(splitRows(out)) }
	line := strings.Repeat("alpha beta ", 17)
	mism := 0
	for round := 0; round < 40; round++ {
		var b strings.Builder
		for i := 0; i < 1500; i++ {
			fmt.Fprintf(&b, "r%d-%d %s\r\n", round, i, line)
		}
		if _, err := s.Write([]byte(b.String())); err != nil {
			t.Fatal(err)
		}
		if round%3 == 0 {
			s.Resize(200-round, 50)
		}
		if got, want := s.HistoryLines(), full(); got != want {
			mism++
			t.Errorf("round %d (width %d): HistoryLines=%d, full count=%d, rows=%d", round, s.Width(), got, want, s.t.ScrollbackRows())
		}
	}
	t.Logf("mismatches: %d of 40", mism)
}
