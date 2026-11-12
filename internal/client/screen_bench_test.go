// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"fmt"
	"testing"
)

// BenchmarkSessionScreenPace is the session's copy of its screen at the pace
// of an agent at work: each op is a piece of output and the reads the agent
// makes after it (record, noteScrollbackLen), with the attention scan's read
// every 8 pieces and a viewer's resize every 64.
//
//	go test -run xxx -bench BenchmarkSessionScreenPace ./internal/client/
func BenchmarkSessionScreenPace(b *testing.B) {
	s, err := newSessionScreen(120, 40, 5000)
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	sizes := [][2]int{{120, 40}, {100, 36}}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = s.Write([]byte(fmt.Sprintf("\x1b[32m●\x1b[0m step %d: reading a file and thinking about it\r\n", i)))
		_ = s.HistoryLen()
		if i%8 == 7 {
			_ = s.IsAltScreen()
			_ = s.Render()
		}
		if i%64 == 63 {
			sz := sizes[(i/64)%2]
			s.Resize(sz[0], sz[1])
			_ = s.HistoryLines()
		}
	}
}
