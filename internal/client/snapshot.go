package client

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/vt"
)

// buildSnapshot serializes the emulator's current state into a self-contained
// paint sequence that reconstructs the screen in a freshly-attached viewer
// instantly — no fast-forward replay of the raw output history. It includes
// scrollback so the viewer lands at the bottom (the live screen) and can scroll
// up through history.
//
// The scrollback is bounded two ways so a long-lived or noisy session can't
// ship a huge snapshot: at most maxLines lines, and within that at most
// maxBytes of rendered text (the newest lines are kept). Either bound at 0
// means "unlimited" for that dimension. The visible screen is always included.
//
// The caller must hold the emulator lock for the duration (the emulator is read
// across several calls here and must not be mutated mid-snapshot).
func buildSnapshot(e *vt.Emulator, maxLines, maxBytes int) string {
	alt := e.IsAltScreen()
	width, height := e.Width(), e.Height()

	var history []string
	if maxLines != 0 {
		lines := e.Scrollback().Lines()
		if maxLines > 0 && len(lines) > maxLines {
			lines = lines[len(lines)-maxLines:]
		}
		rendered := make([]string, len(lines))
		for i, ln := range lines {
			rendered[i] = strings.TrimRight(ln.Render(), " ")
		}
		// Byte budget: walk newest→oldest, keep lines until we'd exceed
		// maxBytes, then drop the older remainder.
		if maxBytes > 0 {
			total, start := 0, len(rendered)
			for i := len(rendered) - 1; i >= 0; i-- {
				total += len(rendered[i]) + 2 // +CRLF
				if total > maxBytes {
					break
				}
				start = i
			}
			rendered = rendered[start:]
		}
		history = rendered
	}

	// Exactly `height` rows for the live screen, padded if Render() omits
	// trailing blank lines, so the viewport lines up precisely.
	screen := strings.Split(e.Render(), "\n")
	if len(screen) > height {
		screen = screen[:height]
	}
	for len(screen) < height {
		screen = append(screen, "")
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
