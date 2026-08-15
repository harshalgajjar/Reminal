// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !windows

package client

import (
	"testing"

	creackpty "github.com/creack/pty"
	ipty "github.com/reminal/reminal/internal/pty"
)

// End-to-end guard: initScreen on a resumed agent (viewer size unknown) must
// seed the rebuild baseline from the inherited PTY's real size, not 80x24.
func TestInitScreenSeedsGeometryFromPTYOnResume(t *testing.T) {
	ptmx, tty, err := creackpty.Open()
	if err != nil {
		t.Skipf("pty unavailable: %v", err)
	}
	defer func() { _ = ptmx.Close() }()
	defer func() { _ = tty.Close() }()
	if err := creackpty.Setsize(ptmx, &creackpty.Winsize{Rows: 40, Cols: 100}); err != nil {
		t.Fatalf("setsize: %v", err)
	}

	a := &Agent{buf: newScrollback(scrollbackBytes), term: ipty.Attach(ptmx)}
	a.initScreen()

	if c, r := a.buf.Base(); c != 100 || r != 40 {
		t.Fatalf("initScreen on resume seeded %dx%d, want 100x40 (regression: fell back to 80x24)", c, r)
	}
}
