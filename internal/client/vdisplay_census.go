// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import "strings"

// The census's arithmetic, apart from the macOS calls that feed it
// (vdisplay_darwin.go), so it is tested everywhere.

// vdisplayName must match the descriptor name in reminal-capture's vdisplay
// subcommand — it's how the census tells our software display from real ones.
const vdisplayName = "reminal"

// ours reports whether s is our software display. Whatever its case: macOS 13
// lists the "reminal" display as "Reminal", and counted as real it was taken
// down on the next look.
func (s censusScreen) ours() bool { return strings.EqualFold(s.name, vdisplayName) }

// censusScreen is one screen as the census sees it.
type censusScreen struct {
	name    string
	w, h    int
	builtin bool // the MacBook's own panel
}

// censusLid is the MacBook lid as the census sees it.
type censusLid struct {
	has  bool // this Mac has one
	shut bool
}

// countRealScreens counts the screens that are real: not ours, and not the
// built-in panel while the lid is shut. An open lid is a real display however
// the screens are listed: macOS 13 has been seen listing only ours once it is
// up, the other display mirrored behind it, and ours must still go when the
// lid opens. w, h is the first real one's size, or, with none, the size of
// the first screen of any kind.
func countRealScreens(screens []censusScreen, lid censusLid) (real, w, h int) {
	for _, s := range screens {
		if s.ours() {
			continue
		}
		if w == 0 {
			w, h = s.w, s.h
		}
		if s.builtin && lid.shut {
			continue
		}
		if real == 0 {
			w, h = s.w, s.h
		}
		real++
	}
	if real == 0 && lid.has && !lid.shut {
		real = 1
	}
	return real, w, h
}
