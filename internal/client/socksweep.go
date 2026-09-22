// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"reminal/internal/proc"
)

// sweepStaleSockets removes ~/.reminal socket files whose owners are gone.
// Clean exits unlink their own sockets, but hard kills, crashes, and (on
// Windows) hot-restart handoffs can't — and the litter accumulates forever
// (a test VM collected dozens in a day). Called in the background at agent
// and daemon startup; best-effort throughout.
//
//   - agent-<pid>.sock: the owner is knowable from the name — remove when
//     that pid is dead. A LIVE pid is left alone even if it isn't reminal
//     (pid reuse): its listener is gone either way, and the real agent with
//     that pid would have just rebound the path.
//   - pty-<id>.sock (Windows ConPTY holders): the owner isn't in the name, so
//     probe it — a quick dial that's refused means no holder is listening.
//     Young files are skipped entirely: a holder binds its socket moments
//     after the file's mtime, and racing that window could delete a live
//     session's socket.
func sweepStaleSockets() {
	dir, err := reminalDir()
	if err != nil {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		full := filepath.Join(dir, name)
		switch {
		case strings.HasPrefix(name, "agent-") && strings.HasSuffix(name, ".sock"):
			pidStr := strings.TrimSuffix(strings.TrimPrefix(name, "agent-"), ".sock")
			pid, err := strconv.Atoi(pidStr)
			if err != nil {
				continue
			}
			if !proc.Alive(pid) {
				_ = os.Remove(full)
			}
		case strings.HasPrefix(name, "pty-") && strings.HasSuffix(name, ".sock"):
			info, err := e.Info()
			if err != nil || time.Since(info.ModTime()) < time.Hour {
				continue // too young to judge — a holder may be mid-boot
			}
			conn, err := net.DialTimeout("unix", full, 500*time.Millisecond)
			if err != nil {
				_ = os.Remove(full)
				continue
			}
			_ = conn.Close() // live holder — leave it be
		}
	}
}
