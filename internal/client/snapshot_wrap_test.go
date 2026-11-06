// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"io"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/vt"
)

// paintedAt replays a snapshot paint through a terminal cols by rows and
// returns what it holds (scrollback then screen, a row a line) and where
// its cursor is.
func paintedAt(cols, rows int, paint string) (string, uv.Position) {
	dst := vt.NewEmulator(cols, rows)
	dst.Scrollback().SetMaxLines(20000)
	go io.Copy(io.Discard, dst)
	dst.Write([]byte(paint))
	var b strings.Builder
	for _, ln := range dst.Scrollback().Lines() {
		b.WriteString(ln.Render())
		b.WriteByte('\n')
	}
	b.WriteString(dst.Render())
	return b.String(), dst.CursorPosition()
}

// A history line is as the program wrote it, however long, and a viewer
// narrower than it wraps it at its own width: a phone joining a session that
// ran at 120 columns reads the whole line, over three rows. The screen's
// rows are painted as rows, so the cursor lands on the prompt's row below
// the wrapped history, wherever that row ended up.
func TestAJoinerNarrowerThanTheSessionWrapsItsHistory(t *testing.T) {
	long := "first " + strings.Repeat("middle ", 14) + "last-word-of-the-line"
	if len(long) <= 100 {
		t.Fatalf("fixture: the line must be longer than the session is wide: %d", len(long))
	}
	v := snapView{width: 120, height: 4, screen: "$ \n\n\n", cursor: uv.Pos(2, 0)}
	paint := paintSnapshot(v, []string{long, "short"}, nil, 0, false)

	got, cur := paintedAt(40, 10, paint)
	// The wrap falls where it falls, inside a word too: read the rows as
	// one line again.
	if text := strings.ReplaceAll(got, "\n", ""); !strings.Contains(text, "last-word-of-the-line") || !strings.Contains(text, "short") {
		t.Errorf("a 40-column viewer lost the end of a 120-column line:\n%s", got)
	}
	// The line wraps onto as many rows of 40 as it needs, "short" is the
	// next, the prompt the one after: column 2 of that row.
	wrapped := (len(long) + 39) / 40
	if want := uv.Pos(2, wrapped+1); cur != want {
		t.Errorf("the cursor is at %v, want %v (on the prompt below the wrapped history):\n%s", cur, want, got)
	}
}
