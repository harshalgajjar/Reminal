// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/charmbracelet/x/vt"
	"reminal/internal/crypto"
)

// A viewer joining a live session whose terminal is taller than the
// session's screen: the snapshot paints history, then the screen as the
// bottom block of what it painted — so the screen sits lower in that viewer
// than its row numbers say. The cursor must land on the screen's cursor row
// all the same: the shell's next output (its prompt redrawn after the
// viewer's resize) is written there, and landing even one row high wrote it
// over the newest lines of output — they were lost from that viewer.
func TestSnapshotCursorLandsOnTheScreensCursorRow(t *testing.T) {
	for _, extra := range []int{0, 2, 10} {
		t.Run(fmt.Sprintf("a viewer %d rows taller", extra), func(t *testing.T) {
			key, _ := crypto.NewSessionKey()
			box, _ := crypto.NewBox(key)
			a := &Agent{box: box, buf: newScrollback(8 << 20), scrollbackLines: 20000}
			a.screen = testScreen(t, 120, 30)
			a.screen.SetMaxLines(20000)
			a.buf.SetBase(120, 30)
			for i := 1; i <= 800; i++ {
				a.record([]byte(fmt.Sprintf("L%05d\r\n", i)))
			}
			a.record([]byte("user@host:/work$ "))

			frm, _ := a.snapshotFrame()
			pt, err := box.Decrypt(frm)
			if err != nil {
				t.Fatalf("decrypt: %v", err)
			}
			dst := vt.NewEmulator(120, 30+extra)
			dst.Scrollback().SetMaxLines(20000)
			go io.Copy(io.Discard, dst)
			freeAtEnd(t, dst)
			dst.Write(pt)
			// The shell, told of the viewer's size, redraws its prompt line.
			dst.Write([]byte("\r\x1b[Kuser@host:/work$ "))
			rows := strings.Split(strings.ReplaceAll(dst.Render(), "\r\n", "\n"), "\n")
			y := dst.CursorPosition().Y
			if y < 1 || !strings.HasPrefix(rows[y], "user@host:/work$") || strings.TrimSpace(rows[y-1]) != "L00800" {
				lo := max(0, y-2)
				t.Fatalf("cursor on row %d of %d: %q (rows around it: %q)", y, len(rows), rows[y], rows[lo:min(len(rows), y+2)])
			}
		})
	}
}
