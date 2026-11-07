// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package vtwasm

import (
	"strconv"
	"strings"
)

// stepsToSpaces is libghostty's VT output with its cells that hold no text
// written as spaces. It steps over them (CUF, "ESC[nC"), and erases a run
// that has only a background then steps over it ("ESC[nX ESC[nC"), so as not
// to make them text in the pen before them. But a cursor step does not wrap:
// painted into a narrower terminal, what follows a step near the right edge
// piles up in the last column, and a reader of the words loses the spaces
// between them. A background run becomes spaces in its background (the pen
// is already it); a bare step becomes spaces with no pen, and the pen is
// set again after them.
func stepsToSpaces(s string) string {
	if !strings.Contains(s, "C") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	pen := "" // the SGR sequences since the last reset, as written
	for i := 0; i < len(s); {
		if s[i] != 0x1b || i+1 >= len(s) || s[i+1] != '[' {
			if s[i] == '\n' {
				pen = ""
			}
			b.WriteByte(s[i])
			i++
			continue
		}
		j := i + 2
		for j < len(s) && (s[j] >= '0' && s[j] <= '9' || s[j] == ';' || s[j] == ':') {
			j++
		}
		if j >= len(s) {
			b.WriteString(s[i:])
			break
		}
		params, final := s[i+2:j], s[j]
		seq := s[i : j+1]
		switch final {
		case 'm':
			if params == "" || params == "0" {
				pen = ""
			} else {
				pen += seq
			}
			b.WriteString(seq)
		case 'X':
			// An erase of n with the background, then a step over it: n
			// spaces in that background.
			n := count(params)
			next := "\x1b[" + params + "C"
			if strings.HasPrefix(s[j+1:], next) {
				b.WriteString(strings.Repeat(" ", n))
				j += len(next)
			} else {
				b.WriteString(seq)
			}
		case 'C':
			n := count(params)
			if pen == "" {
				b.WriteString(strings.Repeat(" ", n))
			} else {
				b.WriteString("\x1b[0m")
				b.WriteString(strings.Repeat(" ", n))
				b.WriteString(pen)
			}
		default:
			b.WriteString(seq)
		}
		i = j + 1
	}
	return b.String()
}

// count is a CSI parameter as a count: 1 when empty or not a number.
func count(params string) int {
	n, err := strconv.Atoi(params)
	if err != nil || n < 1 {
		return 1
	}
	return n
}
