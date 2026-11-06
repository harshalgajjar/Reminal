// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

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
		parts = append(parts, scheduledPart(s, time.Now()))
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

// scheduledStale is how long after its harness said them a count of jobs set
// to run later is said with when that was: a job may have fired, or been
// added, since.
const scheduledStale = time.Hour

// scheduledPart is how `reminal list` says jobs set to run later: "2
// scheduled", and, once its harness said them a while ago, when ("2
// scheduled (as of 14:05)"; a date too past a day).
func scheduledPart(s *session.Scheduled, now time.Time) string {
	part := fmt.Sprintf("%d scheduled", s.Count)
	if s.At.IsZero() || now.Sub(s.At) < scheduledStale {
		return part
	}
	layout := "15:04"
	if now.Sub(s.At) >= 24*time.Hour {
		layout = "Jan 2 15:04"
	}
	return part + " (as of " + s.At.Local().Format(layout) + ")"
}
