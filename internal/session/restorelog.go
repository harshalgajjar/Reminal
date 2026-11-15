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
