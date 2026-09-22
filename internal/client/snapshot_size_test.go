// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"io"
	"strings"
	"testing"

	"github.com/charmbracelet/x/vt"

	"reminal/internal/crypto"
)

// TestSnapshotFitsRelayCap guards the invariant that the ENCRYPTED reconnect
// snapshot always fits the relay's 1 MiB WS frame limit. The snapshot is sent as
// one un-chunked frame; before the maxSnapshotPlaintext clamp a dense 10k-line
// session produced a ~1.1 MiB frame that the relay dropped, breaking reconnect.
func TestSnapshotFitsRelayCap(t *testing.T) {
	const relayCap = 1 << 20

	encSize := func(cols, rows, nlines, linelen int) int {
		e := vt.NewEmulator(cols, rows)
		go func() { _, _ = io.Copy(io.Discard, e) }()
		e.Scrollback().SetMaxLines(10000)
		line := strings.Repeat("x", linelen) + "\r\n"
		for i := 0; i < nlines; i++ {
			_, _ = e.Write([]byte(line))
		}
		history := renderScrollback(e, 10000)
		budget := scrollbackBytes
		if budget <= 0 || budget > maxSnapshotPlaintext {
			budget = maxSnapshotPlaintext
		}
		snap := buildSnapshot(e, history, nil, budget, false)
		box, _ := crypto.NewBox(make([]byte, 32))
		enc, _ := box.Encrypt([]byte(snap))
		return len(enc)
	}

	cases := []struct {
		cols, rows, nlines, linelen int
	}{
		{120, 40, 10000, 78},  // realistic dense build/test logs (the regression case)
		{250, 80, 10000, 240}, // huge terminal, very long lines
		{80, 24, 10000, 79},   // full-width 80-col
		{200, 50, 10000, 199}, // wide
	}
	for _, c := range cases {
		enc := encSize(c.cols, c.rows, c.nlines, c.linelen)
		if enc > relayCap {
			t.Errorf("%dx%d %d×%dch: encrypted snapshot %d bytes EXCEEDS relay cap %d by %d",
				c.cols, c.rows, c.nlines, c.linelen, enc, relayCap, enc-relayCap)
		}
	}
}
