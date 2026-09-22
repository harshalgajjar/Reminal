// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

// `reminal hook <working|input|done|notify>` — the callback a coding agent's own
// lifecycle hook fires (installed by `reminal integrate`). It records the agent's
// PRECISE attention state for the session it's running in, so the attention
// detector can surface "needs you / working / done" from the agent's own events
// instead of inferring it from the screen. Fire-and-forget: fast, silent, and a
// no-op outside a reminal session, so a harness hook never errors.
//
// `notify` is the special case: some harnesses (Claude Code, Qwen) fire ONE
// "Notification" event for two very different things — "I need you to approve a
// tool" (a real "needs you") and "I've been idle waiting for you for a minute"
// (just finished, sitting at the prompt). `notify` reads the event payload and
// splits them, so an idle timeout doesn't make every quiet session scream for
// attention. Under bypass-permissions the idle timeout is the ONLY notification
// that ever fires, so getting this split right is what keeps the signal useful.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/reminal/reminal/internal/session"
)

func runHook(args []string) error {
	if len(args) < 1 {
		_, _ = io.Copy(io.Discard, os.Stdin) // don't leave the harness pipe blocked
		return fmt.Errorf("usage: reminal hook <working|input|done|notify>")
	}
	arg := strings.ToLower(strings.TrimSpace(args[0]))

	// REMINAL_SESSION is set in every session's environment; a hook fired outside
	// a reminal session has nothing to report — succeed silently below.
	//
	// Not every hook inherits it, though: a harness that starts the processes
	// it fires with the environment scrubbed (Codex and cursor-agent both do
	// for their helpers) leaves the hook with no idea which session it is in,
	// and the state it was fired to report is simply lost. A hook is always a
	// descendant of the agent that owns the PTY, and the agent's pid is in its
	// own record, so the process tree still says which session this is.
	id := strings.ToUpper(strings.TrimSpace(os.Getenv("REMINAL_SESSION")))
	if id == "" {
		id = strings.ToUpper(session.Enclosing())
	}

	var state string
	switch arg {
	case "working", "input", "done":
		// Fixed state — we don't need the payload, but drain it so the harness
		// doesn't block on a full pipe / see a broken one.
		go func() { _, _ = io.Copy(io.Discard, os.Stdin) }()
		state = arg
	case "notify":
		// Ambiguous notification — decide "needs you" vs "done" from the payload
		// AND the current turn state (see classifyNotify).
		state = classifyNotify(readCappedStdin(), id)
	default:
		go func() { _, _ = io.Copy(io.Discard, os.Stdin) }()
		return fmt.Errorf("unknown state %q (want working|input|done|notify)", arg)
	}

	if id == "" {
		return nil
	}
	return session.WriteHookState(id, state)
}

// stdinWait bounds how long we wait for the harness to finish writing the
// event payload. Generous for a local pipe, but finite: a hook that never
// returns freezes the agent that ran it.
const stdinWait = 2 * time.Second

// readCappedStdin reads the harness's JSON event payload, bounded so a hook can
// never hang or balloon on a slow or oversized pipe.
func readCappedStdin() []byte {
	done := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(io.LimitReader(os.Stdin, 1<<16))
		done <- b
	}()
	select {
	case b := <-done:
		return b
	case <-time.After(stdinWait):
		// The harness handed us a pipe it never closed. io.ReadAll waits for EOF,
		// so we would block forever — and harnesses run hooks SYNCHRONOUSLY, so
		// blocking here freezes the agent itself. Give up and classify on what we
		// know instead: an unread payload just means classifyNotify falls through
		// to the turn-state rule, which is the same thing it does for a payload it
		// cannot parse. Never make the harness wait on us.
		return nil
	}
}

// classifyNotify maps a harness "notification" payload to an attention state.
// This event is ambiguous: a harness (Claude Code, Qwen) fires it BOTH for a
// permission/approval request AND for a plain "you've been idle" timeout — and
// worse, the idle message is identical whether the agent is genuinely blocked
// mid-turn waiting on you or simply finished and sitting quiet. We split it two
// ways:
//
//   - A permission/approval message always needs you — it blocks the turn.
//   - Otherwise it's an idle ping, disambiguated by the CURRENT turn state:
//     mid-turn (still "working", or already flagged "input") means the agent is
//     waiting on you → "input"; a turn that already ended ("done"), or no state
//     at all, means it just went quiet → "done".
//
// Getting this right is what stops a fleet of finished agents from all turning
// amber after a minute, while still not downgrading a real "needs you" that the
// user simply hasn't answered yet.
func classifyNotify(payload []byte, id string) string {
	low := strings.ToLower(notifyMessage(payload))
	for _, cue := range []string{"permission", "approve", "approval"} {
		if strings.Contains(low, cue) {
			return "input"
		}
	}
	if cur := session.ReadHookState(id); cur != nil && (cur.State == "working" || cur.State == "input") {
		return "input"
	}
	return "done"
}

// notifyMessage pulls the human-readable text out of a harness hook payload,
// which is JSON like {"message":"…","hook_event_name":"Notification",…}. Falls
// back to the raw bytes so a non-JSON or differently-shaped payload still matches.
func notifyMessage(payload []byte) string {
	var v struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(payload, &v); err == nil && v.Message != "" {
		return v.Message
	}
	return string(payload)
}
