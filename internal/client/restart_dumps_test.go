// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"reminal/internal/crypto"
)

// Two sessions' restart dumps, as their agents wrote them (testdata/restart,
// an account address replaced by one of the same length). Both rings hold the
// session's first output:
//   - young: the ring draws all of the history, its first lines otherwise
//     (a notice where the replay has the welcome box); the history came
//     back twice;
//   - restored: the history holds lines an older restart put back above the
//     ring (a welcome box at another width), which the ring does not draw.
//
// Each comes back once, with the text of every row of its history.
func TestRealRestartDumpsComeBackOnceAndWhole(t *testing.T) {
	for _, c := range []struct {
		file  string
		loose int // rows of the history allowed missing: a garbled row a redraw left
	}{
		{"young-restart.dump.json.gz", 1},
		{"restored-restart.dump.json.gz", 0},
	} {
		d := readDumpFixture(t, "testdata/restart/"+c.file)
		key, _ := crypto.NewSessionKey()
		box, _ := crypto.NewBox(key)
		a := &Agent{box: box, buf: newScrollback(scrollbackBytes), scrollbackLines: 10000}
		a.screen = testScreen(t, d.BaseCols, d.BaseRows)
		a.screen.SetMaxLines(a.scrollbackLines)
		a.resumeDump = readDumpFixture(t, "testdata/restart/"+c.file)
		a.restoreResumedScrollback()
		a.screenMu.Lock()
		after := append(a.screen.History(), renderedScreenRows(a.screen)...)
		a.screenMu.Unlock()

		text := func(ls []string) string {
			var b []string
			for _, l := range ls {
				b = append(b, strings.Join(strings.Fields(stripANSI(l)), " "))
			}
			return " " + strings.Join(b, " ") + " "
		}
		before, have := text(d.History), text(after)
		for _, s := range []string{"Welcome to Claude Code", "III. The Storm", "Mara saw it from the kitchen", "IV. The Ship"} {
			if got, want := strings.Count(have, s), strings.Count(before, s); got != want {
				t.Errorf("%s: %q %d times after the restart, %d before", c.file, s, got, want)
			}
		}
		var missing []string
		for _, l := range d.History {
			if w := strings.Join(strings.Fields(stripANSI(l)), " "); w != "" && !strings.Contains(have, " "+w+" ") {
				missing = append(missing, w)
			}
		}
		if len(missing) > c.loose {
			t.Errorf("%s: %d rows of the history no longer shown: %q", c.file, len(missing), missing)
		}
	}
}

func readDumpFixture(t *testing.T, path string) *scrollbackDump {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(z)
	if err != nil {
		t.Fatal(err)
	}
	var d scrollbackDump
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Entries) == 0 || d.Entries[0].Seq != 1 {
		t.Fatalf("%s: want a ring from the session's first output", path)
	}
	return &d
}
