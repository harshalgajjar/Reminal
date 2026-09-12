// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

// `reminal hook <working|input|done>` — the callback a coding agent's own
// lifecycle hook fires (installed by `reminal integrate`). It records the agent's
// PRECISE attention state for the session it's running in, so the attention
// detector can surface "needs you / working / done" from the agent's own events
// instead of inferring it from the screen. Fire-and-forget: fast, silent, and a
// no-op outside a reminal session, so a harness hook never errors.

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/reminal/reminal/internal/session"
)

func runHook(args []string) error {
	// Harnesses pipe a JSON event payload on stdin. We don't need it, but draining
	// it keeps the harness from blocking on a full pipe / seeing a broken one.
	go func() { _, _ = io.Copy(io.Discard, os.Stdin) }()

	if len(args) < 1 {
		return fmt.Errorf("usage: reminal hook <working|input|done>")
	}
	state := strings.ToLower(strings.TrimSpace(args[0]))
	switch state {
	case "working", "input", "done":
	default:
		return fmt.Errorf("unknown state %q (want working|input|done)", state)
	}

	// REMINAL_SESSION is set in every session's environment; a hook fired outside
	// a reminal session has nothing to report — succeed silently.
	id := strings.ToUpper(strings.TrimSpace(os.Getenv("REMINAL_SESSION")))
	if id == "" {
		return nil
	}
	return session.WriteHookState(id, state)
}
