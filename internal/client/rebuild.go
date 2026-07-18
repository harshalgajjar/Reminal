// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/vt"
)

// vviewWriter feeds a recorded output stream into a TALL replay emulator while
// translating absolute row addressing into a sliding "virtual viewport".
//
// Apps address rows assuming the terminal is `rows` tall: `ESC[H` means "my
// viewport's top-left", `ESC[2J` means "my whole screen". On the tall replay
// emulator those rows are ABSOLUTE — a resize repaint homing to row 1 would
// overwrite the oldest history instead of its own previous render. So the
// writer models the viewport a real terminal would show: rows
// [vtop, vtop+rows). Row-addressed sequences are shifted by +vtop, which makes
// every repaint land exactly on the render it replaces — each history line
// exists once, by construction.
//
// vtop moves the way a real terminal's content scrolls:
//   - FLOW advances it: a linefeed or wrap that carries the cursor past the
//     viewport's bottom row is what scrolling is; the viewport slides down.
//   - Absolute addressing NEVER advances it: CUP/VPA to the bottom row (status
//     lines, toasts) cannot scroll a real terminal, so it must not slide the
//     viewport either — that was measurably drifting the anchor.
//   - A height change re-anchors the BOTTOM: shrinking pushes the viewport top
//     down, growing pulls it up (what a bottom-anchored terminal shows).
//
// The stream is split at the row-addressing sequences (CUP/HVP, VPA, CUU/CUD,
// CPL/CNL, ED, DECSTBM); everything between is fed to the emulator verbatim,
// and the emulator's own cursor is queried at each split point — so wrapping,
// wide runes and relative motion are all handled by the real parser, never
// re-implemented.
type vviewWriter struct {
	e    *vt.Emulator
	rows int // the app's believed viewport height (lockstep with resize markers)
	vtop int // 0-indexed top row of the virtual viewport
	// alt tracks whether the stream is on the alternate screen (vim, htop…).
	// The alt buffer has no scrollback and is genuinely absolutely addressed,
	// so translation stands down entirely until the app switches back.
	alt bool
	// tall is the replay emulator's height, used when applying deferred
	// width changes.
	tall int
	// pendCols/pendRows hold a geometry change recorded in the buffer but not
	// yet adopted by the app. The marker lands at PTY-resize time; the app's
	// frames already in flight — and everything until it handles SIGWINCH —
	// are still built for the OLD geometry. Translating those against the new
	// size walks the viewport over rows they never paint (scattered,
	// duplicated history). So a marker only arms the change here; it applies
	// at the app's first full repaint (home/clear/region signature) or after
	// pendMaxAge chunks as a fallback for apps that never repaint (plain
	// shells barely use absolute addressing, so late is harmless there).
	pendCols, pendRows int
	pendAge            int
	// carry holds a CSI split across Write calls (recorded chunks are ~4KB) so
	// a row-addressed sequence straddling a boundary is still translated.
	carry []byte
}

// sync samples the emulator cursor, advances vtop if flow carried the cursor
// past the viewport bottom since the last split point, and returns the
// viewport top plus the cursor. Between split points the cursor only moves
// down via flow (prints, linefeeds, wraps) — every upward or absolute move is
// intercepted — so any excursion past the bottom seen here IS a scroll.
func (w *vviewWriter) sync() (vtop, cy, cx int) {
	pos := w.e.CursorPosition()
	cy, cx = pos.Y, pos.X
	if cy > w.vtop+w.rows-1 {
		w.vtop = cy - w.rows + 1
	}
	return w.vtop, cy, cx
}

// pendMaxAge is the fallback: apply a pending geometry change after this many
// Write chunks even without a repaint signature.
const pendMaxAge = 4

// setGeometry arms a recorded geometry change; it takes effect at the app's
// first post-resize repaint (see pendCols).
func (w *vviewWriter) setGeometry(cols, rows int) {
	if cols == w.e.Width() && rows == w.rows {
		w.pendCols, w.pendRows = 0, 0 // resized back before the app noticed
		return
	}
	w.pendCols, w.pendRows, w.pendAge = cols, rows, 0
}

// applyPending adopts an armed geometry change.
func (w *vviewWriter) applyPending() {
	if w.pendCols == 0 && w.pendRows == 0 {
		return
	}
	cols, rows := w.pendCols, w.pendRows
	w.pendCols, w.pendRows = 0, 0
	if cols > 0 && cols != w.e.Width() {
		w.e.Resize(cols, w.tall)
	}
	if rows > 0 {
		w.setRows(rows)
	}
}

// setRows applies a viewport height change, keeping the BOTTOM anchored the
// way a real terminal does: shrinking hides rows at the top, growing reveals
// older rows above.
func (w *vviewWriter) setRows(rows int) {
	if rows <= 0 || rows == w.rows {
		w.rows = max(rows, 1)
		return
	}
	if !w.alt {
		w.sync() // account for flow since the last split point at the OLD height
		w.vtop += w.rows - rows
		if w.vtop < 0 {
			w.vtop = 0
		}
	}
	w.rows = rows
}

// Base finalizes and returns the virtual viewport's top row: everything above
// it is history, [Base, Base+rows) is the current screen. Samples the cursor a
// last time so trailing plain output (no CSI after it) still counts.
func (w *vviewWriter) Base() int {
	if !w.alt {
		w.sync()
	}
	return w.vtop
}

// Write replays p through the emulator with virtual-viewport translation.
func (w *vviewWriter) Write(p []byte) {
	if len(w.carry) > 0 {
		p = append(w.carry, p...)
		w.carry = nil
	}
	if w.pendCols != 0 || w.pendRows != 0 {
		if w.pendAge++; w.pendAge > pendMaxAge {
			w.applyPending()
		}
	}
	var out strings.Builder
	flush := func() {
		if out.Len() > 0 {
			_, _ = w.e.Write([]byte(out.String()))
			out.Reset()
		}
	}
	i := 0
	for i < len(p) {
		// Find the next CSI introducer.
		j := i
		for j < len(p) {
			if p[j] == 0x1b && (j+1 >= len(p) || p[j+1] == '[') {
				break
			}
			j++
		}
		out.Write(p[i:j])
		if j >= len(p) {
			break
		}
		if j+1 >= len(p) {
			// Bare ESC at the chunk edge — might be the start of a CSI.
			w.carry = append(w.carry, p[j:]...)
			break
		}
		// Parse the CSI: ESC [ [?] params... final. Bail verbatim on anything
		// unterminated or with intermediate markers we don't touch.
		k := j + 2
		private := k < len(p) && p[k] == '?'
		if private {
			k++
		}
		ps := k
		for k < len(p) && (p[k] == ';' || (p[k] >= '0' && p[k] <= '9')) {
			k++
		}
		if k >= len(p) {
			// CSI runs off the chunk edge: hold it for the next Write.
			w.carry = append(w.carry, p[j:]...)
			break
		}
		if p[k] < '@' || p[k] > '~' {
			// Non-simple (intermediates, other private markers): pass through;
			// the rest of the sequence follows as plain bytes, contiguously.
			out.Write(p[j : k+1])
			i = k + 1
			continue
		}
		final := p[k]
		params := parseCSIParams(p[ps:k])
		if private {
			// Track alt-screen switches; translation stands down on the alt
			// buffer (no scrollback there, addressing is genuinely absolute).
			if final == 'h' || final == 'l' {
				for _, m := range params {
					if m == 1049 || m == 1047 || m == 47 {
						w.alt = final == 'h'
					}
				}
			}
			out.Write(p[j : k+1])
			i = k + 1
			continue
		}
		if w.alt {
			out.Write(p[j : k+1])
			i = k + 1
			continue
		}
		switch final {
		case 'H', 'f': // CUP/HVP: viewport row, clamped to it (CUP can't scroll)
			flush()
			r, c := csiParam(params, 0, 1), csiParam(params, 1, 1)
			if r == 1 {
				// Homing starts a full repaint — the first frame built for a
				// newly-adopted size. Apply any armed geometry change now.
				w.applyPending()
			}
			vtop, _, _ := w.sync()
			if r > w.rows {
				r = w.rows
			}
			fmt.Fprintf(&out, "\x1b[%d;%dH", vtop+r, c)
		case 'd': // VPA: same
			flush()
			vtop, _, _ := w.sync()
			r := csiParam(params, 0, 1)
			if r > w.rows {
				r = w.rows
			}
			fmt.Fprintf(&out, "\x1b[%dd", vtop+r)
		case 'r': // DECSTBM: scroll region rows are viewport-relative too
			flush()
			w.applyPending() // region asserts accompany full repaints
			vtop, _, _ := w.sync()
			top := csiParam(params, 0, 1)
			bot := csiParam(params, 1, w.rows)
			if top == 1 && bot >= w.rows {
				// Full-viewport region == "reset margins". Map to the full
				// tall region: a bounded region pinned at today's vtop would
				// swallow (not scroll off) lines once the viewport slides.
				out.WriteString("\x1b[r")
			} else {
				fmt.Fprintf(&out, "\x1b[%d;%dr", vtop+top, vtop+bot)
			}
			// DECSTBM homes the cursor; home is the viewport's top, not row 1.
			fmt.Fprintf(&out, "\x1b[%d;1H", vtop+1)
		case 'A', 'F': // CUU / CPL: clamp at the viewport top, not row 0
			flush()
			vtop, cy, _ := w.sync()
			n := csiParam(params, 0, 1)
			if up := cy - vtop; n > up {
				n = up
			}
			if n > 0 {
				fmt.Fprintf(&out, "\x1b[%dA", n)
			}
			if final == 'F' {
				out.WriteString("\r")
			}
		case 'B', 'E': // CUD / CNL: clamp at the viewport bottom (they can't scroll)
			flush()
			vtop, cy, _ := w.sync()
			n := csiParam(params, 0, 1)
			if down := vtop + w.rows - 1 - cy; n > down {
				n = down
			}
			if n > 0 {
				fmt.Fprintf(&out, "\x1b[%dB", n)
			}
			if final == 'E' {
				out.WriteString("\r")
			}
		case 'J': // ED: constrain to the viewport
			flush()
			mode := csiParam(params, 0, 0)
			if mode >= 2 {
				w.applyPending() // whole-screen clears accompany full repaints
			}
			vtop, cy, cx := w.sync()
			switch mode {
			case 0:
				// Below-cursor: everything under the viewport is blank or
				// stale overshoot on the tall screen, so the untranslated
				// form erases at least as much as intended, all of it unseen.
				out.WriteString("\x1b[J")
			case 1, 2:
				// Above-cursor / whole-screen: blank the viewport rows
				// line-by-line, then restore the cursor (per spec ED does not
				// move it).
				top := vtop
				bot := cy
				if mode == 2 {
					bot = vtop + w.rows - 1
				}
				for r := top; r <= bot; r++ {
					fmt.Fprintf(&out, "\x1b[%d;1H\x1b[2K", r+1)
				}
				fmt.Fprintf(&out, "\x1b[%d;%dH", cy+1, cx+1)
			case 3:
				// Viewport + scrollback: on the tall replay "scrollback" is
				// everything above the viewport too — a genuine full wipe.
				flush()
				_, _ = w.e.Write([]byte("\x1b[2J\x1b[H"))
				w.e.ClearScrollback()
				w.vtop = 0
			default:
				out.Write(p[j : k+1])
			}
		default:
			out.Write(p[j : k+1])
		}
		i = k + 1
	}
	flush()
}

// parseCSIParams splits "12;3" into ints; empty params become 0.
func parseCSIParams(b []byte) []int {
	if len(b) == 0 {
		return nil
	}
	parts := strings.Split(string(b), ";")
	out := make([]int, len(parts))
	for i, s := range parts {
		n := 0
		for _, ch := range s {
			if ch < '0' || ch > '9' {
				return nil
			}
			n = n*10 + int(ch-'0')
		}
		out[i] = n
	}
	return out
}

// csiParam returns params[i], with def for missing or zero entries.
func csiParam(params []int, i, def int) int {
	if i >= len(params) || params[i] == 0 {
		return def
	}
	return params[i]
}
