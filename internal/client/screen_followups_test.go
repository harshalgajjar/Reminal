// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"reminal/internal/crypto"
)

// pieceWriter notes the size of every write made through it.
type pieceWriter struct {
	w     io.Writer
	sizes []int
}

func (c *pieceWriter) Write(p []byte) (int, error) {
	c.sizes = append(c.sizes, len(p))
	return c.w.Write(p)
}

// A long history goes back into the screen in pieces, none much larger than
// restoreChunk, since one call into the terminal is bounded in time; and all
// of it is there afterwards, each line once, with the screen left clear for
// the ring's replay.
func TestALongHistoryIsRestoredInPiecesAndWhole(t *testing.T) {
	e := testScreen(t, 180, 40)
	e.SetMaxLines(30000)
	history := make([]string, 20000)
	for i := range history {
		history[i] = fmt.Sprintf("\x1b[32mline %05d\x1b[m %s", i+1, strings.Repeat("the quick brown fox ", 7))
	}
	cw := &pieceWriter{w: e}
	writeHistory(cw, history, e.Height())
	if len(cw.sizes) < 2 {
		t.Fatalf("the history went in as %d write(s); want pieces of at most %d bytes", len(cw.sizes), restoreChunk)
	}
	for _, n := range cw.sizes {
		if n > restoreChunk+len(history[0])+8 {
			t.Errorf("a piece of %d bytes; want at most %d and a line", n, restoreChunk)
		}
	}
	if err := e.Err(); err != nil {
		t.Fatalf("the screen failed: %v", err)
	}
	seen := map[string]int{}
	line := regexp.MustCompile(`^line (\d{5}) `)
	for _, l := range e.History() {
		if m := line.FindStringSubmatch(stripANSI(l)); m != nil {
			seen[m[1]]++
		}
	}
	if len(seen) != len(history) {
		t.Errorf("%d distinct lines in the history after the restore, want %d", len(seen), len(history))
	}
	for k, n := range seen {
		if n != 1 {
			t.Errorf("line %s is in the history %d times", k, n)
		}
	}
	if rows := renderedScreenRows(e); len(rows) != 0 {
		t.Errorf("%d rows left on the screen after the restore, want it clear: %q", len(rows), rows[0])
	}
}

// failScreen makes a's copy of its screen read as failed, as it does once its
// terminal refused a write.
func failScreen(a *Agent) {
	a.screen.mu.Lock()
	a.screen.failed = errors.New("test: the terminal refused a write")
	a.screen.mu.Unlock()
}

// A restart dump written while the copy of the screen has failed carries the
// ring alone: the copy is not what the program drew, so its history is not
// handed to the successor, and the copy is dropped as every other reader
// drops it.
func TestADumpFromAFailedScreenCarriesNoHistory(t *testing.T) {
	key, _ := crypto.NewSessionKey()
	box, _ := crypto.NewBox(key)
	a := handoverAgent(t, box)
	for i := 1; i <= 200; i++ {
		a.record([]byte(fmt.Sprintf("line %d\r\n", i)))
	}
	failScreen(a)
	plain := func(b []byte) ([]byte, error) { return b, nil }
	path := filepath.Join(t.TempDir(), "dump.json")
	if err := a.writeScrollbackDumpTo(path, plain); err != nil {
		t.Fatalf("writing the dump: %v", err)
	}
	d := readScrollbackDump(path, plain)
	if d == nil {
		t.Fatal("no dump was written")
	}
	if len(d.History) != 0 || d.HistoryCols != 0 {
		t.Errorf("the dump holds %d history lines at %d columns from a failed screen; want the ring alone", len(d.History), d.HistoryCols)
	}
	if len(d.Entries) == 0 {
		t.Error("the ring is not in the dump")
	}
	if a.haveScreen() {
		t.Error("the failed screen was kept")
	}
}

// The notice that the copy of the screen has failed is given once screenMu is
// let go, not under it: a write to the terminal can wait, and the session's
// output and every join wait on that lock.
func TestAFailedScreensNoticeIsGivenAfterItsLockIsLetGo(t *testing.T) {
	key, _ := crypto.NewSessionKey()
	box, _ := crypto.NewBox(key)
	a := handoverAgent(t, box)
	var notices []string
	lockFree := true
	saved := screenNotify
	screenNotify = func(n string) {
		if a.screenMu.TryLock() {
			a.screenMu.Unlock()
		} else {
			lockFree = false
		}
		notices = append(notices, n)
	}
	defer func() { screenNotify = saved }()
	failScreen(a)
	a.record([]byte("hello\r\n"))
	if len(notices) != 1 || !strings.Contains(notices[0], "stopped working") {
		t.Fatalf("notices given: %q; want the one that the copy stopped working", notices)
	}
	if !lockFree {
		t.Error("the notice was given with screenMu held")
	}
	if a.haveScreen() {
		t.Error("the failed screen was kept")
	}
	if a.screenNotices != nil {
		t.Errorf("%d notice(s) still kept after the lock was let go", len(a.screenNotices))
	}
}
