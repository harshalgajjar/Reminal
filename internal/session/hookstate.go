// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// HookState is a per-session attention state reported by an integrated coding
// agent's own lifecycle hook (see `reminal integrate` / `reminal hook`). It's the
// PRECISE signal: the agent tells us "working / input / done" directly, instead
// of us inferring it from the screen. Written to ~/.reminal/hook-<id>.state by the
// `reminal hook` command the harness fires; read by the attention detector, which
// prefers it over the screen-scrape fallback while it's fresh (HookStateTTL).
type HookState struct {
	State string    `json:"state"` // "working" | "input" | "done"
	TS    time.Time `json:"ts"`    // when the hook fired
}

// HookStateTTL bounds how long a hook state is trusted without a fresh event. A
// harness fires on transitions (prompt→working, awaiting→input, stop→done), so a
// state legitimately stands between events — including a long "working" think.
// Past this, we assume the harness went away (crash, kill) and fall back to the
// screen detector. Generous on purpose: a false stale is just a graceful fallback.
const HookStateTTL = 15 * time.Minute

func hookStatePath(id string) (string, error) {
	if id == "" {
		return "", errors.New("hook state requires a session id")
	}
	dir, err := activeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "hook-"+id+".state"), nil
}

// WriteHookState records the agent-reported attention state for a session.
func WriteHookState(id, state string) error {
	p, err := hookStatePath(id)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(HookState{State: state, TS: time.Now()})
	if err != nil {
		return err
	}
	// Written whole, or not at all. Two hooks can fire close enough together to
	// overlap — an agent reporting a state change as it exits, say — and writing
	// in place leaves a reader the tail of the longer record stuck onto the
	// shorter one. That does not parse, so the session silently loses its precise
	// state and falls back to reading the screen until the record expires.
	tmp, err := os.CreateTemp(filepath.Dir(p), "hook-*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // no-op once the rename lands
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

// ReadHookState returns the fresh hook state for a session, or nil when there is
// none, it's unreadable, or it's older than HookStateTTL (stale → let the caller
// fall back to the screen detector).
func ReadHookState(id string) *HookState {
	p, err := hookStatePath(id)
	if err != nil {
		return nil
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var hs HookState
	if err := json.Unmarshal(raw, &hs); err != nil {
		return nil
	}
	if hs.State == "" || time.Since(hs.TS) > HookStateTTL {
		return nil
	}
	return &hs
}

// ClearHookState removes a session's hook state (on session exit). Idempotent.
func ClearHookState(id string) error {
	p, err := hookStatePath(id)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
