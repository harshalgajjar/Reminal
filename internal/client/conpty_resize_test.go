// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// conptyRecord is one line of a capture of what ConPTY sent a Windows session
// (testdata/conpty): a resize, or output.
type conptyRecord struct {
	resize     bool
	cols, rows int
	out        []byte
}

var conptyLineRe = regexp.MustCompile(`^\S+ (out|resize) (".*")$`)

func readConptyCapture(t *testing.T, name string) []conptyRecord {
	t.Helper()
	f, err := os.Open("testdata/conpty/" + name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var recs []conptyRecord
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		m := conptyLineRe.FindStringSubmatch(sc.Text())
		if m == nil {
			t.Fatalf("not a capture line: %q", sc.Text())
		}
		s, err := strconv.Unquote(m[2])
		if err != nil {
			t.Fatal(err)
		}
		if m[1] == "resize" {
			var c, r int
			if _, err := fmt.Sscanf(s, "%dx%d", &c, &r); err != nil {
				t.Fatal(err)
			}
			recs = append(recs, conptyRecord{resize: true, cols: c, rows: r})
			continue
		}
		recs = append(recs, conptyRecord{out: []byte(s)})
	}
	return recs
}

// Resizing a Windows session never writes its lines into the history again.
// After a resize ConPTY repaints its whole screen from the top (ESC[H, every
// row, ESC[K, CRLF), as its own buffer has it. Captured on Windows 11: 80
// numbered lines, then the window resized three times and a viewer resizing
// the same way.
func TestAWindowsSessionsResizesWriteNoLineTwice(t *testing.T) {
	numbered := regexp.MustCompile(`^line (\d+)\s*$`)
	for _, name := range []string{"resize-powershell.txt", "resize-cmd.txt"} {
		t.Run(name, func(t *testing.T) {
			recs := readConptyCapture(t, name)
			s, err := newSessionScreen(120, 30, 10000)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			s.growAsConhost = true // as a session's screen on Windows is
			for _, r := range recs {
				if r.resize {
					s.Resize(r.cols, r.rows)
					continue
				}
				_, _ = s.Write(r.out)
			}
			seen := map[int]int{}
			for _, l := range append(s.History(), strings.Split(s.Render(), "\n")...) {
				if m := numbered.FindStringSubmatch(stripANSI(l)); m != nil {
					n, _ := strconv.Atoi(m[1])
					seen[n]++
				}
			}
			var twice, lost []int
			for n := 1; n <= 80; n++ {
				switch c := seen[n]; {
				case c == 0:
					lost = append(lost, n)
				case c > 1:
					twice = append(twice, n)
				}
			}
			if len(lost) > 0 {
				t.Errorf("lines lost: %v", lost)
			}
			if len(twice) > 0 {
				t.Errorf("lines written more than once: %v (counts %v)", twice, countsOf(seen, twice))
			}
		})
	}
}

func countsOf(seen map[int]int, ns []int) []int {
	out := make([]int, len(ns))
	for i, n := range ns {
		out[i] = seen[n]
	}
	return out
}
