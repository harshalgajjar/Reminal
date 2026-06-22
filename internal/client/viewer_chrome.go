package client

import (
	"fmt"
	"os"
)

// cursorColorIsTabScoped reports whether OSC 12 (set cursor color) is scoped
// to the current tab/pane in the user's terminal. macOS Terminal.app applies
// it window-wide, so a green host cursor in tab 1 leaks into a fresh tab 2 —
// confusing because tab 2 isn't running reminal. iTerm2, Alacritty, kitty,
// WezTerm, and ghostty all scope it per-tab. Detect via TERM_PROGRAM.
func cursorColorIsTabScoped() bool {
	return os.Getenv("TERM_PROGRAM") != "Apple_Terminal"
}

// remoteCursorColor is the cursor tint used to signal "you're driving a remote
// shell." Orange contrasts with most light and dark themes.
const remoteCursorColor = "#ff8800"

// hostCursorColor is the cursor tint used on the source agent's terminal so
// the user can tell at a glance which window is THE reminal terminal (vs.
// just a viewer attached to it). Green to suggest "you own this session."
const hostCursorColor = "#3fb950"

// setRemoteIndicator paints two cues so the user can tell at a glance that
// keystrokes are going to another machine: it pushes the current xterm window
// title onto the title stack and sets a new one, and tints the cursor color.
// Sequences are supported by xterm, iTerm2, Terminal.app, Alacritty, kitty,
// and WezTerm.
func setRemoteIndicator(sessionID string) {
	fmt.Fprintf(os.Stdout, "\x1b[22;0t\x1b]0;reminal: %s\x07", sessionID)
	if cursorColorIsTabScoped() {
		fmt.Fprintf(os.Stdout, "\x1b]12;%s\x07", remoteCursorColor)
	}
}

// clearRemoteIndicator reverses setRemoteIndicator: resets cursor color and
// pops the previous title off the stack.
func clearRemoteIndicator() {
	if cursorColorIsTabScoped() {
		fmt.Fprint(os.Stdout, "\x1b]112\x07")
	}
	fmt.Fprint(os.Stdout, "\x1b[23;0t")
}

// setHostIndicator mirrors setRemoteIndicator but for the agent's source
// terminal: green cursor + "reminal: <id> (host)" window title. Lets the
// user spot which window is the source reminal vs. just a viewer attached
// to it.
func setHostIndicator(sessionID string) {
	fmt.Fprintf(os.Stdout, "\x1b[22;0t\x1b]0;reminal: %s (host)\x07", sessionID)
	if cursorColorIsTabScoped() {
		fmt.Fprintf(os.Stdout, "\x1b]12;%s\x07", hostCursorColor)
	}
}

// clearHostIndicator reverses setHostIndicator.
func clearHostIndicator() {
	if cursorColorIsTabScoped() {
		fmt.Fprint(os.Stdout, "\x1b]112\x07")
	}
	fmt.Fprint(os.Stdout, "\x1b[23;0t")
}
