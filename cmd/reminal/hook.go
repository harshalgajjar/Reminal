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

	"github.com/reminal/reminal/internal/session"
)

func runHook(args []string) error {
	if len(args) < 1 {
		_, _ = io.Copy(io.Discard, os.Stdin) // don't leave the harness pipe blocked
		return fmt.Errorf("usage: reminal hook <working|input|done|notify>")
	}
	arg := strings.ToLower(strings.TrimSpace(args[0]))

	var state string
	switch arg {
	case "working", "input", "done":
		// Fixed state — we don't need the payload, but drain it so the harness
		// doesn't block on a full pipe / see a broken one.
		go func() { _, _ = io.Copy(io.Discard, os.Stdin) }()
		state = arg
	case "notify":
		// Ambiguous notification — decide "needs you" vs "done" from the payload.
		state = classifyNotify(readCappedStdin())
	default:
		go func() { _, _ = io.Copy(io.Discard, os.Stdin) }()
		return fmt.Errorf("unknown state %q (want working|input|done|notify)", arg)
	}

	// REMINAL_SESSION is set in every session's environment; a hook fired outside
	// a reminal session has nothing to report — succeed silently.
	id := strings.ToUpper(strings.TrimSpace(os.Getenv("REMINAL_SESSION")))
	if id == "" {
		return nil
	}
	return session.WriteHookState(id, state)
}

// readCappedStdin reads the harness's JSON event payload, bounded so a hook can
// never hang or balloon on a slow or oversized pipe.
func readCappedStdin() []byte {
	b, _ := io.ReadAll(io.LimitReader(os.Stdin, 1<<16))
	return b
}

// classifyNotify maps a harness "notification" payload to an attention state. A
// plain idle-waiting timeout — the common case, and the only one that fires when
// tool permissions are auto-approved — is "done" (finished, sitting idle at the
// prompt, nothing blocked). Anything else, including a real permission/approval
// request or an unrecognised payload, is "input" so a genuine one is never missed.
func classifyNotify(payload []byte) string {
	low := strings.ToLower(notifyMessage(payload))
	for _, cue := range []string{"waiting for your input", "waiting for input", "is idle", "idle for"} {
		if strings.Contains(low, cue) {
			return "done"
		}
	}
	return "input"
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
