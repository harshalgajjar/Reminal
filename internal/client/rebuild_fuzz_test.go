// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"io"
	"sync"
	"testing"

	"github.com/charmbracelet/x/vt"
)

// One shared emulator + drain goroutine for all fuzz iterations, reset with RIS
// each time (as the agent's history rebuild once reused its emulator). This
// avoids both a per-iteration goroutine leak and the data race that Close() would
// cause against the drain goroutine (vt.Emulator's `closed` field is
// unsynchronized). The mutex serialises Writes across parallel fuzz goroutines.
var (
	fuzzEmuMu sync.Mutex
	fuzzEmu   *vt.Emulator
)

// FuzzVViewWrite feeds arbitrary bytes through vviewWriter (the snapshot history
// rebuilder) in varying chunk sizes, exercising its CSI parser, the carry-over
// state that spans chunk boundaries, geometry changes, and the cursor/row math.
// The property is "never panics": the input is terminal output, which any program
// the user runs can make arbitrary, and an unrecovered panic in vviewWriter would
// take down the snapshot path (and the agent). vviewWriter now contains such
// panics internally (marking itself dead); this guards that invariant. It does not
// assert anything about the duplicate-scrollback bug — that needs a real captured
// stream.
func FuzzVViewWrite(f *testing.F) {
	seeds := [][]byte{
		[]byte("hello\r\nworld\r\n"),
		[]byte("\x1b[H\x1b[2J\x1b[31mred\x1b[0m"),
		[]byte("\x1b[10;20Hpos\x1b[1B\x1b[2K"),
		[]byte("\x1b[999999999;999999999H"), // absurd params
		[]byte("\x1b["),                     // truncated CSI at edge
		[]byte("\x1b[?25l\x1b[6n"),          // private + DSR (generates a response)
		[]byte("\x1b[;;;;;;m\x1b[J\x1b[0J\x1b[1J\x1b[2J\x1b[3J"),
		[]byte("\x1b[r\x1b[10;20r\x1b[A\x1b[B\x1b[C\x1b[D"), // DECSTBM + cursor moves
		[]byte("\x1b[r\x88"),                                // regression: empty DECSTBM then a C1 byte
	}
	for _, s := range seeds {
		f.Add(s, 7)
	}
	f.Fuzz(func(t *testing.T, data []byte, chunk int) {
		if chunk < 1 {
			chunk = 1
		}
		if chunk > 4096 {
			chunk = 4096
		}
		fuzzEmuMu.Lock()
		defer fuzzEmuMu.Unlock()
		if fuzzEmu == nil {
			fuzzEmu = vt.NewEmulator(80, 400)
			e := fuzzEmu
			go func() { _, _ = io.Copy(io.Discard, e) }() // one drain, never closed
		}
		e := fuzzEmu
		// RIS to a clean state each iteration. Guard it: a prior iteration's
		// adversarial input can leave the shared emulator in a state where even
		// this direct write panics — not a production concern (nothing in the
		// agent writes to this emulator), just shared-fixture hygiene.
		func() {
			defer func() { _ = recover() }()
			_, _ = e.Write([]byte("\x1bc"))
		}()
		w := &vviewWriter{e: e, rows: 24}

		for i := 0; i < len(data); i += chunk {
			end := i + chunk
			if end > len(data) {
				end = len(data)
			}
			w.Write(data[i:end]) // must never panic — vviewWriter contains it
			_ = w.Base()
		}
		w.setGeometry(120, 40)
		w.Write(data)
		_ = w.Base()
	})
}
