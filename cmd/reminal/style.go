// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"os"
	"strings"
	"unicode/utf8"

	"golang.org/x/term"
)

// Small ANSI styling helpers for CLI output. Colours are emitted only when
// stdout is a real terminal and NO_COLOR isn't set, so piped/redirected output
// stays clean. Palette matches the 256-colour codes used elsewhere (settings,
// `reminal list`).
var useColor = term.IsTerminal(int(os.Stdout.Fd())) && os.Getenv("NO_COLOR") == ""

func sgr(code, s string) string {
	if !useColor || s == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

// Emphasis is bold + dim (black-on-white friendly); colour is reserved for
// status only, using the basic bold ANSI palette that stays legible on BOTH
// light and dark terminal backgrounds (256-colour shades wash out on white).
func cBold(s string) string  { return sgr("1", s) }
func cDim(s string) string   { return sgr("2", s) }
func cGreen(s string) string { return sgr("1;32", s) }
func cRed(s string) string   { return sgr("1;31", s) }

// padCol left-aligns s to a visible width of w, then applies colour. Padding is
// added on the PLAIN text so columns line up even though the colour escapes have
// zero display width (which tabwriter would miscount). A negative pad (s already
// wider than w) just leaves it as-is plus a single space is the caller's job.
func padCol(s string, w int, color func(string) string) string {
	pad := w - utf8.RuneCountInString(s)
	if pad < 0 {
		pad = 0
	}
	return color(s) + strings.Repeat(" ", pad)
}

// visLen is the display width of s ignoring SGR escapes — for width math.
func visLen(s string) int { return utf8.RuneCountInString(stripSGR(s)) }

// cleanTerm strips ESC and other control characters from REMOTE-sourced text
// (session names/titles arrive from another machine over the directory channel),
// so a malicious or compromised owned machine can't inject terminal escape
// sequences into your terminal when you run `reminal machines`.
// isC1OrBidi: the C1 controls (U+0080-U+009F — U+009B is CSI to a terminal
// that honours C1 in UTF-8, so stripping ESC alone is not enough) and the
// characters that change which way text reads, which can make a name read as
// something other than what it is.
func isC1OrBidi(r rune) bool {
	return (r >= 0x80 && r <= 0x9f) || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) ||
		r == 0x200e || r == 0x200f || r == 0x061c
}

func cleanTerm(s string) string {
	if s == "" {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == 0x1b || r < 0x20 || r == 0x7f || isC1OrBidi(r) {
			continue // ESC, C0 controls, DEL, C1 controls, direction overrides
		}
		b.WriteRune(r)
	}
	return b.String()
}

func stripSGR(s string) string {
	if !strings.Contains(s, "\x1b[") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && s[j] != 'm' {
				j++
			}
			i = j + 1
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
