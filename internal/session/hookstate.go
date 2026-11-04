// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reminal/internal/atrest"
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
	// Background is the work the harness said it still had running when its
	// turn ended: nil when it said nothing about it (a harness, or a build,
	// that does not), which is not the same as none.
	Background *Background `json:"background,omitempty"`
	// TurnAt is when the harness last said a turn began ("working"), kept
	// through the states written after it: a turn over in a moment is
	// overwritten by its "done" before anything reads it, and still a turn
	// was taken — what says an agent at its limit is working again
	// (internal/client/harnesslimit.go). Zero from a reminal too old to
	// keep it.
	TurnAt time.Time `json:"turn_at,omitzero"`
	// Failure is the error its harness said ended its last turn — Claude
	// Code's StopFailure, which fires instead of Stop when an API error
	// ended the turn: "rate_limit", "authentication_failed", … With State
	// "done" (the turn did end); "" for a turn that ended as it should.
	Failure string `json:"failure,omitempty"`
}

// Background is how many shells and subagents a harness still has running in
// the background, as its own hooks said: a turn that ended with them going is
// done, and its session is still at work. Other is the rest of it, by kind as
// the harness names it ("monitor", "workflow", "MCP task", …): work running
// all the same.
type Background struct {
	Shells int            `json:"shells,omitempty"`
	Agents int            `json:"agents,omitempty"`
	Other  map[string]int `json:"other,omitempty"`
}

// BackgroundWork is work a harness says is running without saying what or
// how much (Antigravity's Stop, fullyIdle false): one of it, until anything
// gives a count.
const BackgroundWork = "background work"

// AddOther counts n more of kind.
func (b *Background) AddOther(kind string, n int) {
	if n <= 0 {
		return
	}
	if b.Other == nil {
		b.Other = map[string]int{}
	}
	b.Other[kind] += n
}

// OtherCount is how much of Other there is, in all.
func (b *Background) OtherCount() int {
	n := 0
	for _, k := range b.Other {
		n += k
	}
	return n
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
	return WriteHookStateWith(id, state, nil)
}

// WriteHookStateFailed records that the session's turn ended on an error
// its harness named (failure): "done", the turn's start kept.
func WriteHookStateFailed(id, failure string) error {
	return writeHookStateAs(id, "done", nil, failure)
}

// WriteHookStateWith records the agent-reported attention state for a
// session, with the background work it said it still has (nil: it said
// nothing about that).
func WriteHookStateWith(id, state string, bg *Background) error {
	return writeHookStateAs(id, state, bg, "")
}

func writeHookStateAs(id, state string, bg *Background, failure string) error {
	// Under the record's lock, so it never lands between another hook's
	// reading the record and writing it back (UpdateHookBackground) to be
	// overwritten by the older state. A turn's state must land: if the lock
	// cannot be had in time, it is written anyway.
	if unlock, err := lockHookState(id); err == nil {
		defer unlock()
	}
	hs := HookState{State: state, TS: time.Now(), Background: bg, Failure: failure}
	if state == "working" {
		hs.TurnAt = hs.TS
	} else if prev := ReadHookStateAnyAge(id); prev != nil {
		hs.TurnAt = prev.TurnAt
	}
	return writeHookState(id, hs)
}

// hookLockWait caps waiting for a session's hook record: a hook holds up the
// harness that fired it, and the other holder is a hook writing one small
// file.
const hookLockWait = time.Second

// lockHookState takes the session's hook record lock (an OS lock, let go if
// its holder dies).
func lockHookState(id string) (func(), error) {
	if id == "" {
		return nil, errors.New("hook state requires a session id")
	}
	dir, err := activeDir()
	if err != nil {
		return nil, err
	}
	return atrest.Lock(dir, "hook-"+id+".lock", hookLockWait)
}

// UpdateHookBackground records the background work a harness still has
// running, said between turns (a subagent of its own finishing), leaving its
// turn's state and when that was said as they are: the state is the turn's,
// and a subagent finishing inside a turn does not end it. With no state yet
// there is no turn to attach it to, and nothing is written. Read and written
// back under the record's lock, so a turn's state written meanwhile is not
// undone; without the lock it is left alone — the next turn's end says the
// count again.
func UpdateHookBackground(id string, bg *Background) error {
	unlock, err := lockHookState(id)
	if err != nil {
		return nil
	}
	defer unlock()
	hs := ReadHookStateAnyAge(id)
	if hs == nil {
		return nil
	}
	hs.Background = bg
	return writeHookState(id, *hs)
}

func writeHookState(id string, hs HookState) error {
	p, err := hookStatePath(id)
	if err != nil {
		return err
	}
	if err := atrest.CheckWritable(); err != nil {
		return err // a test binary with the real HOME writes nothing here
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(hs)
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
	hs := ReadHookStateAnyAge(id)
	if hs == nil || time.Since(hs.TS) > HookStateTTL {
		return nil
	}
	return hs
}

// ReadHookStateAnyAge is the session's hook state however old it is, for a
// caller that knows whether the program that wrote it is still running —
// a harness at rest stays at rest, and says nothing more, for as long as it
// is left there.
func ReadHookStateAnyAge(id string) *HookState {
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
	if hs.State == "" {
		return nil
	}
	return &hs
}

// A harness that says when it is up and reading its input (Claude Code's
// SessionStart: started, resumed, cleared) is ready from then: a line typed
// before it — a resumed conversation still loading — may not be taken. Kept
// apart from the turn's state: being up is not a turn, and must never read
// as one begun.

func hookReadyPath(id string) (string, error) {
	p, err := hookStatePath(id)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(p), "hook-"+id+".ready"), nil
}

// MarkHookReady records that the session's harness said it is ready, now.
func MarkHookReady(id string) error {
	p, err := hookReadyPath(id)
	if err != nil {
		return err
	}
	if err := atrest.CheckWritable(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	// Written whole (a reader never sees half a time), as the turn's state is.
	tmp, err := os.CreateTemp(filepath.Dir(p), "hook-*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.WriteString(time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

// HookReadyAt is when the session's harness last said it was ready; zero if
// it never has (a harness, or an install, without the hook).
func HookReadyAt(id string) time.Time {
	p, err := hookReadyPath(id)
	if err != nil {
		return time.Time{}
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, string(raw))
	if err != nil {
		return time.Time{}
	}
	return t
}

// ClearHookState removes a session's hook state (on session exit). Idempotent.
func ClearHookState(id string) error {
	p, err := hookStatePath(id)
	if err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(filepath.Dir(p), "hook-"+id+".lock"))
	_ = os.Remove(filepath.Join(filepath.Dir(p), "hook-"+id+".ready"))
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// AppendHookDebug adds one line to ~/.reminal/hook-debug.log: what a hook
// made of its event, for whoever set REMINAL_HOOK_DEBUG to check it. Only
// what the hook concluded goes in, never the payload it was given.
func AppendHookDebug(line string) error {
	dir, err := activeDir()
	if err != nil {
		return err
	}
	if err := atrest.CheckWritable(); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "hook-debug.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.WriteString(time.Now().Format(time.RFC3339) + " " + line + "\n")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
