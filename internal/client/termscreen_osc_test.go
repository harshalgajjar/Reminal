// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
)

// Claude Code's title is its spinner glyph and its name ("✳ Claude Code"),
// set with OSC 0. Each glyph's UTF-8 holds the byte 0x9C, which is also the
// C1 string terminator: the old emulator took it as the end of the title
// and printed the rest (" Claude Code") onto the screen, where a join then
// showed it between the program's own output. A title is a title, however
// it is terminated, and nothing of it reaches the screen.
func TestATitleWithAByte9CStaysOffTheScreen(t *testing.T) {
	for _, glyph := range []string{"✢", "✳", "✶", "✻", "✽"} {
		for name, end := range map[string]string{"BEL": "\a", "ST": "\x1b\\"} {
			s := testScreen(t, 20, 3)
			if _, err := s.Write([]byte("AB\x1b]0;" + glyph + " Claude Code" + end + "CD")); err != nil {
				t.Fatalf("%s %s: write: %v", glyph, name, err)
			}
			rows := strings.Split(s.Render(), "\n")
			for i, r := range rows {
				rows[i] = strings.TrimRight(stripANSI(r), " ")
			}
			if got, want := strings.Join(rows, "|"), "ABCD||"; got != want {
				t.Errorf("%s %s: the screen is %q, want %q", glyph, name, got, want)
			}
			if got, want := s.CursorPosition(), uv.Pos(4, 0); got != want {
				t.Errorf("%s %s: the cursor is at %v, want %v", glyph, name, got, want)
			}
			s.Close()
		}
	}
}
