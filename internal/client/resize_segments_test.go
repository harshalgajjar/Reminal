// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"fmt"
	"io"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"reminal/internal/crypto"
)

// TestResizeSegmentsDropRepaintsKeepInterleaved is the regression test for the flaw
// that sank the v2.3.0 position-only frame band: everything committed between two
// resizes was treated as repaint overflow, so genuine output that flowed DURING a
// resize burst (or a fresh agent's first paint) was deleted. The captured-frame
// segments must drop exactly the re-wrapped copies of each resize's captured screen
// and nothing else — even when genuine lines land between the resizes.
func TestResizeSegmentsDropRepaintsKeepInterleaved(t *testing.T) {
	key, _ := crypto.NewSessionKey()
	box, _ := crypto.NewBox(key)
	a := &Agent{box: box, buf: newScrollback(8 << 20), scrollbackLines: 20000}
	a.screen = vt.NewEmulator(70, 20)
	a.screen.Scrollback().SetMaxLines(20000)
	go io.Copy(io.Discard, a.screen)
	a.buf.SetBase(70, 20)

	// Committed history, then a frame that fills the WHOLE screen — like a real
	// inline TUI (verified: Claude homes to ESC[H and repaints the full screen).
	var b strings.Builder
	for i := 1; i <= 40; i++ {
		b.WriteString(fmt.Sprintf("HIST-%04d committed line scrolled off before resizes\r\n", i))
	}
	frame := make([]string, 20)
	for i := range frame {
		frame[i] = fmt.Sprintf("FRAME-%02d frame row words kept short to fit unwrapped", i+1)
	}
	for _, r := range frame {
		b.WriteString(r + "\r\n")
	}
	a.record([]byte(b.String()))

	// repaint re-emits the CURRENT screen content re-wrapped — exactly what a real
	// inline TUI does on SIGWINCH (its frame is the screen; it can't repaint content
	// it no longer shows).
	repaint := func(w int) {
		a.screenMu.Lock()
		cur := strings.Split(a.screen.Render(), "\n")
		a.screenMu.Unlock()
		var rb strings.Builder
		rb.WriteString("\x1b[H")
		for _, r := range cur {
			r = strings.TrimRight(r, " ")
			if r == "" {
				continue
			}
			for _, wr := range nsWrap(r, w) {
				rb.WriteString("\r\x1b[K" + wr + "\n")
			}
		}
		a.record([]byte(rb.String()))
	}

	// Resize 1 (narrower): the repaint stamps a stale re-wrapped copy into scrollback.
	a.resizeScreen(50, 20)
	repaint(50)

	// GENUINE output flows right after — inside what the v2.3.0 band would have
	// swallowed — followed by more output that pushes all of it into scrollback.
	var gb strings.Builder
	gb.WriteString("boundary line absorbing the repaint seam artifact of the synthetic stream\r\n")
	for i := 1; i <= 12; i++ {
		gb.WriteString(fmt.Sprintf("GENUINE-%04d brand new content during the resize burst\r\n", i))
	}
	for i := 1; i <= 20; i++ {
		gb.WriteString(fmt.Sprintf("PAD-%04d later ordinary output pushing things along\r\n", i))
	}
	a.record([]byte(gb.String()))

	// Resize 2 (wider): another full repaint.
	a.resizeScreen(65, 20)
	repaint(65)

	frm, _ := a.snapshotFrame()
	pt, err := box.Decrypt(frm)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	dst := vt.NewEmulator(65, 400)
	dst.Scrollback().SetMaxLines(20000)
	go io.Copy(io.Discard, dst)
	dst.Write(pt)
	rows := strings.Split(strings.ReplaceAll(dst.Render(), "\r\n", "\n"), "\n")

	re := regexp.MustCompile(`(HIST|GENUINE|PAD|FRAME)-\d+`)
	counts := map[string]int{}
	for _, r := range rows {
		for _, m := range re.FindAllString(r, -1) {
			counts[m]++
		}
	}
	for i := 1; i <= 40; i++ {
		if c := counts[fmt.Sprintf("HIST-%04d", i)]; c != 1 {
			t.Errorf("HIST-%04d: %d copies (want exactly 1)", i, c)
		}
	}
	for i := 1; i <= 12; i++ {
		if c := counts[fmt.Sprintf("GENUINE-%04d", i)]; c < 1 {
			t.Errorf("GENUINE-%04d deleted (the v2.3.0 band bug) — must survive", i)
		}
	}
	for i := 1; i <= 20; i++ {
		if c := counts[fmt.Sprintf("PAD-%04d", i)]; c < 1 {
			t.Errorf("PAD-%04d deleted — must survive", i)
		} else if c > 2 {
			t.Errorf("PAD-%04d: %d copies (want <=2: committed + live frame)", i, c)
		}
	}
	frameMax := 0
	for i := 1; i <= 20; i++ {
		if c := counts[fmt.Sprintf("FRAME-%02d", i)]; c > frameMax {
			frameMax = c
		}
	}
	t.Logf("frame maxCopies=%d (repaint stamps should be collapsed)", frameMax)
	if frameMax > 2 {
		t.Errorf("stale resize-repaint copies survived: frame maxCopies=%d (want <=2)", frameMax)
	}
}

// TestResizeSegmentsSnapshotCostAtCap guards the performance envelope: a snapshot
// over a large scrollback with the segment list at its cap must stay fast (it runs
// on reconnect and quiesce-refresh, so tens of milliseconds is the budget).
func TestResizeSegmentsSnapshotCostAtCap(t *testing.T) {
	key, _ := crypto.NewSessionKey()
	box, _ := crypto.NewBox(key)
	a := &Agent{box: box, buf: newScrollback(8 << 20), scrollbackLines: 20000}
	a.screen = vt.NewEmulator(80, 24)
	a.screen.Scrollback().SetMaxLines(20000)
	go io.Copy(io.Discard, a.screen)
	a.buf.SetBase(80, 24)

	// Interleave output bursts with resizes until the segment list is at cap.
	var sb strings.Builder
	for i := 0; i < 4; i++ {
		sb.WriteString(fmt.Sprintf("filler-%04d words to give every captured frame a realistic word count here\r\n", i))
	}
	frame := []byte(sb.String())
	w := 80
	for len(a.resizeSegs) < maxResizeSegs {
		a.record(frame)
		if w == 80 {
			w = 76
		} else {
			w = 80
		}
		a.resizeScreen(uint16(w), 24)
	}
	// Grow the scrollback large but keep it under the cap (at the cap the
	// trim guard purges all segments — separate known limitation).
	for i := 0; i < 500; i++ {
		a.record(frame)
	}
	if len(a.resizeSegs) != maxResizeSegs {
		t.Fatalf("segments purged during setup: %d (scrollback hit its cap?)", len(a.resizeSegs))
	}

	start := time.Now()
	frm, _ := a.snapshotFrame()
	elapsed := time.Since(start)
	if frm == "" {
		t.Fatal("empty snapshot")
	}
	t.Logf("snapshot with %d segments over ~%d scrollback lines took %v",
		len(a.resizeSegs), a.screen.Scrollback().Len(), elapsed)
	budget := 2 * time.Second
	if raceDetector {
		budget *= 8 // instrumented builds run far slower; see raceDetector
	}
	if elapsed > budget {
		t.Errorf("snapshot too slow at segment cap: %v (budget %v)", elapsed, budget)
	}
}
