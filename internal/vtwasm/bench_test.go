// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package vtwasm

import (
	"fmt"
	"testing"
)

// BenchmarkWriteThenRead is a session's work for each piece of output: the
// write, then the reads the session makes after it (its scrollback, the
// alternate screen, its size and cursor).
func BenchmarkWriteThenRead(b *testing.B) {
	v, err := New(120, 40, 20000)
	if err != nil {
		b.Fatal(err)
	}
	defer v.Close()
	p := []byte("some output from a program, a line of it\r\n")
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = v.Write(p)
		_ = v.ScrollbackRows()
		_ = v.AltScreen()
		_, _ = v.Size()
		_, _ = v.Cursor()
	}
}

// BenchmarkScreen is formatting the screen, as the attention scan does after
// output.
func BenchmarkScreen(b *testing.B) {
	v, err := New(120, 40, 20000)
	if err != nil {
		b.Fatal(err)
	}
	defer v.Close()
	for i := 0; i < 200; i++ {
		fmt.Fprintf(v, "line %d of some output\r\n", i)
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = v.Write([]byte("x"))
		_, _ = v.Screen()
	}
}

// BenchmarkResize is a resize of a terminal holding lines of scrollback: a
// change of height alone (a phone's keyboard), and of width, which re-wraps
// the scrollback.
func BenchmarkResize(b *testing.B) {
	for _, lines := range []int{1000, 10000} {
		for _, k := range []struct {
			name string
			a, z [2]int
		}{{"height", [2]int{120, 40}, [2]int{120, 30}}, {"width", [2]int{120, 40}, [2]int{100, 40}}} {
			b.Run(fmt.Sprintf("%s-%d", k.name, lines), func(b *testing.B) {
				v, err := New(120, 40, 4*lines)
				if err != nil {
					b.Fatal(err)
				}
				defer v.Close()
				for i := 0; i < lines; i++ {
					fmt.Fprintf(v, "line %d of some output from a program at work, longer than a phone is wide\r\n", i)
				}
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					sz := k.a
					if i%2 == 0 {
						sz = k.z
					}
					_ = v.Resize(sz[0], sz[1])
				}
			})
		}
	}
}

// BenchmarkFormat is formatting a span of the history's last rows, as the
// line count across a resize reads them, and the screen, at two lengths of
// scrollback.
func BenchmarkFormat(b *testing.B) {
	for _, lines := range []int{1000, 10000} {
		v, err := New(120, 45, 4*lines)
		if err != nil {
			b.Fatal(err)
		}
		for i := 0; i < lines; i++ {
			fmt.Fprintf(v, "\x1b[32mline\x1b[0m %d of some output from a program at work, longer than a phone is wide\r\n", i)
		}
		n := v.ScrollbackRows()
		for _, span := range []int{8, 45, 376} {
			b.Run(fmt.Sprintf("tail%d-%d", span, lines), func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					_, _ = v.HistoryRows(n-span, n-1)
				}
			})
		}
		b.Run(fmt.Sprintf("first8-%d", lines), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_, _ = v.HistoryRows(0, 7)
			}
		})
		b.Run(fmt.Sprintf("screen-%d", lines), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_, _ = v.Screen()
			}
		})
		v.Close()
	}
}
