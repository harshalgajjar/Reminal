// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/vt"
)

// trimHistorySeam drops history's tail where it reproduces the live screen's
// leading rows. It tries the largest overlap first (the true seam is the
// screen's entire non-blank content), falling back to smaller overlaps if
// output raced between the history rebuild and the screen read. No match
// leaves history untouched.
func trimHistorySeam(history, screen []string) []string {
	live := make([]string, 0, len(screen))
	for _, r := range screen {
		live = append(live, strings.TrimRight(r, " "))
	}
	for len(live) > 0 && live[len(live)-1] == "" {
		live = live[:len(live)-1]
	}
	if len(live) == 0 || len(history) == 0 {
		return history
	}
	max := len(live)
	if max > len(history) {
		max = len(history)
	}
	for k := max; k >= 1; k-- {
		match := true
		for i := 0; i < k; i++ {
			if history[len(history)-k+i] != live[i] {
				match = false
				break
			}
		}
		if match {
			return history[:len(history)-k]
		}
	}
	return history
}

// renderScrollback renders up to maxLines of e's scrollback into plain-text
// lines (newest kept when over the cap; maxLines <= 0 means no cap). The
// caller must hold whatever lock guards e.
func renderScrollback(e *vt.Emulator, maxLines int) []string {
	lines := e.Scrollback().Lines()
	if maxLines > 0 && len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	rendered := make([]string, len(lines))
	for i, ln := range lines {
		rendered[i] = strings.TrimRight(ln.Render(), " ")
	}
	return rendered
}

// buildSnapshot serializes the emulator's current screen plus the given
// pre-rendered history into a self-contained paint sequence that reconstructs
// the view in a freshly-attached viewer instantly — no fast-forward replay of
// the raw output history. History goes into the viewer's scrollback; the
// viewer lands at the bottom (the live screen) and can scroll up through it.
//
// history is pre-rendered (see renderScrollback / Agent.rebuildView) so the
// caller chooses its source: the agent rebuilds it from the raw output buffer
// at current geometry, which avoids the duplicate-transcript scars a live
// incrementally-resized emulator accumulates from inline-TUI re-renders.
// maxBytes caps the rendered history size (newest lines kept; 0 = no cap).
// The visible screen is always included.
//
// screenRows, when non-nil, is the screen carved from the SAME rebuild as
// history (the replay's virtual viewport) — history and screen then join with
// no overlap by construction and no seam heuristic runs. When nil the screen
// is rendered from the live emulator and the history tail is seam-cut against
// it (fallback path, and alt-screen sessions where the app owns the view).
//
// reserveLastRow leaves the bottom row BLANK in the painted screen (used when an
// always-on status bar owns that row): the bar is transient and live-drawn, so
// baking it into the snapshot would strand it as a ghost on a stale row after a
// resize. The live reassert repaints the real bar. Ignored on the alt screen.
//
// The caller must hold the emulator lock for the duration (the emulator is read
// across several calls here and must not be mutated mid-snapshot).
func buildSnapshot(e *vt.Emulator, history, screenRows []string, maxBytes int, reserveLastRow bool) string {
	alt := e.IsAltScreen()
	width, height := e.Width(), e.Height()
	if alt {
		// The full-screen app owns the view; the rebuild only reconstructs the
		// main buffer behind it, which is already folded into history.
		screenRows = nil
	}

	// Byte budget: walk newest→oldest, keep lines until we'd exceed
	// maxBytes, then drop the older remainder.
	if maxBytes > 0 && len(history) > 0 {
		total, start := 0, len(history)
		for i := len(history) - 1; i >= 0; i-- {
			total += len(history[i]) + 2 // +CRLF
			if total > maxBytes {
				break
			}
			start = i
		}
		history = history[start:]
	}

	// Exactly `height` rows for the screen, padded if the source omits
	// trailing blank lines, so the viewport lines up precisely.
	fromRebuild := screenRows != nil
	var screen []string
	if fromRebuild {
		screen = append([]string(nil), screenRows...)
	} else {
		screen = strings.Split(e.Render(), "\n")
	}
	if len(screen) > height {
		screen = screen[:height]
	}
	for len(screen) < height {
		screen = append(screen, "")
	}

	if !fromRebuild {
		// Seam-cut: a rebuilt history replays the same bytes the live screen
		// came from, so it may END with content the screen below paints again.
		// Trim that overlap or a (re)join shows the current screen twice in a
		// row. Unnecessary when the screen came from the same rebuild as the
		// history — the two are disjoint by construction.
		history = trimHistorySeam(history, screen)
	}
	// Blank the status-bar row so the snapshot never carries a transient bar (see
	// reserveLastRow). Main screen only — the alt buffer has no reserved row.
	if reserveLastRow && !alt && len(screen) > 0 {
		screen[len(screen)-1] = ""
	}

	// Height-tolerance: with no scrollback history to paint, drop trailing blank
	// screen rows before emitting. The row loop below paints its rows as a run of
	// `\r\n`-separated lines from the home position, so emitting all `height` rows
	// fills the viewport to the last line — and if the joining viewer's terminal
	// is even one row SHORTER than `height` at the instant it applies this paint
	// (a common transient while its size settles on connect), that overflow
	// scrolls the top rows — the prompt — up into scrollback, where they stay:
	// the viewer lands on a blank screen until the next keystroke forces a repaint.
	// The trailing rows are blank (the screen was already cleared), so not painting
	// them is visually identical while making the paint immune to a height
	// undershoot. Only safe with no history: when history IS present those trailing
	// rows are what push the live screen down into the viewport (history scrolls up
	// into scrollback), so keep the full-height paint then.
	if len(history) == 0 {
		for len(screen) > 0 && strings.TrimRight(screen[len(screen)-1], " ") == "" {
			screen = screen[:len(screen)-1]
		}
	}

	var b strings.Builder
	b.Grow(len(history)*width + height*width + 64)
	b.WriteString("\x1b[?25l")            // hide cursor while painting
	b.WriteString("\x1b[?1049l")          // ensure the main screen is active
	b.WriteString("\x1b[2J\x1b[3J\x1b[H") // clear screen + the viewer's old scrollback, home
	b.WriteString("\x1b[?7l")             // disable autowrap so full-width rows don't wrap

	// History lines flow into the main buffer's scrollback as the screen
	// scrolls; each is newline-terminated so the next pushes it up.
	for _, ln := range history {
		b.WriteString(ln)
		b.WriteString("\x1b[0m\r\n")
	}

	if alt {
		// A full-screen app (vim, claude, …) is on the alternate screen. The
		// history above went into the main buffer (visible after the app
		// exits); now switch to the alt buffer and paint the live screen there.
		b.WriteString("\x1b[?1049h\x1b[2J\x1b[H")
	}
	for i, row := range screen {
		if i > 0 {
			b.WriteString("\r\n")
		}
		b.WriteString(row)
		b.WriteString("\x1b[0m")
	}

	b.WriteString("\x1b[?7h") // restore autowrap
	pos := e.CursorPosition()
	fmt.Fprintf(&b, "\x1b[%d;%dH", pos.Y+1, pos.X+1) // restore cursor (1-based)
	b.WriteString("\x1b[?25h")                       // show cursor
	return b.String()
}
