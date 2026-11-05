// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import "testing"

// The built-in panel behind a shut lid is not a real display, so closed-lid
// mode makes its own; open, it counts again, even when macOS lists only ours.
// Ours never counts, whatever its case.
func TestCountRealScreens(t *testing.T) {
	builtin := censusScreen{name: "Built-in Retina Display", w: 1728, h: 1117, builtin: true}
	monitor := censusScreen{name: "DELL U2720Q", w: 2560, h: 1440}
	ours := censusScreen{name: vdisplayName, w: 1728, h: 1117}
	oursOn13 := censusScreen{name: "Reminal", w: 1728, h: 1117} // as macOS 13 lists it
	shut, open, noLid := censusLid{has: true, shut: true}, censusLid{has: true}, censusLid{}
	for _, c := range []struct {
		name        string
		screens     []censusScreen
		lid         censusLid
		real, wantW int
	}{
		{"lid open, built-in only", []censusScreen{builtin}, open, 1, 1728},
		{"lid shut, built-in only", []censusScreen{builtin}, shut, 0, 1728},
		{"lid shut, ours made", []censusScreen{builtin, ours}, shut, 0, 1728},
		{"lid shut, ours as macOS 13 names it", []censusScreen{builtin, oursOn13}, shut, 0, 1728},
		{"lid opened again, ours still up", []censusScreen{builtin, ours}, open, 1, 1728},
		{"lid opened again, only ours listed", []censusScreen{oursOn13}, open, 1, 0},
		{"lid shut, a monitor attached", []censusScreen{builtin, monitor}, shut, 1, 2560},
		{"no lid, a monitor", []censusScreen{monitor}, noLid, 1, 2560},
		{"no lid, only ours", []censusScreen{ours}, noLid, 0, 0},
		{"nothing", nil, shut, 0, 0},
	} {
		real, w, _ := countRealScreens(c.screens, c.lid)
		if real != c.real || w != c.wantW {
			t.Errorf("%s: real %d width %d, want %d and %d", c.name, real, w, c.real, c.wantW)
		}
	}
}
