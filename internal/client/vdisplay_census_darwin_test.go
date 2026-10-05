// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import "testing"

// A closed lid's built-in panel, still listed by NSScreen, is not a display:
// counting it left a closed-lid Mac with no virtual display (Harshal's Mac,
// 2026-10-04). Asleep or inactive displays do not count either.
func TestParseCensusLidClosed(t *testing.T) {
	builtin := "Built-in Retina Display\t1728\t1117\t1\t0\t1"
	external := "DELL U2720Q\t2560\t1440\t0\t0\t1"
	ours := "reminal\t1920\t1080\t0\t0\t1"
	cases := []struct {
		name      string
		out       string
		lidClosed bool
		real, w   int
	}{
		{"lid open, built-in only", builtin, false, 1, 1728},
		{"lid closed, built-in only (the bug)", builtin, true, 0, 0},
		{"lid closed, external attached", builtin + "\n" + external, true, 1, 2560},
		{"our virtual display never counts", ours, true, 0, 0},
		{"asleep display", "Studio Display\t2560\t1440\t0\t1\t1", false, 0, 0},
		{"inactive display", "Studio Display\t2560\t1440\t0\t0\t0", false, 0, 0},
		{"old three-field line, lid open", "Built-in Retina Display\t1728\t1117", false, 1, 1728},
		{"old three-field line, lid closed", "Built-in Retina Display\t1728\t1117", true, 0, 0},
		{"flags unknown (bridge failed)", "Built-in Retina Display\t1728\t1117\t\t\t", false, 1, 1728},
	}
	for _, c := range cases {
		real, w, _ := parseCensus(c.out, c.lidClosed)
		if real != c.real || w != c.w {
			t.Errorf("%s: real=%d w=%d, want real=%d w=%d", c.name, real, w, c.real, c.w)
		}
	}
}

func TestParseClamshell(t *testing.T) {
	yes := `+-o IOPMrootDomain  <class IOPMrootDomain>
    {
      "AppleClamshellState" = Yes
      "AppleClamshellCausesSleep" = No
    }`
	no := `      "AppleClamshellState" = No`
	if !parseClamshell(yes) || parseClamshell(no) || parseClamshell("") {
		t.Fatal("clamshell parsing")
	}
}
