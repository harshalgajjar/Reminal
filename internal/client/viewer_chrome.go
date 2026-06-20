package client

import (
	"fmt"
	"os"
)

// remoteCursorColor is the cursor tint used to signal "you're driving a remote
// shell." Orange contrasts with most light and dark themes.
const remoteCursorColor = "#ff8800"

// setRemoteIndicator paints two cues so the user can tell at a glance that
// keystrokes are going to another machine: it pushes the current xterm window
// title onto the title stack and sets a new one, and tints the cursor color.
// Sequences are supported by xterm, iTerm2, Terminal.app, Alacritty, kitty,
// and WezTerm.
func setRemoteIndicator(sessionID string) {
	fmt.Fprintf(os.Stdout,
		"\x1b[22;0t"+ // push window+icon title onto stack
			"\x1b]0;reminal: %s\x07"+ // set new window+icon title
			"\x1b]12;%s\x07", // set cursor color
		sessionID, remoteCursorColor)
}

// clearRemoteIndicator reverses setRemoteIndicator: resets cursor color and
// pops the previous title off the stack.
func clearRemoteIndicator() {
	fmt.Fprint(os.Stdout,
		"\x1b]112\x07"+ // reset cursor color to terminal default
			"\x1b[23;0t") // pop window+icon title from stack
}
