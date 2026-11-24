// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"reminal/internal/atrest"
)

// restoreLogMax is how large the restore log grows before it starts again
// (the last one kept as .1).
const restoreLogMax = 256 << 10

// LogRestore appends one line about session id's restore record to
// ~/.reminal/restore/events.log: what decided what a restore will do, kept
// for when a session comes back unlike how it was left. A session's agent
// writes nothing else anywhere (its output is not kept), so without it the
// reason was gone with the process. Best-effort, and bounded.
func LogRestore(id, format string, args ...any) {
	if atrest.CheckWritable() != nil {
		return
	}
	dir, err := restoreDir()
	if err != nil || os.MkdirAll(dir, 0o700) != nil {
		return
	}
	p := filepath.Join(dir, "events.log")
	if fi, err := os.Stat(p); err == nil && fi.Size() > restoreLogMax {
		_ = os.Rename(p, p+".1")
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	line := fmt.Sprintf(format, args...)
	line = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, line)
	fmt.Fprintf(f, "%s %s %s\n", time.Now().Format(time.RFC3339), strings.ToUpper(id), line)
}

// LogEnded keeps why session id's process ended: the one "ended: ..." line a
// session leaves as it goes, whichever way it goes.
func LogEnded(id, format string, args ...any) {
	LogRestore(id, "ended: "+format, args...)
}

// LogSent keeps that this process sent sig to pid, the process serving
// session id, and why. With it, a session ended by reminal itself can be
// told from one ended from outside, which leaves no such line.
func LogSent(id string, pid int, sig, why string) {
	LogRestore(id, "%s sent to pid %d by %s (pid %d): %s", sig, pid, senderName(), os.Getpid(), why)
}

// senderName says what this process is, for LogSent: its program and what it
// was asked to do ("reminal kill", "reminal daemon").
func senderName() string {
	name := filepath.Base(os.Args[0])
	for _, a := range os.Args[1:] {
		if !strings.HasPrefix(a, "-") {
			return name + " " + a
		}
	}
	if len(os.Args) > 1 {
		return name + " " + os.Args[1]
	}
	return name
}

// logGoneWithoutAWord keeps that the process serving session id is gone
// with no word of why: its record named a pid that no longer runs, and the
// last line kept for it is not its own "ended". what says which record.
// A crash file that says something is pointed at.
func logGoneWithoutAWord(id string, pid int, what string) {
	if strings.HasPrefix(lastLogged(id), "ended: ") {
		return
	}
	crash := ""
	if CrashLogSaysSomething(id) {
		if p, err := CrashLogPath(id); err == nil {
			crash = "; its last words are in " + filepath.Base(p)
		}
	}
	LogRestore(id, "ended without a word: pid %d is gone, %s removed by %s (pid %d)%s", pid, what, senderName(), os.Getpid(), crash)
}

// lastLogged is the last line LogRestore kept for session id, without its
// time and id ("" when there is none).
func lastLogged(id string) string {
	dir, err := restoreDir()
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(dir, "events.log"))
	if err != nil {
		return ""
	}
	mark := " " + strings.ToUpper(id) + " "
	lines := strings.Split(string(b), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		// "<time> <ID> <line>": the id is the second word.
		if sp := strings.IndexByte(lines[i], ' '); sp > 0 && strings.HasPrefix(lines[i][sp:], mark) {
			return lines[i][sp+len(mark):]
		}
	}
	return ""
}
