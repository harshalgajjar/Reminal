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

// TestSnapshotNeverDropsScrollback guards the regression where the snapshot silently
// deleted committed scrollback. A fresh agent (as a hot-restart creates) starts with an
// empty scrollback, then genuine conversation history accumulates while the viewport is
// resized a couple of times (phone connect + address-bar). The reconnect snapshot MUST
// still contain that history — a viewer left with "nothing to scroll" is the failure the
// removed positional frame-band drop caused (its band opened at index 0 and grew to cover
// the whole buffer).
func TestSnapshotNeverDropsScrollback(t *testing.T) {
	key, _ := crypto.NewSessionKey()
	box, _ := crypto.NewBox(key)
	a := &Agent{box: box, buf: newScrollback(8 << 20), scrollbackLines: 20000}
	a.screen = vt.NewEmulator(50, 20)
	a.screen.Scrollback().SetMaxLines(20000)
	go io.Copy(io.Discard, a.screen)
	a.buf.SetBase(50, 20)

	a.resizeScreen(50, 18) // phone connects — first resize on an empty buffer
	for i := 1; i <= 80; i++ {
		a.record([]byte(fmt.Sprintf("HISTORY-%04d a real committed conversation line the user wants to scroll back to\r\n", i)))
	}
	a.resizeScreen(50, 17) // address bar / a second fit while more content arrives
	a.record([]byte("HISTORY-0081 more\r\nHISTORY-0082 more\r\n"))

	frm, _ := a.snapshotFrame()
	pt, err := box.Decrypt(frm)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	dst := vt.NewEmulator(50, 400)
	dst.Scrollback().SetMaxLines(20000)
	go io.Copy(io.Discard, dst)
	dst.Write(pt)
	rows := strings.Split(strings.ReplaceAll(dst.Render(), "\r\n", "\n"), "\n")

	present := 0
	for i := 1; i <= 82; i++ {
		mark := fmt.Sprintf("HISTORY-%04d", i)
		for _, r := range rows {
			if strings.Contains(r, mark) {
				present++
				break
			}
		}
	}
	t.Logf("history lines present in snapshot: %d/82", present)
	if present < 82 {
		t.Errorf("snapshot dropped scrollback: only %d/82 committed history lines survived (want all)", present)
	}
}
