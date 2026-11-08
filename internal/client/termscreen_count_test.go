// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"fmt"
	"strings"
	"testing"
)

// The carried count through re-wraps of long lines (wider than the new width,
// styled or not), with the last line ended, left above the cursor, unended,
// or a prompt being typed, to widths narrower, wider and shorter.
func TestTheHistoryLineCountThroughLongLineRewraps(t *testing.T) {
	bad, total := 0, 0
	w := []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot"}
	long := func(i, n int) string {
		line := fmt.Sprintf("L%03d ", i)
		for k := 0; len(line) < n; k++ {
			line += w[(i*7+k)%6] + " "
		}
		return line[:n]
	}
	for n := 30; n < 70; n++ {
		for _, tail := range []string{"", "\x1b[A\x1b[A", long(999, 187), "❯ " + long(998, 150)} {
			for _, sizes := range [][2]int{{160, 40}, {120, 40}, {160, 30}, {210, 50}} {
				s := testScreen(t, 200, 40)
				s.SetMaxLines(100000)
				var b strings.Builder
				for i := 0; i < n; i++ {
					l := long(i, 150+(i*37)%50)
					if i%2 == 0 {
						l = "\x1b[1m" + l[:20] + "\x1b[0m\x1b[38;2;200;100;50m" + l[20:120] + "\x1b[0m" + l[120:]
					}
					fmt.Fprintf(&b, "%s\r\n", l)
				}
				b.WriteString(tail)
				s.Write([]byte(b.String()))
				_ = s.HistoryLines()
				s.Resize(sizes[0], sizes[1])
				out, _ := s.t.History()
				got, want := s.HistoryLines(), len(splitRows(out))
				total++
				if got != want {
					bad++
					if bad < 6 {
						t.Logf("n=%d tail=%q size=%v: count %d, history %d", n, tail[:min(len(tail), 8)], sizes, got, want)
					}
				}
			}
		}
	}
	if bad > 0 {
		t.Errorf("%d of %d off", bad, total)
	}
}
