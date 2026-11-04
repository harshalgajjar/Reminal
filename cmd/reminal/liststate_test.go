// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import "testing"

// On a narrow terminal the shell count folds into the state word, and the row
// must stay the width it was: the state column is padded, never truncated, so
// anything longer than stateCol pushes the whole row out and wraps on a phone.
func TestNarrowStateWithBackground(t *testing.T) {
	cases := []struct {
		state  string
		shells int
		want   string
	}{
		{"done", 0, "done"},              // nothing running: untouched
		{"done", 1, "done ·1"},           // the case this exists for
		{"working", 4, "working ·4"},     // 10 wide, fits
		{"needs you", 9, "needs you ·9"}, // 12 wide, exactly the budget
		{"idle 3d", 12, "idle 3d ·12"},
		// Past the budget: the count is dropped rather than the row widened.
		{"needs you", 12, "needs you"},
		{"logged out", 1, "logged out"},
		{"logged out", 99, "logged out"},
	}
	for _, c := range cases {
		got := narrowStateWithBackground(c.state, c.shells)
		if got != c.want {
			t.Errorf("narrowStateWithBackground(%q, %d) = %q, want %q", c.state, c.shells, got, c.want)
		}
		if visLen(got) > stateCol {
			t.Errorf("narrowStateWithBackground(%q, %d) = %q is %d wide, over the %d-column budget",
				c.state, c.shells, got, visLen(got), stateCol)
		}
	}
}
