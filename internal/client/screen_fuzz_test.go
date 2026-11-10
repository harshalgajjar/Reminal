// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"io"
	"sync"
	"testing"

	"github.com/charmbracelet/x/vt"
)

// One shared emulator + drain for all iterations, RIS-reset each time (as the live
// a.screen is long-lived). Avoids a per-iteration Close race on vt's unsynchronized
// `closed` field; the mutex serialises Writes across parallel fuzz goroutines.
var (
	rawEmuMu sync.Mutex
	rawEmu   *vt.Emulator
)

// FuzzRawEmulatorWrite feeds arbitrary bytes to a raw vt.Emulator as the live
// screen (a.screen) once was fed — every byte of shell output, in varying chunk
// sizes to mimic PTY read boundaries. The property is "never panics": the agent
// has no recover around its writes to the screen, so a sequence that panicked
// an emulator would crash it on its hot path. The live screen is vtwasm now
// (its conformance tests cover it); this guards the emulator the tests still use.
func FuzzRawEmulatorWrite(f *testing.F) {
	seeds := [][]byte{
		[]byte("hello\r\nworld\r\n\x1b[2J"),
		[]byte("\x1b[r\x88"), // vviewWriter's trigger — harmless to the raw emulator, but seed it
		[]byte("\x1b[999;999H\x1b[J"),
		[]byte("\x1b]0;title\x07"),
		[]byte("\x1b#8\x1b[1;1H"),   // DECALN
		[]byte("\x1bP+q544e\x1b\\"), // DCS
		[]byte("\x1b[38;2;255;0;0m\x1b[48;5;16m"),
		[]byte("\x1b[6n\x1b[?2004h\x1b[?1049h\x1b[?1049l"),
	}
	for _, s := range seeds {
		f.Add(s, 3)
	}
	f.Fuzz(func(t *testing.T, data []byte, chunk int) {
		if chunk < 1 {
			chunk = 1
		}
		if chunk > 4096 {
			chunk = 4096
		}
		rawEmuMu.Lock()
		defer rawEmuMu.Unlock()
		if rawEmu == nil {
			rawEmu = vt.NewEmulator(80, 24)
			e := rawEmu
			go func() { _, _ = io.Copy(io.Discard, e) }()
		}
		e := rawEmu
		_, _ = e.Write([]byte("\x1bc")) // RIS reset each iteration
		for i := 0; i < len(data); i += chunk {
			end := i + chunk
			if end > len(data) {
				end = len(data)
			}
			_, _ = e.Write(data[i:end]) // must never panic
		}
	})
}
