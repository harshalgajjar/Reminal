// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The restore log keeps one line per event, with nothing in it a terminal
// would act on, and starts again past its bound, the last one kept.
func TestTheRestoreLogKeepsLinesAndStaysBounded(t *testing.T) {
	h := isolateHome(t)
	p := filepath.Join(h, "restore", "events.log") // h is ~/.reminal
	LogRestore("abcd1234", "record no longer names %s: in front %q", "claude", "-zsh\x1b[2J")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); !strings.Contains(got, " ABCD1234 record no longer names claude: in front \"-zsh") || strings.ContainsRune(got, 0x1b) || strings.Count(got, "\n") != 1 {
		t.Errorf("log %q: want one line, the id upper-cased, no escape", got)
	}
	if err := os.WriteFile(p, []byte(strings.Repeat("x", restoreLogMax+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	LogRestore("abcd1234", "after the bound")
	if fi, err := os.Stat(p); err != nil || fi.Size() > 200 {
		t.Errorf("the log did not start again past its bound: %v %v", fi, err)
	}
	if _, err := os.Stat(p + ".1"); err != nil {
		t.Errorf("the last log was not kept: %v", err)
	}
}
