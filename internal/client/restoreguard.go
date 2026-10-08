// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"reminal/internal/session"
)

// A session id is served by one agent at a time. A restore that brings back a
// session still running makes a second shell and a second copy of its agent on
// the same conversation, and the two take the session's record and seat from
// each other until one is stopped.
//
// So every agent holds a lock named for its session (live/<ID>.lock) for as
// long as it runs — tied to the process, so the kernel lets go of it however
// the agent ends — and nothing restores a session whose lock is held, whose
// record names a process that is alive, or whose record cannot be read (not
// knowing is not "gone").

var (
	liveMu   sync.Mutex
	liveHeld = map[string]*os.File{}
)

func liveLockName(id string) string {
	return filepath.Join("live", strings.ToUpper(strings.TrimSpace(id))+".lock")
}

// holdLive claims a session id for this process, for as long as it runs.
// held is false when another process holds it.
func holdLive(id string) (held bool, err error) {
	if id == "" {
		return false, fmt.Errorf("no session id")
	}
	liveMu.Lock()
	defer liveMu.Unlock()
	if liveHeld[id] != nil {
		return true, nil
	}
	f, ok, err := tryLockFile(liveLockName(id))
	if err != nil || !ok {
		return false, err
	}
	liveHeld[id] = f
	return true, nil
}

// releaseLive lets a session id go as its session ends for good, removing
// the lock file so they do not pile up — before the lock is released, so no
// one takes a lock on a file that is going.
func releaseLive(id string) {
	liveMu.Lock()
	defer liveMu.Unlock()
	f := liveHeld[id]
	if f == nil {
		return
	}
	delete(liveHeld, id)
	if p, err := lockFilePath(liveLockName(id)); err == nil {
		_ = os.Remove(p)
	}
	unlockFile(f)
}

// runningElsewhere says why a session must not be started again here: another
// process holds its id, its record names a process that is alive, or its
// record cannot be read. "" when it is gone.
func runningElsewhere(id string) string {
	liveMu.Lock()
	mine := liveHeld[id] != nil
	liveMu.Unlock()
	if !mine {
		f, ok, err := tryLockFile(liveLockName(id))
		if err == nil && !ok {
			return "it is running"
		}
		if ok {
			unlockFile(f)
		}
	}
	switch session.ActiveState(id) {
	case session.Running:
		return "it is running"
	case session.Unknown:
		return "its record cannot be read right now, so it may be running"
	}
	return ""
}

// claimForRestore is a restored agent's first act: the session's id, held,
// or an error saying why it may not come back.
func claimForRestore(id string) error {
	held, err := holdLive(id)
	if err == nil && !held {
		return fmt.Errorf("session %s is already running: not restoring it again", id)
	}
	if why := runningElsewhere(id); why != "" {
		return fmt.Errorf("session %s not restored: %s", id, why)
	}
	return nil
}

// spawnEnv is this process's environment for a session it starts, with what
// marks this process's own session taken out — or the new session would come
// up as this one (REMINAL_RESTORE), or with its name (REMINAL_NEW_NAME) —
// and extra added.
func spawnEnv(extra ...string) []string {
	var out []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if k == envRestore || k == envNewName {
			continue
		}
		out = append(out, kv)
	}
	return append(out, extra...)
}

const envNewName = "REMINAL_NEW_NAME"

// forgetDeadHarness drops, as a restored session starts, what its harness
// last reported of itself (hook-<id>.state): that harness died with the
// machine, and the one this restore starts says nothing until it acts. Left
// in place, a "working" from a turn the restart cut short made the restored
// harness's first idle ping read as "needs you" (classifyNotify), and the
// new shell showed the dead harness's state meanwhile. A hot restart keeps
// its harness running, and what it said stays true.
func (a *Agent) forgetDeadHarness() {
	if a.restoring {
		_ = session.ClearHookState(a.sessionID)
	}
}
