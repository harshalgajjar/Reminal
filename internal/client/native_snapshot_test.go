// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"fmt"
	"io"
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/x/vt"
	"reminal/internal/crypto"
)

// nsWrap word-wraps a logical line to width (space-delimited, like an inline TUI).
func nsWrap(s string, w int) []string {
	if w < 1 {
		w = 1
	}
	words := strings.Fields(s)
	if len(words) == 0 {
		return []string{""}
	}
	var rows []string
	line := ""
	for _, wd := range words {
		switch {
		case line == "":
			line = wd
		case len(line)+1+len(wd) <= w:
			line += " " + wd
		default:
			rows = append(rows, line)
			line = wd
		}
	}
	return append(rows, line)
}

// TestNativeSnapshotCommittedCleanThroughWiden models a faithful inline-TUI app the way
// real Claude Code behaves (verified via PTY probe): it commits scrolled-off content once
// and, on SIGWINCH, repaints ONLY its bounded frame (~one screen), not the whole session.
// It drives the width DOWN then UP (a smaller viewer joins, then leaves — the widen-on-
// disconnect case) and asserts the delivered snapshot keeps every committed line exactly
// once with nothing lost. This is the guarantee the native-emulator reconstruction gives
// by construction (a.screen faithfully emulates the terminal; committed history is never
// re-emitted, so it can't duplicate — regardless of how the width changes).
func TestNativeSnapshotCommittedCleanThroughWiden(t *testing.T) {
	var committed, frame []string
	for i := 1; i <= 60; i++ {
		committed = append(committed, fmt.Sprintf("COMMIT-%04d committed transcript line printed once and scrolled off, never touched again by the app", i))
	}
	for i := 1; i <= 18; i++ {
		frame = append(frame, fmt.Sprintf("RECENT-%04d live frame line inside the app re-render window repainted on each resize", i))
	}

	key, _ := crypto.NewSessionKey()
	box, _ := crypto.NewBox(key)
	a := &Agent{box: box, buf: newScrollback(8 << 20), scrollbackLines: 20000}
	a.screen = vt.NewEmulator(70, 24)
	a.screen.Scrollback().SetMaxLines(20000)
	go io.Copy(io.Discard, a.screen) // drain terminal-query replies (initScreen does this live)
	a.buf.SetBase(70, 24)

	var b strings.Builder
	for _, l := range committed {
		b.WriteString(l + "\r\n") // committed content scrolls off once
	}
	fr := nsWrap2(frame, 70)
	for _, r := range fr {
		b.WriteString(r + "\r\n")
	}
	a.record([]byte(b.String()))
	prev := len(fr)

	// widths: start, narrower (small viewer joins), then progressively WIDER (it leaves).
	// Uses the real resizeScreen path so the frame-anchor watermarks are exercised.
	for _, w := range []int{44, 70, 90, 50, 110} {
		a.resizeScreen(uint16(w), 24)
		var rb strings.Builder
		fmt.Fprintf(&rb, "\x1b[%dA", prev) // home to frame top
		fr = nsWrap2(frame, w)
		for _, r := range fr {
			rb.WriteString("\r\x1b[K" + r + "\n") // repaint the bounded frame only
		}
		a.record([]byte(rb.String()))
		prev = len(fr)
	}

	frm, seq := a.snapshotFrame()
	if frm == "" || seq == 0 {
		t.Fatalf("empty snapshot (frame=%q seq=%d)", frm, seq)
	}
	pt, err := box.Decrypt(frm)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	dst := vt.NewEmulator(110, 600)
	dst.Scrollback().SetMaxLines(20000)
	go io.Copy(io.Discard, dst)
	dst.Write(pt)
	rows := strings.Split(strings.ReplaceAll(dst.Render(), "\r\n", "\n"), "\n")

	re := regexp.MustCompile(`(COMMIT|RECENT)-\d{4}`)
	counts := map[string]int{}
	for _, r := range rows {
		for _, m := range re.FindAllString(r, -1) {
			counts[m]++
		}
	}
	missing, dup := 0, 0
	for i := 1; i <= 60; i++ {
		switch counts[fmt.Sprintf("COMMIT-%04d", i)] {
		case 0:
			missing++
		case 1:
		default:
			dup++
		}
	}
	// The frame (RECENT) overflows the 24-row screen at the narrow widths, so its
	// re-emits pile into scrollback. We do NOT dedup those cross-width copies (the
	// positional band-drop that used to was removed — it could delete real scrollback);
	// the guarantee is only that COMMITTED history stays clean and NOTHING is ever lost.
	frameMax, frameMissing := 0, 0
	for i := 1; i <= 18; i++ {
		c := counts[fmt.Sprintf("RECENT-%04d", i)]
		if c == 0 {
			frameMissing++
		}
		if c > frameMax {
			frameMax = c
		}
	}
	t.Logf("committed: %d distinct, missing=%d duplicated=%d | frame: maxCopies=%d missing=%d (5 width changes incl. widen)",
		len(counts), missing, dup, frameMax, frameMissing)
	if missing > 0 || dup > 0 {
		t.Errorf("committed history not pristine through resizes: missing=%d duplicated=%d (want 0,0)", missing, dup)
	}
	if frameMissing > 0 {
		t.Errorf("frame content lost: %d/18 RECENT lines missing (want 0) — nothing may ever be dropped", frameMissing)
	}
}

func nsWrap2(ls []string, w int) []string {
	var out []string
	for _, l := range ls {
		out = append(out, nsWrap(l, w)...)
	}
	return out
}
