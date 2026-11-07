// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"reminal/internal/session"
)

// listBackground is the background work a session's harness still has
// running, as `reminal list` says it beside the state: its shells, then each
// other kind by the harness's own name ("1 shell", "2 monitors"), in a steady
// order, and how much there is in all. Work said with no count
// (session.BackgroundWork) reads as itself, and counts as one. What it has
// set to run later ends the list ("1 scheduled"), never counted in n.
func listBackground(a session.Active) (parts []string, n int) {
	if a.Shells > 0 {
		parts = append(parts, fmt.Sprintf("%d shell%s", a.Shells, plural(a.Shells)))
	}
	for _, kind := range slices.Sorted(maps.Keys(a.Other)) {
		switch c := a.Other[kind]; {
		case c <= 0:
		case kind == session.BackgroundWork:
			parts = append(parts, kind)
		default:
			parts = append(parts, fmt.Sprintf("%d %s", c, pluralKind(kind, c)))
		}
	}
	// What it has set to run later, last, and never in n: it is not work
	// running ("done · 1 scheduled" is a finished turn with a job to come).
	if s := session.CleanScheduled(a.Scheduled); s != nil {
		parts = append(parts, fmt.Sprintf("%d scheduled", s.Count))
	}
	return parts, max(a.Shells, 0) + session.CountOther(a.Other)
}

// pluralKind is the name of c of kind: "monitor", "monitors", "background
// processes".
func pluralKind(kind string, c int) string {
	if c == 1 {
		return kind
	}
	for _, end := range []string{"s", "x", "ch", "sh"} {
		if strings.HasSuffix(kind, end) {
			return kind + "es"
		}
	}
	return kind + "s"
}
