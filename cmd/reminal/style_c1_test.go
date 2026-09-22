// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"strings"
	"testing"
)

// Text that came from somewhere else — a session's name, a title a program
// set — is printed to this terminal, so it must not be able to act on it.
// Stripping ESC is not enough: 0x9B is CSI to a terminal reading C1 in UTF-8,
// and the direction overrides make a name read as something it is not.
func TestCleanTermStripsC1AndDirectionOverrides(t *testing.T) {
	c := func(r rune) string { return string(r) }
	for _, bad := range []string{c(0x1b), c(0x07), c(0x9b), c(0x7f), c(0x202e), c(0x200f), c(0x2066)} {
		in := "name" + bad + "2Jrest"
		if got := cleanTerm(in); strings.Contains(got, bad) {
			t.Errorf("cleanTerm(%q) kept %q: %q", in, bad, got)
		}
	}
	if got := cleanTerm("ordinary-name_1"); got != "ordinary-name_1" {
		t.Errorf("cleanTerm changed ordinary text: %q", got)
	}
}
