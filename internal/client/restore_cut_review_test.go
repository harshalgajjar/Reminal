// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"fmt"
	"strings"
	"testing"
)

// At a restart, the history goes back up to where the ring's replay starts
// (ringStartIn), and the replay draws the rest. No row below the cut may be
// one the replay does not draw as it is: a row lost there is shown nowhere.
// Each shape is one review found lost; "again" carries the replay's screen
// rows, as restoreResumedScrollbackLocked's does, while the history may not
// (a dump taken on the alternate screen keeps none of them).
func TestRestoreCutLosesNoRow(t *testing.T) {
	lines := func(p string, n int) []string {
		var o []string
		for i := 1; i <= n; i++ {
			o = append(o, fmt.Sprintf("%s line %d of the session with some words", p, i))
		}
		return o
	}
	cat := func(xs ...[]string) []string {
		var o []string
		for _, x := range xs {
			o = append(o, x...)
		}
		return o
	}
	banner := []string{"Welcome to the build box", "Type help for the commands", "$ make test"}
	screen := func(n int) []string {
		o := make([]string, n)
		o[0] = "$ "
		return o
	}
	session := lines("S", 30)
	shell := []string{
		"ok      reminal/internal/api 0.512s",
		"$ git push origin main",
		"Everything up-to-date",
		"nothing to commit, working tree clean",
	}
	for _, c := range []struct {
		name           string
		history, again []string
	}{
		{"an older block far above", cat(banner, lines("O", 200), banner, session), cat(banner, session, screen(40))},
		{"an older block 10 lines above", cat(banner, lines("O", 10), banner, session), cat(banner, session, screen(40))},
		{"an older block 40 lines above", cat(banner, lines("O", 40), banner, session), cat(banner, session, screen(40))},
		{"the session's start pruned from the history", cat(banner, lines("O", 10), session[20:]), cat(banner, session, screen(40))},
		{"the ring's start drawn otherwise in the history", cat(banner, lines("O", 10), []string{"Welcome to the build box Type help", "for the commands", "$ make test"}, session), cat(banner, session, screen(40))},
		// A row whose start and rest are each shown, in unrelated places
		// ("$ make" of "$ make test", "clean" of "working tree clean"), is
		// not shown: on its own above the ring, with the history short of a
		// screen, it was cut off.
		{"a row spliced from two unrelated places", cat(banner, []string{"$ make clean"}, banner, shell), cat(banner, shell, screen(4))},
	} {
		cut := ringStartIn(c.history, c.again)
		shown := map[string]bool{}
		for _, l := range c.again {
			shown[strings.TrimSpace(l)] = true
		}
		for _, l := range c.history[cut:] {
			if r := strings.TrimSpace(l); r != "" && !shown[r] {
				t.Errorf("%s: cut at %d of %d, %q below it is drawn by nothing", c.name, cut, len(c.history), r)
			}
		}
	}
}
