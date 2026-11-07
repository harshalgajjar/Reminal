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

	"reminal/internal/session"
)

func runHook(args []string) error {
	// Fired by a program reminal only asked something (an agent's --version,
	// session.HookProbeEnv): no session's, and nothing of it is written.
	if session.InProbe() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		return nil
	}
	if len(args) < 1 {
		_, _ = io.Copy(io.Discard, os.Stdin) // don't leave the harness pipe blocked
		return fmt.Errorf("usage: reminal hook <working|input|done|failed|notify|subagent|ready>")
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

	var state, failure string
	var bg *session.Background // what it still has running in the background, if it said
	switch arg {
	case "working", "input", "done":
		// The state is fixed; what the payload adds is which conversation the
		// agent is in, kept so a restart can resume that one and not "the
		// latest" (see session.Restore).
		payload := readCappedStdin()
		if subagentTurn(payload) {
			return nil
		}
		if conv := convFromPayload(payload); conv != "" && id != "" && convOwnSession(id) {
			_ = session.WriteConv(id, conv)
			// A turn began in it, asked something in it, or ended in it:
			// written (an agent writes a conversation on its first message),
			// resumable. Each of these comes only within or after a turn
			// (UserPromptSubmit, PermissionRequest, Stop): one whose
			// turn-start payload names no conversation is still marked.
			_ = session.MarkConvWritten(id, conv)
		}
		if arg == "done" {
			// What it left running as its turn ended (Claude Code's Stop):
			// done, and still at work in the background.
			bg = hookBackground(payload, "")
			hookDebugBackground(id, arg, bg)
		}
		state = arg
	case "failed":
		// Its turn ended on an API error (Claude Code's StopFailure, which
		// fires instead of Stop): done, and what ended it — a usage limit's
		// refusal ("rate_limit") is no turn taken (internal/client/
		// harnesslimit.go).
		payload := readCappedStdin()
		failure = hookFailure(payload)
		state = "done"
		// A turn ended in it, refused or not: its conversation is written
		// (Claude Code writes the message and the error alike).
		if conv := convFromPayload(payload); conv != "" && id != "" && convOwnSession(id) {
			_ = session.MarkConvWritten(id, conv)
		}
	case "ready":
		// The harness is up and reading its input (Claude Code's
		// SessionStart). Not a turn's state: kept apart from it.
		payload := readCappedStdin()
		if id == "" {
			return nil
		}
		if conv := convFromPayload(payload); conv != "" && convOwnSession(id) {
			_ = session.WriteConv(id, conv)
			if sessionStartResumed(payload) {
				// Entered by its own resume (/resume, --resume, --continue)
				// or after a compaction: a conversation that is written,
				// though nothing is sent in it yet.
				_ = session.MarkConvWritten(id, conv)
			}
		}
		if err := session.MarkHookReady(id); err != nil {
			return err
		}
		if rec, err := session.ReadActiveByID(id); err == nil && rec.PID > 0 {
			_, _ = sendControl(rec.PID, "attn", hookNudgeWait)
		}
		return nil
	case "subagent":
		// A subagent of its own finished (Claude Code's SubagentStop): what is
		// still running now. Not a turn's state — one inside a turn finishing
		// does not end the turn — so only the count changes.
		payload := readCappedStdin()
		bg = hookBackground(payload, hookAgentID(payload))
		hookDebugBackground(id, arg, bg)
		if bg == nil {
			return nil
		}
	case "notify":
		// Ambiguous notification — decide "needs you" vs "done" from the payload
		// AND the current turn state (see classifyNotify).
		state = classifyNotify(readCappedStdin(), id)
		// It says nothing of what runs in the background or is set to run
		// later: what the turn's end said stands (written below with
		// WriteHookStateKeepingBackground, under the record's lock). Written
		// without it, an idle ping (Qwen Code's comes right after its Stop,
		// Claude Code's a minute on) wiped the count the Stop had just given.
	default:
		go func() { _, _ = io.Copy(io.Discard, os.Stdin) }()
		return fmt.Errorf("unknown state %q (want working|input|done|failed|notify|subagent|ready)", arg)
	}

	if id == "" {
		return nil
	}
	var err error
	switch {
	case arg == "subagent":
		err = session.UpdateHookBackground(id, bg)
	case arg == "failed":
		err = session.WriteHookStateFailed(id, failure)
	case arg == "notify":
		err = session.WriteHookStateKeepingBackground(id, state)
	default:
		err = session.WriteHookStateWith(id, state, bg)
	}
	if err != nil {
		return err
	}
	// The session's agent reads the state when told to, so a turn that ends
	// on a still screen shows as ended at once. An agent too old to know the
	// verb, or gone, reads it the next time its screen moves.
	if rec, err := session.ReadActiveByID(id); err == nil && rec.PID > 0 {
		_, _ = sendControl(rec.PID, "attn", hookNudgeWait)
	}
	return nil
}

// subagentTurn says a payload is one of the agent's own subagents taking a
// turn, not the session's: Copilot CLI fires a subagent's prompt and its
// stop on the same hooks as the session's, naming the agent it was started
// by. Its start is not the session working, nor its end the session done.
func subagentTurn(payload []byte) bool {
	var p struct {
		ParentAgentID string `json:"parentAgentId"`
	}
	return json.Unmarshal(payload, &p) == nil && p.ParentAgentID != ""
}

// hookFailure is the error a StopFailure payload names, as a short word
// ("rate_limit"); "unknown" when it names none or one not read as a word.
func hookFailure(payload []byte) string {
	var p struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(payload, &p)
	e := strings.ToLower(strings.TrimSpace(p.Error))
	if e == "" || len(e) > 40 || strings.TrimLeft(e, "abcdefghijklmnopqrstuvwxyz_") != "" {
		return "unknown"
	}
	return e
}

// hookNudgeWait caps telling the agent: a local socket answers at once, and a
// hung agent must not hold up the harness that fired the hook.
const hookNudgeWait = 500 * time.Millisecond

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

// convFromPayload is the conversation id in a harness's hook payload, under
// whichever name that harness gives it, or "".
// convOwnSession says a hook may record a conversation for session id: it
// runs inside that session, or its enclosing session cannot be told. A
// conversation id is what a session types when it is restored, so one
// session's hook never names another's (REMINAL_SESSION set by hand to
// another session's id).
var convOwnSession = func(id string) bool { return convAllowed(session.Enclosing(), id) }

// convAllowed: a hook in session enclosing may write session id's conversation.
func convAllowed(enclosing, id string) bool {
	return enclosing == "" || strings.EqualFold(enclosing, id)
}

// sessionStartResumed says a SessionStart payload's conversation is one the
// agent went back to (Claude Code's source: resume, or compact — the same
// conversation, compacted), not one it begins (startup, clear).
func sessionStartResumed(b []byte) bool {
	var m struct {
		Source string `json:"source"`
	}
	if json.Unmarshal(b, &m) != nil {
		return false
	}
	return m.Source == "resume" || m.Source == "compact"
}

func convFromPayload(b []byte) string {
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return ""
	}
	for _, k := range []string{"session_id", "conversation_id", "conversationId", "thread_id", "thread-id", "chat_id"} {
		if v, ok := m[k].(string); ok && session.ValidConv(v) {
			return v
		}
	}
	return ""
}
