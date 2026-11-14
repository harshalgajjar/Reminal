// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"fmt"
	"strings"
	"testing"
)

// BenchmarkSessionScreenWindow is the session's copy of its screen through
// 70 s of an agent at work with a viewer that resizes often, as a soak
// counted it: 437 writes of 839 bytes on average, each followed by the
// agent's read of the scrollback (record, noteScrollbackLen); 166 reads of
// the screen (the attention scan); 44 resizes, 42 of them changing the width
// (resizeScreen's own reads). One op is the whole window.
//
//	go test -run xxx -bench BenchmarkSessionScreenWindow ./internal/client/
func BenchmarkSessionScreenWindow(b *testing.B) {
	const writes, renders, resizes = 437, 166, 44
	chunks := make([][]byte, writes)
	for i := range chunks {
		var c strings.Builder
		for n := 0; c.Len() < 839; n++ {
			fmt.Fprintf(&c, "\x1b[32m●\x1b[0m step %d.%d: reading %s and thinking about what it says\r\n", i, n, strings.Repeat("a file ", 1+(i+n)%9))
		}
		chunks[i] = []byte(c.String())
	}
	sizes := [][2]int{{120, 40}, {100, 40}, {140, 45}, {80, 40}}
	s, err := newSessionScreen(120, 40, 300)
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	for _, c := range chunks { // a scrollback about as long as the soak's (300 lines)
		_, _ = s.Write(c)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for w, c := range chunks {
			_, _ = s.Write(c)
			_ = s.HistoryLen()
			if (w+1)*renders/writes != w*renders/writes {
				_ = s.IsAltScreen()
				_ = s.Render()
			}
			if r := (w + 1) * resizes / writes; r != w*resizes/writes {
				sz := sizes[r%len(sizes)]
				if r%22 == 0 {
					sz[1] -= 10 // a height change alone
				}
				_ = s.Render()
				s.Resize(sz[0], sz[1])
				_ = s.HistoryLines()
			}
		}
	}
}
