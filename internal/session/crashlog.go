// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"
)

// crashLogMax is how large a session's crash file grows before it starts
// again (the last one kept as .1). crashLogKeep is how long one is kept with
// no session writing to it.
const (
	crashLogMax  = 128 << 10
	crashLogKeep = 30 * 24 * time.Hour
)

// CrashLogPath is where session id's last words are kept:
// ~/.reminal/restore/crash-<ID>.log.
func CrashLogPath(id string) (string, error) {
	if id == "" || strings.ContainsAny(id, `/\.`) {
		return "", errors.New("a crash file requires a session id")
	}
	dir, err := restoreDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "crash-"+strings.ToUpper(id)+".log"), nil
}

// KeepCrashOutput has what the Go runtime prints as this process dies (a
// panic, a fatal error) written to session id's crash file as well as to
// standard error. A background session's standard error goes nowhere, so
// without it such an end left nothing behind. Each process serving the
// session calls it as it starts (new, restored, or the next image of a
// restart in place), and says which in what: one "# " line per call, so the
// file also says who was serving when. Best-effort.
func KeepCrashOutput(id, what string) error {
	p, err := CrashLogPath(id)
	if err != nil {
		return err
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	dropOldCrashLogs(dir, time.Now())
	if fi, err := os.Stat(p); err == nil && fi.Size() > crashLogMax {
		_ = os.Rename(p, p+".1")
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	// The runtime keeps its own handle on the file: this one is done with.
	defer f.Close()
	exe, _ := os.Executable()
	what = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, what)
	if _, err := fmt.Fprintf(f, "# %s pid %d %s: %s\n", time.Now().Format(time.RFC3339), os.Getpid(), exe, what); err != nil {
		return err
	}
	return debug.SetCrashOutput(f, debug.CrashOptions{})
}

// CrashLogSaysSomething reports whether session id's crash file holds more
// than the lines KeepCrashOutput writes: what a dying process printed.
func CrashLogSaysSomething(id string) bool {
	p, err := CrashLogPath(id)
	if err != nil {
		return false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if line != "" && !strings.HasPrefix(line, "# ") {
			return true
		}
	}
	return false
}

// DropQuietCrashLog is for the process serving session id, as it ends in
// order: it stops keeping its dying words (it has said its end), and removes
// the crash file when nothing died into it, so only files that say something
// stay. The runtime lets go of the file first: Windows removes no file that
// is held open.
func DropQuietCrashLog(id string) {
	_ = debug.SetCrashOutput(nil, debug.CrashOptions{})
	RemoveQuietCrashLog(id)
}

// RemoveQuietCrashLog removes session id's crash file when nothing died
// into it: for whoever finds the process that served the session gone (its
// record pruned, or ended by reminal kill). A file holding only the lines
// of who was serving must not be left to be read as a crash.
func RemoveQuietCrashLog(id string) {
	p, err := CrashLogPath(id)
	if err != nil || CrashLogSaysSomething(id) {
		return
	}
	_ = os.Remove(p)
}

// dropOldCrashLogs removes the crash files in dir no process has written to
// for crashLogKeep.
func dropOldCrashLogs(dir string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, ent := range entries {
		name := ent.Name()
		if !strings.HasPrefix(name, "crash-") || !(strings.HasSuffix(name, ".log") || strings.HasSuffix(name, ".log.1")) {
			continue
		}
		if fi, err := ent.Info(); err == nil && now.Sub(fi.ModTime()) > crashLogKeep {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
}
