// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// What each harness shows at its limit, and where that came from. None of
// these could be driven to a real limit (that needs an account at it), so
// each is its CLI's own template, from the installed release's bundle or
// binary (Docker, 2026-10-19), filled in as the CLI fills it:
//
//	claude        2.1.303   source  "You've hit your <name> limit · resets <time> (<zone>)"
//	codex         0.163.0   source  "You’ve hit your usage limit. … try again at <time>."
//	gemini        0.64.0    source  "Usage limit reached for <model>." / "Access resets at <time> <zone>."
//	qwen          0.25.3    source  "[API Error: … (Status: 429)]" / "Possible quota limitations …"
//	opencode      1.19.3    source  "Usage limit reached. It will reset in <h> hours <m> minutes. …"
//	goose         1.55.0    source  "Rate limit exceeded: …", "Credits exhausted: …"
//	crush         0.98.1    inferred: no notice of its own; the service's words under "Provider Error"
//	cursor-agent  2026.10.18 source "Upgrade your plan to continue" (the limit's words are its service's)
//
// Later releases' own words, from their bundles and binaries the same way
// (2026-10-28): claude 2.1.312, codex 0.165.0, gemini 0.66.0, qwen 0.26.2,
// opencode 1.19.4, goose 1.56.0, amp 0.0.1793131239, pi 1.4.0, agy 1.4.8.
// Where a CLI's glyph before its notice is not known, the screen has none.
//
// The logged-out screens (TestHarnessLoggedOutScreens) were each captured
// from the CLI itself, run with no login.
var limitScreens = []struct {
	prog, screen string
	resets       time.Time // zero: says none
}{
	{"claude", "  ⎿  You've hit your session limit · resets 3pm (America/Los_Angeles)\n✻ Churned for 0s\n────\n❯ \n────\n  ⏵⏵ auto mode on", limitLA(2026, 10, 19, 15, 0)},
	{"claude", "  ⎿  You’ve hit your weekly limit · resets Oct 21, 3:30pm (America/Los_Angeles) · progress saved\n❯ ", limitLA(2026, 10, 21, 15, 30)},
	{"claude", "  ⎿  You've hit your usage limit · resets 9am (America/Los_Angeles)\n     /upgrade to increase your usage limit.\n❯ ", limitLA(2026, 10, 20, 9, 0)},
	{"claude", "● You've hit your limit · resets 11:15am (America/Los_Angeles)\n❯ ", limitLA(2026, 10, 19, 11, 15)},
	{"claude", "  ⎿  You're out of usage credits · resets 5pm (America/Los_Angeles)\n❯ ", limitLA(2026, 10, 19, 17, 0)},
	{"claude", "  ⎿  Usage limit reached · limit resets 4pm\n❯ ", limitLA(2026, 10, 19, 16, 0)},
	{"claude", "  ⎿  You've hit your org's monthly spend limit\n❯ ", time.Time{}},
	{"claude", "  ⎿  You've hit your session limit · resets 3pm\n     (America/Los_Angeles)\n❯ ", limitLA(2026, 10, 19, 15, 0)},
	// Seen on real screens, 2026-10-27: the command to get more after it, on
	// one row or wrapped onto the next.
	{"claude", "  ⎿  You've hit your individual spend limit · run /usage-credits to ask your admin for more\n❯ ", time.Time{}},
	{"claude", "  ⎿  You've hit your individual spend limit · run /usage-credits to ask y\n     our admin for more\n❯ ", time.Time{}},
	{"claude", "  ⎿  You've hit your weekly limit · resets 6am (America/Los_Angeles) · /usage-credits to keep using Claude\n❯ ", limitLA(2026, 10, 20, 6, 0)},
	// Claude Code 2.1.312's wording.
	{"claude", "  ⎿  Out of usage credits\n❯ ", time.Time{}},
	{"claude", "  ⎿  You've hit your channel's monthly spend limit.\n❯ ", time.Time{}},
	{"claude", "  ⎿  Couldn't reset your 5-hour limit with this login · run /login, then try again\n❯ ", time.Time{}},
	{"claude", "  ⎿  A 5-hour limit reset isn't available right now.\n❯ ", time.Time{}},
	{"claude", "  ⎿  Automatic continue stopped after repeated usage limit hits · This task will not resume on its own (/rate-limit-options to try again)\n❯ ", time.Time{}},
	{"claude", "  ⎿  Automatic continue stopped after repeated usage limit hits · This task will not resume on its own (/rate-limit-op\n     tions to try again)\n❯ ", time.Time{}},
	{"claude", "  ⎿  You're out of usage credits. /model to switch models.\n❯ ", time.Time{}},
	{"claude", "  ⎿  You're out of usage credits. Run /usage-credits to keep using Claude\n❯ ", time.Time{}},
	{"claude", "  ⎿  Your org is out of usage · add funds to continue\n❯ ", time.Time{}},
	{"claude", "  ⎿  Your org is out of usage · contact your admin\n❯ ", time.Time{}},
	{"claude", "  ⎿  You're out of extra usage\n❯ ", time.Time{}},
	{"claude", "  ⎿  You've reached your Fable limit.\n❯ ", time.Time{}},
	{"claude", "  ⎿  Fable limit reached\n❯ ", time.Time{}},
	{"claude", "  ⎿  Your organization is out of usage credits. Contact your admin to add more.\n❯ ", time.Time{}},
	{"claude", "  ⎿  You've hit your monthly limit · run /usage-credits to adjust\n❯ ", time.Time{}},
	{"claude", "  ⎿  You've hit your team's shared budget. Run /usage-credits to raise it and keep using Claude or switch models to continue this chat.\n❯ ", time.Time{}},
	// Held until the time it says, then going on by itself.
	{"claude", "  ⎿  Usage limit reached · continuing automatically at 3pm · esc to cancel\n❯ ", limitLA(2026, 10, 19, 15, 0)},
	{"claude", "  ⎿  Usage limit reached again · continuing automatically in 2h 5m · esc to cancel\n❯ ", limitLA(2026, 10, 19, 12, 5)},
	// Claude Code 2.1.317–318: a goal paused at
	// its limit, and a group's pooled budget on the Claude apps gateway,
	// with when it resets.
	{"claude", "  ⎿  Goal paused · usage limit reached · send a message after it resets to continue\n❯ ", time.Time{}},
	{"claude", "  ⎿  Goal paused · usage limit reached · continues automatically when it resets\n❯ ", time.Time{}},
	{"claude", "  ⎿  This request exceeds your group's budget. Ask your organization's admins to raise it, or try again after it resets on November 3 at 14:00 UTC.\n❯ ", time.Date(2026, 11, 3, 14, 0, 0, 0, time.UTC)},
	{"codex", "■ Your workspace is out of credits. Add credits to continue.\n› ", time.Time{}},
	// Codex 0.166.0: a workspace member at its
	// limit, asked to request more from its owner.
	{"codex", "■ Request a limit increase from your owner to continue using codex. Request increase?\n› ", time.Time{}},
	{"codex", "■ You're out of credits. Your workspace is out of credits. Add credits to continue using Codex.\n› ", time.Time{}},
	{"codex", "■ Your workspace is out of credits. Ask your workspace owner to refill in order to continue.\n› ", time.Time{}},
	{"codex", "■ You've reached your workspace credit limit\n› ", time.Time{}},
	{"codex", "■ Your included usage is exhausted. Choose an option below to continue.\n› ", time.Time{}},
	// On a phone's narrow screen: four rows.
	{"codex", "■ You’ve hit your usage limit. Upgrade to Pro\n(https://chatgpt.com/explore/pro), visit\nhttps://chatgpt.com/settings/usage to purchase\nmore credits or try again at 3:05 PM.\n\n› ", limitLA(2026, 10, 19, 15, 5)},
	{"gemini", "✕ Rate limit exceeded. Try again later.\n│ > │", time.Time{}},
	{"gemini", "✕ Rate limit exceeded\n│ > │", time.Time{}},
	{"gemini", "✕ Please wait and try again later. To increase your limits, request a quota increase through AI Studio, or switch to another /auth method\n│ > │", time.Time{}},
	{"qwen", "✕ Quota exhausted\n│ > │", time.Time{}},
	{"qwen", "✕ [API Error: Quota exhausted]\n│ > │", time.Time{}},
	{"qwen", "✕ Rate limit exceeded. Please wait and try again.\n│ > │", time.Time{}},
	{"qwen", "✕ Model API rate limit exceeded (HTTP 429). Wait and try again later.\n│ > │", time.Time{}},
	{"qwen", "✕ Qwen OAuth quota exceeded\n│ > │", time.Time{}},
	// Claude Code 2.1.319: an organization whose usage is paused, and a goal
	// paused by it, drawn as its other notices are.
	{"claude", "  ⎿  Usage is paused for your organization. For help, please contact support.\n\n❯ ", time.Time{}},
	{"claude", "  ⎿  Goal paused · usage is paused for your organization\n\n❯ ", time.Time{}},
	// Qwen Code 0.26.9: its credits used up, the weekly ones, what to do,
	// and when they come back (on one row, and wrapped onto two).
	{"qwen", "✕ You've used up your credits. Please try again after the reset.\n│ > │", time.Time{}},
	{"qwen", "✕ You've used up your weekly credits. You can wait or run /upgrade to get more credits.\n│ > │", time.Time{}},
	{"qwen", "✕ You've used up your weekly credits.\n  Please try again after the reset.\n│ > │", time.Time{}},
	{"qwen", "✕ Your credits are expected to reset in 3 hours.\n│ > │", limitLA(2026, 10, 19, 13, 0)},
	{"qwen", "✕ Your credits are expected to reset in 2 days 4 hours.\n│ > │", limitLA(2026, 10, 21, 14, 0)},
	{"opencode", "┃  Usage limit reached. To continue using this model now, enable usage from your available balance\n┃", time.Time{}},
	{"opencode", "┃  Free limit reached\n┃", time.Time{}},
	{"opencode", "┃  Go limit reached\n┃", time.Time{}},
	{"opencode", "┃  Free usage exceeded, subscribe to Go\n┃", time.Time{}},
	{"goose", "  Credits exhausted\n( O)> ", time.Time{}},
	{"goose", "  Ran into this error: Credits exhausted.\n( O)> ", time.Time{}},
	{"goose", "  Please add credits to your account, then resend your message to continue.\n( O)> ", time.Time{}},
	{"amp", "  You are out of credits. Add paid credits or choose a paid tier.\n", time.Time{}},
	{"amp", "  Add credits to keep using Amp right now, or wait until the next hour starts for more free usage.\n", time.Time{}},
	{"amp", "  Upgrade to a paid tier, or add credits to keep using Amp.\n", time.Time{}},
	{"amp", "  Rate limit exceeded. Try again shortly.\n", time.Time{}},
	{"amp", "  Usage Quota Exceeded\n", time.Time{}},
	{"pi", "  Monthly usage limit reached\n", time.Time{}},
	{"pi", "  You've reached your ChatGPT workspace's usage limit. Ask your workspace admin to increase it at admin.openai.com → Workspace → Usage limits.\n", time.Time{}},
	{"pi", "  You have hit your ChatGPT usage limit.\n", time.Time{}},
	{"pi", "  Hugging Face rate limit reached; retry in 30s\n", limitLA(2026, 10, 19, 10, 0).Add(30 * time.Second)},
	{"agy", "  AI: Out of credits\n", time.Time{}},
	{"copilot", "  You've hit your session rate limit.\n", time.Time{}},
	{"copilot", "  You've reached your weekly rate limit. Learn More (https://docs.github.com/copilot/concepts/rate-limits).\n", time.Time{}},
	{"copilot", "  You've hit the rate limit for this model. Please switch models or wait for your limit to reset\n", time.Time{}},
	{"copilot", "  You've been rate limited on the current model. Would you like to switch to auto to continue?\n", time.Time{}},
	{"copilot", "  You've used all your Copilot Free chat requests for the month.\n", time.Time{}},
	// With the hint the CLI draws under it, in lower case (review).
	{"claude", "  ⎿  You've hit your session limit · resets 3pm\n     run /usage-credits to ask your admin for more\n❯ ", limitLA(2026, 10, 19, 15, 0)},
	{"codex", "■ You’ve hit your usage limit. Visit https://chatgpt.com/settings/usage to purchase more credits or try again at 3:05 PM.\nesc to interrupt\n› ", limitLA(2026, 10, 19, 15, 5)},
	{"codex", "■ You’ve hit your usage limit. Upgrade to Pro (https://chatgpt.com/explore/pro), visit\nhttps://chatgpt.com/settings/usage to purchase more credits or try again at 3:05 PM.\n\n› ", limitLA(2026, 10, 19, 15, 5)},
	{"codex", "■ You've hit your usage limit. Visit https://chatgpt.com/settings/usage to purchase more credits or try again at Oct 21st, 2026 3:05 PM.\n› ", limitLA(2026, 10, 21, 15, 5)},
	{"codex", "■ You’ve hit your usage limit. To get more access now, send a request to your admin or try again later.\n› ", time.Time{}},
	{"gemini", "✕ Usage limit reached for gemini-2.5-pro.\n  Access resets at 3:05 PM PDT.\n  /stats model for usage details\n  /model to switch models.\n╭───╮\n│ > Type your message │", limitLA(2026, 10, 19, 15, 5)},
	{"gemini", "✕ You have exhausted your daily quota on this model.\n│ > │", time.Time{}},
	{"qwen", "✕ [API Error: Request failed with status code 429 (Status: 429)]\nPossible quota limitations in place or slow response times detected. Please wait and try again later.\n│ > │", time.Time{}},
	{"opencode", "┃  Usage limit reached. It will reset in 3 hours 20 minutes. To continue using this model now, enable usage from your available balance\n┃", limitLA(2026, 10, 19, 13, 20)},
	{"opencode", "┃  Rate limit exceeded, retry in 45s\n┃", limitLA(2026, 10, 19, 10, 0).Add(45 * time.Second)},
	// Led by the model's id, in lower case as OpenCode writes it (review).
	{"opencode", "┃  claude-sonnet-4 usage limit reached. It will reset in 3 hours 20 minutes.\n┃", limitLA(2026, 10, 19, 13, 20)},
	{"opencode", "┃  gpt-5 usage limit reached. To continue using this model now, enable usage from your available balance\n┃", time.Time{}},
	{"goose", "  Ran into this error: Rate limit exceeded: 429 Too Many Requests.\n\n  Please retry if you think this is a transient or recoverable error.\n( O)> ", time.Time{}},
	{"goose", "  Rate limited — wait a moment and try again.\n( O)> ", time.Time{}},
	{"crush", "  Provider Error\n  You exceeded your current quota, please check your plan and billing details.\n", time.Time{}},
	{"cursor-agent", "  ⚠ Usage limit\n  Upgrade your plan to continue\n  → ", time.Time{}},
}

var limitTestNow = limitLA(2026, 10, 19, 10, 0)

func limitLA(y int, mo time.Month, d, h, m int) time.Time {
	la, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		return time.Time{}
	}
	return time.Date(y, mo, d, h, m, 0, 0, la)
}

func TestHarnessLimitScreens(t *testing.T) {
	if limitTestNow.IsZero() {
		t.Skip("no zone data")
	}
	for _, tc := range limitScreens {
		hit, at := harnessLimitIn(tc.screen, tc.prog, limitTestNow)
		if !hit {
			t.Errorf("%s: limit not seen in %q", tc.prog, tc.screen)
			continue
		}
		if !at.Equal(tc.resets) {
			t.Errorf("%s: resets %v, want %v (%q)", tc.prog, at, tc.resets, tc.screen)
		}
		// An agent run from a path the probe does not know: still seen.
		if hit, _ := harnessLimitIn(tc.screen, "", limitTestNow); !hit {
			t.Errorf("%s: not seen with the program unknown", tc.prog)
		}
	}
}

// Never a person's own text, an agent talking about limits, or one CLI's
// words on another's screen.
func TestHarnessLimitIsNotConversation(t *testing.T) {
	for _, tc := range []struct{ prog, screen, why string }{
		{"claude", "❯ You've hit your session limit · resets 3pm (America/Los_Angeles)", "typed at the prompt"},
		{"claude", "> You've hit your session limit · resets 3pm", "a person's message, echoed"},
		{"claude", "│ > You've hit your usage limit · resets 3pm │", "in the input box"},
		{"claude", "● The report said \"You've hit your session limit · resets 3pm\", so I waited.", "quoted in an answer"},
		{"claude", "● When a session shows Usage limit reached, reminal reports it as limited.", "discussed mid-sentence"},
		{"claude", "  ⎿  You've used 90% of your session limit · resets 3pm (America/Los_Angeles)", "a warning, not the limit"},
		{"claude", "  ⎿  Approaching session limit · resets 3pm", "a warning, not the limit"},
		// Rows that start with a notice's words and go on, or quote it
		// (review).
		{"claude", "● \"You've hit your session limit · resets 3pm\" is what Claude shows when it stops.", "quoted at a row's start"},
		{"claude", "  You've hit your session limit · resets 3pm — the agent now waits.", "a changelog's line"},
		{"claude", "  you've hit your limit · resets 4pm is all it answers.", "wrapped prose"},
		{"claude", "  ⎿  \"Usage limit reached · resets 4pm\",", "a test's string, in a tool's output"},
		{"claude", "● You've hit your session limit · resets 3pm, and the next window\n  opens then.", "a sentence that wraps on"},
		// Claude Code 2.1.312: rows that leave the agent working, and its
		// notices quoted in answers that go on.
		{"claude", "  ⎿  Fast mode disabled · Out of usage credits", "fast mode off, the session carries on"},
		// Bare words at the start of a row with no ⎿ before them (review).
		{"claude", "● It stopped because the review session had run\n  out of usage credits.", "an answer's wrapped last row"},
		{"claude", "  ⎿  Fast mode disabled ·\n     Out of usage credits", "fast mode off, wrapped on a narrow screen"},
		{"claude", "● It prints this:\n\n  Out of usage credits", "shown in an answer"},
		{"claude", "● The banner it showed said its\n  usage limit reached.", "an answer's wrapped last row"},
		{"claude", "  ⎿  Continue now at lower priority after reaching your 5-hour limit; run again to stop", "an option that keeps it going"},
		{"claude", "● Out of usage credits is what it shows when the balance runs out.", "an answer quoting the notice"},
		{"claude", "● You've hit your channel's monthly spend limit. That row is new in this version.", "an answer quoting the notice"},
		// An answer's own sentence, as a notice's words with nothing a notice
		// carries after them (no reset, no command): not the notice (review).
		{"claude", "● You've hit your session limit.", "an answer saying it, no reset or command"},
		{"claude", "● Your org is out of usage.", "an answer saying it, no command"},
		{"opencode", "┃  gpt-5 usage limit reached is what it printed earlier.", "a sentence that starts with a model's id and goes on"},
		{"claude", "● Couldn't reset your 5-hour limit with this login · run /login, then try again, is the row it prints.", "an answer quoting the notice"},
		{"claude", "● A 5-hour limit reset isn't available right now, so I waited.", "an answer quoting the notice"},
		{"claude", "● Automatic continue stopped after repeated usage limit hits · This task will not resume on its own (/rate-limit-options to try again) is the new row.", "an answer quoting the notice"},
		{"claude", "  - You've hit your session limit · resets 3pm", "a list item"},
		{"claude", "  ## You've hit your session limit", "a heading"},
		{"claude", "  | You've hit your session limit · resets 3pm |", "a table cell"},
		// The notice and its command, quoted at the start of an answer that
		// goes on (review).
		{"claude", "● You've hit your weekly limit · /usage-credits to keep using Claude is the row I added a test for; all 40 tests pass.", "an answer quoting the notice and its command"},
		{"claude", "● You've hit your individual spend limit · run /usage-credits to ask your admin for more, that is what it shows.", "an answer quoting the notice and its command"},
		{"codex", "› You’ve hit your usage limit. Try again at 3:05 PM.", "typed at Codex's prompt"},
		{"codex", "• Codex prints “You’ve hit your usage limit” when the plan runs out.", "an answer about it"},
		{"gemini", "│ > Usage limit reached for gemini-2.5-pro. │", "typed at Gemini's prompt"},
		{"goose", "( O)> Rate limit exceeded: what does that mean?", "typed at goose's prompt"},
		{"goose", "  The API answers 429 when the rate limit is exceeded: back off and retry.", "an answer about it"},
		{"opencode", "┃  Add backoff so a usage limit reached error retries later.", "an answer about it"},
		// Another CLI's words, where this one runs: a Codex session whose tool
		// printed Claude Code's notice is not at its limit.
		{"codex", "  ⎿  You've hit your session limit · resets 3pm (America/Los_Angeles)", "Claude Code's notice under Codex"},
		{"claude", "✕ Usage limit reached for gemini-2.5-pro.", "Gemini CLI's notice under Claude Code"},
		// A plain shell: nothing of an agent's here, and no notice that is.
		{"bash", "$ echo \"You've hit your session limit\"", "a shell command"},
	} {
		if hit, _ := harnessLimitIn(tc.screen, tc.prog, limitTestNow); hit {
			t.Errorf("%s (%s): read as at its limit: %q", tc.prog, tc.why, tc.screen)
		}
	}
}

// Seen twice to count, its reset time kept, and over the moment the agent is
// at work again, or the notice has gone.
func TestHarnessLimitState(t *testing.T) {
	if limitTestNow.IsZero() {
		t.Skip("no zone data")
	}
	now := limitTestNow
	defer func() { harnessNow = time.Now }()
	harnessNow = func() time.Time { return now }
	screen := limitScreens[0].screen
	a := &Agent{}
	if a.noteHarnessLimit(screen, "claude", false, time.Time{}, time.Time{}, "") {
		t.Fatal("one glimpse counted")
	}
	if !a.noteHarnessLimit(screen, "claude", false, time.Time{}, time.Time{}, "") {
		t.Fatal("seen twice, not limited")
	}
	if l, at := a.harnessLimitedNow(); !l || !at.Equal(limitLA(2026, 10, 19, 15, 0)) {
		t.Fatalf("limited=%v resets %v", l, at)
	}
	if a.activeRecord(0).Attn != "" {
		t.Fatal("the record's state is the probe's to set")
	}
	a.attnState = harnessLimited
	if r := a.activeRecord(0); !r.ResetsAt.Equal(limitLA(2026, 10, 19, 15, 0)) {
		t.Fatalf("record resets_at %v", r.ResetsAt)
	}
	// At work: over at once.
	if !a.noteHarnessLimit(screen, "claude", true, time.Time{}, time.Time{}, "") {
		t.Fatal("at work and still limited")
	}
	if l, at := a.harnessLimitedNow(); l || !at.IsZero() {
		t.Fatalf("after work: limited=%v resets %v", l, at)
	}
	// The notice it had is from before it worked: not news.
	a.noteHarnessLimit(screen, "claude", false, time.Time{}, time.Time{}, "")
	a.noteHarnessLimit(screen, "claude", false, time.Time{}, time.Time{}, "")
	if l, _ := a.harnessLimitedNow(); l {
		t.Fatal("the notice from before it worked read as the limit again")
	}
	// A new one under it: limited again. Then both scroll away: over after
	// harnessUpFor.
	a.noteHarnessLimit(screen+"\n"+screen, "claude", false, time.Time{}, time.Time{}, "")
	a.noteHarnessLimit(screen+"\n"+screen, "claude", false, time.Time{}, time.Time{}, "")
	a.noteHarnessLimit("❯ ", "claude", false, time.Time{}, time.Time{}, "")
	if l, _ := a.harnessLimitedNow(); !l {
		t.Fatal("one read without it cleared it")
	}
	now = now.Add(harnessUpFor)
	if !a.noteHarnessLimit("❯ ", "claude", false, time.Time{}, time.Time{}, "") {
		t.Fatal("gone for harnessUpFor, still limited")
	}
}

// A turn begun after the limit was found ends it once answered with no
// notice of its own, though its notice is still in the conversation above;
// the harness turning that turn away adds a notice of its own, and the lapse
// holds.
func TestHarnessLimitEndsWithATurn(t *testing.T) {
	if limitTestNow.IsZero() {
		t.Skip("no zone data")
	}
	now := limitTestNow
	defer func() { harnessNow = time.Now }()
	harnessNow = func() time.Time { return now }
	notice := "  ⎿  You've hit your session limit · resets 3pm (America/Los_Angeles)"
	a := &Agent{}
	turn := now.Add(-time.Minute) // the turn the harness turned away
	a.noteHarnessLimit(notice+"\n❯ ", "", false, turn, turn, "")
	a.noteHarnessLimit(notice+"\n❯ ", "", false, turn, turn, "")
	if l, _ := a.harnessLimitedNow(); !l {
		t.Fatal("not limited")
	}
	// After the reset, "continue": a turn begins, and it is answered.
	now = now.Add(5 * time.Hour)
	turn = now
	answered := notice + "\n❯ continue\n● Picking up where I left off.\n❯ "
	if a.noteHarnessLimit(answered, "", false, turn, time.Time{}, "") {
		t.Fatal("a turn begun, not answered yet, ended the limit")
	}
	now = now.Add(time.Second)
	end := now
	a.noteHarnessLimit(answered, "", false, turn, end, "")
	if l, _ := a.harnessLimitedNow(); !l {
		t.Fatal("over the moment its turn ended: its notice may be a read behind")
	}
	if got := a.harnessLimitReadAt(); !got.Equal(end.Add(harnessUpFor)) {
		t.Fatalf("read again at %v, want %v", got, end.Add(harnessUpFor))
	}
	now = end.Add(harnessUpFor)
	if !a.noteHarnessLimit(answered, "", false, turn, end, "") {
		t.Fatal("answered with no notice of its own, still limited")
	}
	for i := 0; i < 3; i++ {
		now = now.Add(time.Second)
		a.noteHarnessLimit(answered, "", false, turn, end, "")
	}
	if l, _ := a.harnessLimitedNow(); l {
		t.Fatal("the old notice, still on screen, read as the limit again")
	}
	// Turned away again: a second notice.
	turn = now
	again := answered + "\n❯ go on\n" + notice + "\n❯ "
	now = now.Add(time.Second)
	a.noteHarnessLimit(again, "", false, turn, now, "")
	now = now.Add(time.Second)
	a.noteHarnessLimit(again, "", false, turn, now.Add(-time.Second), "")
	if l, _ := a.harnessLimitedNow(); !l {
		t.Fatal("a new notice under the old one not read as the limit")
	}
}

// A "continue" its harness turns away is the same lapse: limited throughout,
// its reset time the same (or the new one it says), never a change (review:
// a refused turn read as the lapse's end).
func TestHarnessLimitRefusedTurnHoldsTheLapse(t *testing.T) {
	if limitTestNow.IsZero() {
		t.Skip("no zone data")
	}
	now := limitLA(2026, 10, 19, 13, 0)
	defer func() { harnessNow = time.Now }()
	harnessNow = func() time.Time { return now }
	notice := "  ⎿  You've hit your session limit · resets 3pm (America/Los_Angeles)"
	a := &Agent{}
	turn := now.Add(-5 * time.Second)
	a.noteHarnessLimit(notice+"\n❯ ", "claude", false, turn, now, "")
	a.noteHarnessLimit(notice+"\n❯ ", "claude", false, turn, now, "")
	want := limitLA(2026, 10, 19, 15, 0)
	if l, at := a.harnessLimitedNow(); !l || !at.Equal(want) {
		t.Fatalf("limited=%v resets %v", l, at)
	}
	// 15:01, "continue", turned away with the same words.
	now = limitLA(2026, 10, 19, 15, 1)
	turn = now
	screen := notice + "\n❯ continue\n" + notice + "\n❯ "
	for i := 0; i < 6; i++ {
		if a.noteHarnessLimit(screen, "claude", false, turn, turn.Add(time.Second), "rate_limit") {
			t.Fatalf("read %d: a refused turn changed the state", i)
		}
		now = now.Add(time.Second)
	}
	if l, at := a.harnessLimitedNow(); !l || !at.Equal(want) {
		t.Fatalf("after a refused turn: limited=%v resets %v, want %v", l, at, want)
	}
	// Turned away again, now with a new time: that is the reset now.
	turn = now
	screen += "\n❯ continue\n  ⎿  You've hit your session limit · resets 8pm (America/Los_Angeles)\n❯ "
	a.noteHarnessLimit(screen, "claude", false, turn, turn.Add(time.Second), "rate_limit")
	if l, at := a.harnessLimitedNow(); !l || !at.Equal(limitLA(2026, 10, 19, 20, 0)) {
		t.Fatalf("a new time: limited=%v resets %v", l, at)
	}
}

// Refused again and again: the rows read hold a few notices at most, and
// once full, one more pushes the oldest out — the count stays the same. The
// newest still answers the last message: the lapse holds. And once over, a
// new lapse on that full screen is seen (review).
func TestHarnessLimitHoldsOnAScreenFullOfNotices(t *testing.T) {
	if limitTestNow.IsZero() {
		t.Skip("no zone data")
	}
	now := limitLA(2026, 10, 19, 13, 0)
	defer func() { harnessNow = time.Now }()
	harnessNow = func() time.Time { return now }
	notice := "  ⎿  You've hit your session limit · resets 3pm (America/Los_Angeles)"
	box := "\n────\n❯ \n────\n  ⏵⏵ auto mode on"
	conv := "❯ do the work\n" + notice
	a := &Agent{}
	read := func(turn, end time.Time) bool {
		return a.noteHarnessLimit(harnessTail(conv+box, harnessRows), "", false, turn, end, "")
	}
	turn := now.Add(-2 * time.Second)
	read(turn, turn.Add(time.Second))
	read(turn, turn.Add(time.Second))
	if l, _ := a.harnessLimitedNow(); !l {
		t.Fatal("not limited")
	}
	for i := 1; i <= 8; i++ {
		now = now.Add(time.Minute)
		turn = now
		conv += "\n❯ continue\n" + notice
		read(turn, time.Time{})
		now = now.Add(time.Second)
		end := now
		read(turn, end)
		now = now.Add(harnessUpFor)
		read(turn, end)
		if l, _ := a.harnessLimitedNow(); !l {
			t.Fatalf("refusal %d: read as no longer limited", i)
		}
	}
	// Taken at last: answered with no notice — over.
	now = now.Add(time.Minute)
	turn = now
	conv += "\n❯ continue\n● Picking up where I left off."
	read(turn, time.Time{})
	now = now.Add(time.Second)
	end := now
	read(turn, end)
	now = now.Add(harnessUpFor)
	read(turn, end)
	if l, _ := a.harnessLimitedNow(); l {
		t.Fatal("answered with no notice, still limited")
	}
	// At its limit again, on a screen whose count cannot grow.
	now = now.Add(time.Minute)
	turn = now
	conv += "\n❯ and the next part\n" + notice
	read(turn, turn.Add(time.Second))
	now = now.Add(time.Second)
	read(turn, turn.Add(time.Second))
	if l, _ := a.harnessLimitedNow(); !l {
		t.Fatal("a new lapse on a screen full of notices not seen")
	}
}

// A refused message longer than the rows read: its echo (its one row at the
// prompt's mark) scrolled above them. Its notice, the last thing above the
// input box, still answers it: the lapse holds. A person's draft of many rows
// in the box is no echo either (review).
func TestHarnessLimitHoldsPastALongMessageAndADraft(t *testing.T) {
	if limitTestNow.IsZero() {
		t.Skip("no zone data")
	}
	now := limitLA(2026, 10, 19, 13, 0)
	defer func() { harnessNow = time.Now }()
	harnessNow = func() time.Time { return now }
	notice := "  ⎿  You've hit your session limit · resets 3pm (America/Los_Angeles)"
	box := func(draft ...string) string {
		rule := strings.Repeat("─", 40)
		b := "\n✻ Churned for 0s\n" + rule + "\n❯ "
		if len(draft) > 0 {
			b += draft[0]
			for _, d := range draft[1:] {
				b += "\n  " + d
			}
		}
		return b + "\n" + rule + "\n  ⏵⏵ auto mode on"
	}
	conv := "❯ do the work\n" + notice
	a := &Agent{}
	read := func(tail string, turn, end time.Time) {
		a.noteHarnessLimit(harnessTail(conv+tail, harnessRows), "", false, turn, end, "")
	}
	turn := now.Add(-2 * time.Second)
	read(box(), turn, turn.Add(time.Second))
	read(box(), turn, turn.Add(time.Second))
	if l, _ := a.harnessLimitedNow(); !l {
		t.Fatal("not limited")
	}
	// A message of nine rows, turned away.
	now = now.Add(time.Minute)
	turn = now
	conv += "\n❯ here is the whole plan:"
	for i := 1; i <= 8; i++ {
		conv += "\n  step " + string(rune('0'+i)) + " of the plan"
	}
	conv += "\n" + notice
	read(box(), turn, time.Time{})
	now = now.Add(time.Second)
	end := now
	read(box(), turn, end)
	now = now.Add(harnessUpFor)
	read(box(), turn, end)
	if l, _ := a.harnessLimitedNow(); !l {
		t.Fatal("a long message turned away read as taken: its echo scrolled out of the rows read")
	}
	// Turned away again, and a person's draft of two rows left in the box.
	now = now.Add(time.Minute)
	turn = now
	conv += "\n❯ continue\n" + notice
	draft := box("first line of mine", "second line of mine")
	read(draft, turn, time.Time{})
	now = now.Add(time.Second)
	end = now
	read(draft, turn, end)
	now = now.Add(harnessUpFor)
	read(draft, turn, end)
	if l, _ := a.harnessLimitedNow(); !l {
		t.Fatal("a person's draft in the box read as a message answered")
	}
	for i := 0; i < 12; i++ {
		now = now.Add(time.Second)
		read(draft, time.Time{}, end)
	}
	if l, _ := a.harnessLimitedNow(); !l {
		t.Fatal("a person's draft in the box, read on, ended the lapse")
	}
	// A long message taken: the agent's work under it, no notice in view.
	now = now.Add(time.Minute)
	turn = now
	conv += "\n❯ here is the whole plan again:"
	for i := 1; i <= 8; i++ {
		conv += "\n  step " + string(rune('0'+i)) + " of the plan"
	}
	conv += "\n● On it.\n  ⎿  Read 3 files"
	read(box(), turn, time.Time{})
	now = now.Add(time.Second)
	end = now
	read(box(), turn, end)
	now = now.Add(harnessUpFor)
	read(box(), turn, end)
	if l, _ := a.harnessLimitedNow(); l {
		t.Fatal("a long message taken, still limited")
	}
}

// Claude Code 2.1.78 on says how its turn ended: StopFailure ("rate_limit")
// in Stop's place for one its limit turned away. That record is read, not
// the screen (review): a refusal holds however the screen looks — a long
// message, a screen full of notices, a draft. A plain Stop is no proof (an
// older Claude Code ends a refusal with it): the screen says, as for any
// harness.
func TestHarnessLimitTurnsAreReadFromClaudesHook(t *testing.T) {
	if limitTestNow.IsZero() {
		t.Skip("no zone data")
	}
	now := limitLA(2026, 10, 19, 13, 0)
	defer func() { harnessNow = time.Now }()
	harnessNow = func() time.Time { return now }
	notice := "  ⎿  You've hit your session limit · resets 3pm (America/Los_Angeles)"
	a := &Agent{}
	turn := now.Add(-2 * time.Second)
	screen := notice + "\n❯ "
	a.noteHarnessLimit(screen, "claude", false, turn, turn.Add(time.Second), "rate_limit")
	a.noteHarnessLimit(screen, "claude", false, turn, turn.Add(time.Second), "rate_limit")
	if l, _ := a.harnessLimitedNow(); !l {
		t.Fatal("not limited")
	}
	// Turned away, and the screen shows nothing new it can count or place
	// (the same rows: its long message and its notice scrolled alike).
	for i := 1; i <= 4; i++ {
		now = now.Add(time.Minute)
		turn = now
		a.noteHarnessLimit(screen, "claude", false, turn, time.Time{}, "")
		now = now.Add(time.Second)
		a.noteHarnessLimit(screen, "claude", false, turn, now, "rate_limit")
		now = now.Add(harnessUpFor)
		a.noteHarnessLimit(screen, "claude", false, turn, now.Add(-harnessUpFor), "rate_limit")
		if l, _ := a.harnessLimitedNow(); !l {
			t.Fatalf("refusal %d, its hook saying so: read as no longer limited", i)
		}
	}
	// A plain Stop, a new notice under the message it answered (an older
	// Claude Code's refusal): the screen says turned away — holds.
	now = now.Add(time.Minute)
	turn = now
	screen = notice + "\n❯ continue\n" + notice + "\n❯ "
	a.noteHarnessLimit(screen, "claude", false, turn, time.Time{}, "")
	now = now.Add(time.Second)
	end := now
	a.noteHarnessLimit(screen, "claude", false, turn, end, "")
	now = now.Add(harnessUpFor)
	a.noteHarnessLimit(screen, "claude", false, turn, end, "")
	if l, _ := a.harnessLimitedNow(); !l {
		t.Fatal("a plain Stop with a new notice under its message: read as answered")
	}
	// A plain Stop, the agent's answer under the message: over.
	now = now.Add(time.Minute)
	turn = now
	screen += "\n❯ continue\n● Picking up where I left off.\n❯ "
	a.noteHarnessLimit(screen, "claude", false, turn, time.Time{}, "")
	now = now.Add(time.Second)
	end = now
	a.noteHarnessLimit(screen, "claude", false, turn, end, "")
	now = now.Add(harnessUpFor)
	if !a.noteHarnessLimit(screen, "claude", false, turn, end, "") {
		t.Fatal("answered, by its Stop and its screen: still limited")
	}
}

// Only the limit's errors hold the lapse: a turn ended by another error (the
// service overloaded) with the agent's answer under its message is over, as a
// plain end is (review).
func TestHarnessLimitHeldOnlyByTheLimitsErrors(t *testing.T) {
	if limitTestNow.IsZero() {
		t.Skip("no zone data")
	}
	defer func() { harnessNow = time.Now }()
	notice := "  ⎿  You've hit your session limit · resets 3pm (America/Los_Angeles)"
	for failure, held := range map[string]bool{
		"rate_limit": true, "billing_error": true, "authentication_failed": true,
		"overloaded": false, "server_error": false, "unknown": false,
	} {
		now := limitLA(2026, 10, 19, 13, 0)
		harnessNow = func() time.Time { return now }
		a := &Agent{}
		turn := now.Add(-2 * time.Second)
		screen := notice + "\n❯ "
		a.noteHarnessLimit(screen, "claude", false, turn, turn.Add(time.Second), "rate_limit")
		a.noteHarnessLimit(screen, "claude", false, turn, turn.Add(time.Second), "rate_limit")
		if l, _ := a.harnessLimitedNow(); !l {
			t.Fatalf("%s: not limited", failure)
		}
		// A turn after the reset: the agent answered, and its harness says
		// the turn ended on this error.
		now = now.Add(time.Hour)
		turn = now
		screen += "\n❯ continue\n● Picking up where I left off.\n❯ "
		a.noteHarnessLimit(screen, "claude", false, turn, time.Time{}, "")
		now = now.Add(time.Second)
		end := now
		a.noteHarnessLimit(screen, "claude", false, turn, end, failure)
		now = now.Add(harnessUpFor)
		a.noteHarnessLimit(screen, "claude", false, turn, end, failure)
		if l, _ := a.harnessLimitedNow(); l != held {
			t.Errorf("%s: limited %v, want %v", failure, l, held)
		}
	}
}

// The screen's own reading, with no hook to say: a plan's notice goes on
// with its hint ("/upgrade to increase your usage limit."), and is still the
// last thing above the box; a draft in the box with a list's "- " or a code
// line's backtick is still no echo (review).
func TestHarnessLimitFallbackKnowsAHintAndADraftsRows(t *testing.T) {
	if limitTestNow.IsZero() {
		t.Skip("no zone data")
	}
	now := limitLA(2026, 10, 19, 13, 0)
	defer func() { harnessNow = time.Now }()
	harnessNow = func() time.Time { return now }
	notice := "  ⎿  You've hit your usage limit · resets 9pm (America/Los_Angeles)\n     /upgrade to increase your usage limit."
	rule := strings.Repeat("─", 40)
	box := func(draft ...string) string {
		b := "\n" + rule + "\n❯ "
		if len(draft) > 0 {
			b += draft[0]
			for _, d := range draft[1:] {
				b += "\n  " + d
			}
		}
		return b + "\n" + rule + "\n  ⏵⏵ auto mode on"
	}
	conv := "❯ do the work\n" + notice
	a := &Agent{}
	read := func(tail string, turn, end time.Time) {
		a.noteHarnessLimit(harnessTail(conv+tail, harnessRows), "", false, turn, end, "")
	}
	turn := now.Add(-2 * time.Second)
	read(box(), turn, turn.Add(time.Second))
	read(box(), turn, turn.Add(time.Second))
	if l, _ := a.harnessLimitedNow(); !l {
		t.Fatal("not limited")
	}
	// A message of twenty rows, turned away: its echo scrolled out, the
	// notice and its hint the last things above the box.
	now = now.Add(time.Minute)
	turn = now
	conv += "\n❯ here is the whole plan:"
	for i := 1; i <= 19; i++ {
		conv += "\n  step " + strconv.Itoa(i) + " of the plan"
	}
	conv += "\n" + notice
	read(box(), turn, time.Time{})
	now = now.Add(time.Second)
	end := now
	read(box(), turn, end)
	now = now.Add(harnessUpFor)
	read(box(), turn, end)
	if l, _ := a.harnessLimitedNow(); !l {
		t.Fatal("a long message turned away read as taken: the notice's hint under it")
	}
	// Turned away again, a person's draft with a list and a code line in the
	// box.
	now = now.Add(time.Minute)
	turn = now
	conv += "\n❯ continue\n" + notice
	draft := box("first, the plan:", "- the login", "`make test` after")
	read(draft, turn, time.Time{})
	now = now.Add(time.Second)
	end = now
	read(draft, turn, end)
	now = now.Add(harnessUpFor)
	read(draft, turn, end)
	if l, _ := a.harnessLimitedNow(); !l {
		t.Fatal("a draft's list or code line in the box read as a message answered")
	}
}

// "resets 3pm" read again after 3pm — a resize, any output, the read before
// the press — is the same 3pm, not tomorrow's (review).
// And a reminal started again after it, the notice still on the screen,
// reads it as its hook says the turn it answered ended, not now.
func TestHarnessLimitTimeReadOnceALapse(t *testing.T) {
	if limitTestNow.IsZero() {
		t.Skip("no zone data")
	}
	now := limitLA(2026, 10, 19, 13, 0)
	defer func() { harnessNow = time.Now }()
	harnessNow = func() time.Time { return now }
	screen := "  ⎿  You've hit your session limit · resets 3pm (America/Los_Angeles)\n❯ "
	want := limitLA(2026, 10, 19, 15, 0)
	a := &Agent{}
	a.noteHarnessLimit(screen, "claude", false, time.Time{}, time.Time{}, "")
	a.noteHarnessLimit(screen, "claude", false, time.Time{}, time.Time{}, "")
	now = limitLA(2026, 10, 19, 15, 0).Add(20 * time.Second)
	if a.noteHarnessLimit(screen, "claude", false, time.Time{}, time.Time{}, "") {
		t.Fatal("read again after its time: changed")
	}
	if _, at := a.harnessLimitedNow(); !at.Equal(want) {
		t.Fatalf("read again after its time: resets %v, want %v", at, want)
	}
	// Started again at 15:30; the turn the notice answered ended at 12:59.
	b := &Agent{}
	now = limitLA(2026, 10, 19, 15, 30)
	ended := limitLA(2026, 10, 19, 12, 59)
	b.noteHarnessLimit(screen, "claude", false, ended.Add(-3*time.Second), ended, "")
	b.noteHarnessLimit(screen, "claude", false, ended.Add(-3*time.Second), ended, "")
	if l, at := b.harnessLimitedNow(); !l || !at.Equal(want) {
		t.Fatalf("after a restart: limited=%v resets %v, want %v", l, at, want)
	}
	// No hook to say: read against now, as before.
	c := &Agent{}
	c.noteHarnessLimit(screen, "claude", false, time.Time{}, time.Time{}, "")
	c.noteHarnessLimit(screen, "claude", false, time.Time{}, time.Time{}, "")
	if _, at := c.harnessLimitedNow(); !at.Equal(want.AddDate(0, 0, 1)) {
		t.Fatalf("no hook: resets %v", at)
	}
}

// Times no notice means.
func TestHarnessLimitTimesNotTaken(t *testing.T) {
	if limitTestNow.IsZero() {
		t.Skip("no zone data")
	}
	for _, row := range []string{
		"resetsoct32,3pm",
		"resetsfeb30,1pm",
		"resets3pm(mars/olympus)",
		"tryagainin99999days",
	} {
		if at := parseLimitReset(row, limitTestNow); !at.IsZero() {
			t.Errorf("%q read as %v", row, at)
		}
	}
	if at := parseLimitReset("tryagainin30days", limitTestNow); !at.Equal(limitTestNow.Add(30 * 24 * time.Hour)) {
		t.Errorf("a month's wait read as %v", at)
	}
}

// The login screens of the CLIs the table lacked, as each showed them.
func TestHarnessLoggedOutScreens(t *testing.T) {
	for _, tc := range []struct{ prog, screen string }{
		// goose 1.55, no provider (captured).
		{"goose", "•  Telemetry disabled. You can enable it anytime in settings.\n  error: No provider configured. Run 'goose configure' first."},
		// Crush 0.98, first run (captured).
		{"crush", "  To start, let's choose a provider and model.\n  > Find your fave\n  ↑/↓ choose • enter confirm"},
		{"crush", "ERROR\nNo providers configured - please run 'crush' to set up a provider interactively."},
		// Cursor CLI, `cursor-agent -p` with no login (captured).
		{"cursor-agent", "Error: Authentication required. Please run 'agent login' first, or set CURSOR_API_KEY environment variable."},
		// Qwen Code, `qwen -p` with no auth (captured).
		{"qwen", "No auth type is selected. Please configure an auth type (e.g. via settings or `--auth-type`) before running in non-interactive mode."},
		// OpenCode with a provider and no key (source).
		{"opencode", "┃  Anthropic API key is missing. Pass it using the 'apiKey' parameter or the ANTHROPIC_API_KEY environment variable."},
		// Those already in the table, as captured again on 2026-10-19.
		{"claude", "❯ hi\n  ⎿  Not logged in · Please run /login\n✻ Churned for 0s · done 7:38 PM\n────\n❯ \n────\n  ⏵⏵ auto mode on (shift+tab to cycle)                    Not logged in · Run /login"},
		{"codex", "  Welcome to Codex, OpenAI's command-line coding agent\n  Sign in with ChatGPT to use Codex as part of your paid plan\n> 1. Sign in with ChatGPT"},
		{"gemini", "│ ? Get started │\n│   How would you like to authenticate for this project? │\n│   ● 1. Sign in with Google │"},
		{"qwen", "  │ Connect a Provider │\n  │ › QwenCloud │"},
		{"cursor-agent", "        Cursor Agent\n        v2026.10.18-ea085cb\n        Press any key to log in..."},
	} {
		if !harnessProblemIn(tc.screen, tc.prog) {
			t.Errorf("%s: logged out not seen in %q", tc.prog, tc.screen)
		}
	}
	// And a goose that is set up, talking about configuring: not logged out.
	if harnessProblemIn("  To use another model, run goose configure and pick a provider.\n( O)> ", "goose") {
		t.Error("goose's advice read as its login screen")
	}
	if harnessProblemIn(strings.Repeat("x", 3), "crush") {
		t.Error("nothing read as crush logged out")
	}
}

// Only an answer's glyph ends an echo: a draft's list item or code line in
// the box goes on to its rule (review).
func TestLimitEchoRowEndsOnlyAtAnAnswer(t *testing.T) {
	rule := strings.Repeat("─", 40)
	for _, c := range []struct {
		below []string
		echo  bool
	}{
		{[]string{"  ⎿  You've hit your session limit · resets 3pm"}, true},
		{[]string{"  second line", "● On it."}, true},
		{[]string{"  - the login", "  `make test` after", rule}, false},
		{[]string{"  * one", "  # two", rule}, false},
		{[]string{rule}, false},
		{nil, false},
	} {
		if got := limitEchoRow(c.below); got != c.echo {
			t.Errorf("%q: echo %v, want %v", c.below, got, c.echo)
		}
	}
}

// Claude Code at an individual spend limit, as three sessions showed it
// (2026-10-27): the notice, the command it points to on a row of its own,
// the turn's time ("· done"), and the input box empty, with a draft, or with
// an attachment chip. Each read as limited, though its turn ended with an
// ordinary Stop and the screen looks done; with the message it answered in
// view, and scrolled away.
func TestHarnessLimitSpendScreens(t *testing.T) {
	if limitTestNow.IsZero() {
		t.Skip("no zone data")
	}
	notice := "  ⎿  You've hit your individual spend limit · run /usage-credits to ask your admin for a higher limit\n" +
		"     /usage-credits to request more usage from your admin.\n\n" +
		"✻ Churned for 1s · done 12:34 PM\n\n"
	for _, echo := range []string{"❯ continue\n\n", ""} {
		for _, draft := range []string{"", "c", "[⧉ In Untitled-1]"} {
			screen := echo + notice + claudeBoxWith(draft)
			if !limitedAfterTurn(t, screen) {
				t.Errorf("not limited (echo %q, draft %q):\n%s", echo, draft, screen)
			}
		}
	}
}

// An answer that quotes the notice and its command, then goes on, is the
// agent talking, not the limit: its session is not limited (review).
func TestHarnessLimitQuotedInAnAnswer(t *testing.T) {
	if limitTestNow.IsZero() {
		t.Skip("no zone data")
	}
	for _, row := range []string{
		"● You've hit your weekly limit · /usage-credits to keep using Claude is the row I added a test for; all 40 tests pass.",
		"● You've hit your individual spend limit · run /usage-credits to ask your admin for more, that is what it shows.",
	} {
		screen := "❯ what did you change?\n\n" + row + "\n\n✻ Churned for 3s · done\n\n" + claudeBoxWith("")
		if limitedAfterTurn(t, screen) {
			t.Errorf("read as limited:\n%s", screen)
		}
	}
}

// A notice Claude Code draws as an answer (●) or as a tool's output (⎿) is
// that answer's last row, with only its hint under it: one that quotes a
// notice and goes on, below it, after a blank row or after an indented "> "
// quote, is not it; nor is one quoted or under an answer's own words (review).
func TestHarnessLimitAnswerDrawnNoticeIsItsLastRow(t *testing.T) {
	for _, tc := range []struct {
		screen string
		want   bool
	}{
		{"● You've hit your limit · resets 11:15am (America/Los_Angeles)\n❯ ", true},
		{"● You've hit your weekly limit · /usage-credits to keep using Claude\n❯ ", true},
		{"● You've hit your session limit · resets 3pm\n\n✻ Churned for 0s\n\n" + claudeBoxWith(""), true},
		{"● You've hit your session limit · resets 3pm", true},
		{"● You've hit your session limit · resets 3pm (America/Los_Angeles)\n  That is the row it drew before it stopped.\n❯ ", false},
		{"● You've hit your session limit · resets 3pm\n\n  So the review waits until then.\n❯ ", false},
		{"● You've hit your session limit · resets 3pm\n● Next I'll pick up the deploy.\n❯ ", false},
		{"● Here is what it showed:\n\n  You've hit your session limit · resets 3pm (America/Los_Angeles)\n❯ ", false},
		{"● `You've hit your session limit · resets 3pm`\n❯ ", false},
		{"● \"You've hit your session limit · resets 3pm\"\n❯ ", false},
		{"● You've hit your session limit · resets 3pm\n  > quoted from the log\n  and so it stopped.\n❯ ", false},
		{"  ⎿  You've hit your session limit · resets 3pm\n     /upgrade to increase your usage limit.\n❯ ", true},
		{"  ⎿  Usage limit reached\n\n✻ Churned for 0s\n\n" + claudeBoxWith(""), true},
		{"  ⎿  You've hit your session limit.\n  That row is new.\n❯ ", false},
		{"  ⎿  Usage limit reached\n\n  So the tests wait.\n❯ ", false},
		// A hint is the notice's own, named: a sentence that starts with a
		// command is the answer going on.
		{"  ⎿  You've hit your session limit · resets 3pm\n     Run /usage-credits to ask your admin for more\n❯ ", true},
		{"● You've hit your session limit · resets 3pm (America/Los_Angeles)\n  /usage shows how much is left, so I'll wait.\n❯ ", false},
		{"● You've hit your session limit · resets 3pm (America/Los_Angeles)\n  Run /review once it resets and we're done.\n❯ ", false},
		{"  ⎿  You've hit your session limit · resets 3pm\n     /usage shows the rest of the week.\n❯ ", false},
	} {
		if hit, _ := harnessLimitIn(tc.screen, "claude", limitTestNow); hit != tc.want {
			t.Errorf("hit=%v, want %v: %q", hit, tc.want, tc.screen)
		}
	}
}

// OpenCode's notice may start with a model's id in lower case: one with a
// digit, a known maker's or one OpenCode serves by name, not any
// hyphenated word (review).
func TestHarnessLimitModelLedNotice(t *testing.T) {
	for _, tc := range []struct {
		screen string
		want   bool
	}{
		{"┃  gpt-5 usage limit reached. It will reset in 2 hours.\n┃", true},
		{"┃  claude-sonnet usage limit reached. It will reset in 2 hours.\n┃", true},
		{"┃  qwen3-coder usage limit reached. It will reset in 2 hours.\n┃", true},
		{"┃  big-pickle usage limit reached. It will reset in 2 hours.\n┃", true},
		{"┃  re-run usage limit reached. It will reset in 2 hours.\n┃", false},
		{"┃  a-b usage limit reached. It will reset in 2 hours.\n┃", false},
		{"┃  gpt-5 usage limit reached is what it printed earlier.", false},
	} {
		if hit, _ := harnessLimitIn(tc.screen, "opencode", limitTestNow); hit != tc.want {
			t.Errorf("hit=%v, want %v: %q", hit, tc.want, tc.screen)
		}
	}
}

// claudeBoxWith is Claude Code's input box holding draft, and the footer
// under it (claudeBox, in attention_probe_test.go, is the box alone).
func claudeBoxWith(draft string) string {
	return "────────────────────────────────\n❯ " + draft + "\n────────────────────────────────\n  ⏵⏵ bypass permissions on"
}

// limitedAfterTurn is whether a Claude session whose turn ended with screen
// on show reads as at its limit: the screen seen twice, then once more after
// the notice has held for harnessUpFor.
func limitedAfterTurn(t *testing.T, screen string) bool {
	t.Helper()
	now := limitTestNow
	defer func() { harnessNow = time.Now }()
	harnessNow = func() time.Time { return now }
	a := &Agent{}
	turn, end := now.Add(-2*time.Second), now.Add(-time.Second)
	a.noteHarnessLimit(screen, "claude", false, turn, end, "")
	a.noteHarnessLimit(screen, "claude", false, turn, end, "")
	now = now.Add(harnessUpFor)
	a.noteHarnessLimit(screen, "claude", false, turn, end, "")
	l, _ := a.harnessLimitedNow()
	return l
}

// Each notice the later releases added, as an agent's own text: a sentence
// that goes on after its words, its words quoted or mid-sentence, a
// sentence that wraps on with them; and a notice whose words are plain
// enough to end anyone's sentence, with no glyph before it, as a wrapped
// sentence's last row. None is the notice.
func TestHarnessLimitNewWordingsAreWholeRows(t *testing.T) {
	const carryOn = ", so I paused the deploy and will pick it up after lunch."
	for _, n := range []struct {
		prog, glyph, text string
		bare              bool // its words are the notice only after its glyph
	}{
		{"claude", "⎿  ", "You're out of usage credits. /model to switch models.", false},
		{"claude", "⎿  ", "You're out of usage credits. Run /usage-credits to keep using Claude", false},
		{"claude", "⎿  ", "Your org is out of usage · add funds to continue", false},
		{"claude", "⎿  ", "Your org is out of usage · contact your admin", false},
		{"claude", "⎿  ", "You're out of extra usage", false},
		{"claude", "⎿  ", "You've reached your Fable limit.", false},
		{"claude", "⎿  ", "Your organization is out of usage credits. Contact your admin to add more.", false},
		{"claude", "⎿  ", "You've hit your monthly limit · run /usage-credits to adjust", false},
		{"claude", "⎿  ", "Fable limit reached", true},
		{"claude", "⎿  ", "Usage limit reached · continuing automatically at 3pm · esc to cancel", true},
		{"codex", "■ ", "Your workspace is out of credits. Add credits to continue.", false},
		{"codex", "■ ", "Your workspace is out of credits. Ask your workspace owner to refill in order to continue.", false},
		{"codex", "■ ", "You've reached your workspace credit limit", false},
		{"codex", "■ ", "Your included usage is exhausted. Choose an option below to continue.", false},
		{"codex", "■ ", "Request a limit increase from your owner to continue using codex. Request increase?", false},
		{"codex", "■ ", "Request a limit increase from your owner to continue using codex.", false},
		{"claude", "⎿  ", "Goal paused · usage limit reached · send a message after it resets to continue", false},
		{"claude", "⎿  ", "Goal paused · usage limit reached · continues automatically when it resets", false},
		{"claude", "⎿  ", "This request exceeds your group's budget. Ask your organization's admins to raise it, or try again after it resets on November 3 at 14:00 UTC.", false},
		{"codex", "■ ", "You’ve hit your usage limit. Visit https://chatgpt.com/settings/usage to purchase more credits or try again at 3:05 PM.", false},
		{"gemini", "✕ ", "Rate limit exceeded. Try again later.", false},
		{"gemini", "✕ ", "Rate limit exceeded", true},
		{"gemini", "✕ ", "You have exhausted your daily quota on this model.", false},
		{"gemini", "✕ ", "Usage limit reached for gemini-2.5-pro.", false},
		{"gemini", "✕ ", "Please wait and try again later. To increase your limits, request a quota increase through AI Studio, or switch to another /auth method", false},
		{"qwen", "✕ ", "Quota exhausted", true},
		{"qwen", "✕ ", "Rate limit exceeded", true},
		{"qwen", "✕ ", "Rate limit exceeded. Please wait and try again.", false},
		{"qwen", "✕ ", "Model API rate limit exceeded (HTTP 429). Wait and try again later.", false},
		{"qwen", "✕ ", "Qwen OAuth quota exceeded", false},
		{"qwen", "✕ ", "Possible quota limitations in place or slow response times detected. Please wait and try again later.", false},
		// Claude Code 2.1.319: an organization whose usage is paused.
		{"claude", "⎿  ", "Usage is paused for your organization. For help, please contact support.", false},
		{"claude", "⎿  ", "Goal paused · usage is paused for your organization", false},
		// Qwen Code 0.26.9: its credits.
		{"qwen", "✕ ", "You've used up your credits. Please try again after the reset.", false},
		{"qwen", "✕ ", "You've used up your weekly credits. Please try again after the reset.", false},
		{"qwen", "✕ ", "You've used up your weekly credits. You can wait or run /upgrade to get more credits.", false},
		{"qwen", "✕ ", "Your credits are expected to reset in 3 hours.", false},
		{"qwen", "✕ ", "Your credits are expected to reset in 2 days 4 hours.", false},
		{"opencode", "┃  ", "Usage limit reached. To continue using this model now, enable usage from your available balance", false},
		{"opencode", "┃  ", "Free limit reached", false},
		{"opencode", "┃  ", "Go limit reached", false},
		{"opencode", "┃  ", "Free usage exceeded, subscribe to Go", false},
		{"opencode", "┃  ", "Quota exceeded. Check your plan and billing details.", false},
		{"goose", "", "Credits exhausted", false},
		{"goose", "", "Ran into this error: Credits exhausted.", false},
		{"goose", "", "Please add credits to your account, then resend your message to continue.", false},
		{"goose", "", "Rate limited — wait a moment and try again.", false},
		{"amp", "", "You are out of credits. Add paid credits or choose a paid tier.", false},
		{"amp", "", "Add credits to keep using Amp right now, or wait until the next hour starts for more free usage.", false},
		{"amp", "", "Rate limit exceeded. Try again shortly.", false},
		{"amp", "", "Usage Quota Exceeded", false},
		{"pi", "", "Monthly usage limit reached", false},
		{"pi", "", "You've reached your ChatGPT workspace's usage limit. Ask your workspace admin to increase it at admin.openai.com → Workspace → Usage limits.", false},
		{"copilot", "", "You've hit your rate limit. Please wait for your limit to reset", false},
		{"copilot", "", "You've hit your rate limit. Please wait for your limit to reset or switch to auto model to continue", false},
		{"copilot", "", "You've hit your session rate limit.", false},
		{"copilot", "", "You've reached your weekly rate limit.", false},
		{"copilot", "", "You've hit the rate limit for this model. Please switch models or wait for your limit to reset", false},
		{"copilot", "", "You've been rate limited on the current model. Would you like to switch to gpt-5.1 to continue?", false},
		{"copilot", "", "You've used all your Copilot Free chat requests for the month.", false},
		{"pi", "", "Hugging Face rate limit reached; retry in 30s", false},
		{"agy", "", "AI: Out of credits", false},
	} {
		words := strings.TrimRight(n.text, ".")
		if hit, _ := harnessLimitIn("  "+n.glyph+n.text+"\n", n.prog, limitTestNow); !hit {
			t.Errorf("%s: the notice itself not seen: %q", n.prog, n.glyph+n.text)
		}
		for why, screen := range map[string]string{
			"a sentence that goes on":       "  " + n.glyph + words + carryOn,
			"an answer that goes on":        "● " + words + carryOn,
			"quoted mid-sentence":           "● The screen said " + words + " and the agent stopped.",
			"quoted at a row's start":       "  \"" + n.text + "\" is what it prints.",
			"a sentence that wraps on":      "● It stopped after this:\n  " + words + carryOn,
			"wrapped, going on on the next": "● It stopped after this:\n  " + words + "\n  and then the agent went on with the next file.",
		} {
			if hit, _ := harnessLimitIn(screen, n.prog, limitTestNow); hit {
				t.Errorf("%s (%s): read as at its limit: %q", n.prog, why, screen)
			}
		}
		if n.bare {
			screen := "● It stopped because the review session said\n  " + strings.ToLower(n.text[:1]) + n.text[1:]
			if hit, _ := harnessLimitIn(screen, n.prog, limitTestNow); hit {
				t.Errorf("%s (a wrapped sentence's last row, no glyph): read as at its limit: %q", n.prog, screen)
			}
		}
	}
	// Found in review: a sentence's wrapped last row, Claude Code's new
	// forms in an answer or a code block, a Codex bullet, an answer naming a
	// sum, a status word in a sentence.
	for _, tc := range []struct{ prog, screen string }{
		{"claude", "● I looked at the billing page. For your account it now says that\n  you're out of extra usage."},
		{"goose", "  The provider said the account's\n  credits exhausted."},
		{"amp", "  The dashboard shows the\n  usage quota exceeded."},
		{"pi", "  It stopped once the\n  monthly usage limit reached."},
		{"agy", "  The status bar then read\n  AI: out of credits."},
		{"claude", "● You've reached your usage limit."},
		{"claude", "  You've reached your monthly limit."},
		{"claude", "● The message is:\n\n      Your organization is out of usage credits. Contact your admin to add more."},
		{"codex", "• Your workspace is out of credits. Add credits to continue."},
		{"amp", "  You've exceeded your usage limit of $20."},
		{"amp", "  You've reached your free usage limit"},
	} {
		if hit, _ := harnessLimitIn(tc.screen, tc.prog, limitTestNow); hit {
			t.Errorf("%s: read as at its limit: %q", tc.prog, tc.screen)
		}
	}
	// What leaves the agent going: Claude Code going on now on another
	// model, or once its limit has reset.
	for _, screen := range []string{
		"  ⎿  Fable limit reached · continuing on Opus uses usage credits",
		"  ⎿  Usage limit reset · Continuing automatically",
		"  ⎿  [Usage limit reached — grace window active. Checkpoint now: finish the",
	} {
		if hit, _ := harnessLimitIn(screen, "claude", limitTestNow); hit {
			t.Errorf("claude: read as at its limit: %q", screen)
		}
	}
}

// A screen of long rows, as fast to read as one of short ones: notices are
// joined with the rows under them only up to the longest a notice reads
// (review: 200 rows of 2,220 characters took 438 ms for OpenCode, 747 ms
// with the program not known).
func BenchmarkHarnessLimitLongRows(b *testing.B) {
	row := strings.Repeat("word ", 444)
	screen := strings.Repeat(row+"\n", 200)
	for _, prog := range []string{"opencode", ""} {
		b.Run("prog="+prog, func(b *testing.B) {
			for b.Loop() {
				harnessLimitIn(screen, prog, limitTestNow)
			}
		})
	}
}

// While Claude Code waits to go on by itself after a usage limit, its footer
// says so under its input box (2.1.312): read there, whole or as its status
// row's right-hand part, it is at its limit. The same words in an answer
// above the box, a footer row that goes on in words, and another CLI's
// screen are not (review).
func TestHarnessLimitClaudeWaitFooter(t *testing.T) {
	if limitTestNow.IsZero() {
		t.Skip("no zone data")
	}
	const (
		rule   = "────────────────────────────────"
		status = "  Usage limit reached · limit resets 3pm (America/Los_Angeles)"
		next   = "  Continuing automatically at 3pm (America/Los_Angeles) · Esc to cancel"
	)
	box := "● Done.\n" + rule + "\n❯ \n" + rule + "\n"
	for _, tc := range []struct {
		screen string
		resets time.Time
	}{
		{box + status + "\n" + next, limitLA(2026, 10, 19, 15, 0)},
		{box + status, limitLA(2026, 10, 19, 15, 0)},
		{box + next, limitLA(2026, 10, 19, 15, 0)},
		{box + "  ⏵⏵ auto mode on (shift+tab to cycle)    Usage limit reached · limit resets 3pm (America/Los_Angeles)\n" + next, limitLA(2026, 10, 19, 15, 0)},
		{box + "  Usage limit reached · limit resets 11:15am\n  Continuing automatically at 11:15am · Esc to cancel", limitLA(2026, 10, 19, 11, 15)},
		{box + "  Usage limit reached\n  Continuing automatically when it resets · Esc to cancel", time.Time{}},
		{box + "  Usage limit reached · limit resets 3pm\n  Continuing shortly · Esc to cancel", limitLA(2026, 10, 19, 15, 0)},
	} {
		for _, prog := range []string{"claude", ""} {
			hit, at := harnessLimitIn(tc.screen, prog, limitTestNow)
			if !hit || !at.Equal(tc.resets) {
				t.Errorf("as %q: hit=%v resets %v, want true, %v:\n%s", prog, hit, at, tc.resets, tc.screen)
			}
		}
	}
	for why, tc := range map[string]struct{ prog, screen string }{
		"in an answer above the box":   {"claude", "● Usage limit reached · limit resets 3pm (America/Los_Angeles)\n  Continuing automatically at 3pm (America/Los_Angeles) · Esc to cancel\n" + rule + "\n❯ \n" + rule},
		"the same, no program known":   {"", "● Usage limit reached · limit resets 3pm (America/Los_Angeles)\n  Continuing automatically at 3pm (America/Los_Angeles) · Esc to cancel\n" + rule + "\n❯ \n" + rule},
		"a footer row going on":        {"claude", box + status + ", so I'm waiting"},
		"a status row's part going on": {"claude", box + "  ⏵⏵ auto mode on    Usage limit reached · limit resets 3pm and then more"},
		"as Codex":                     {"codex", box + status + "\n" + next},
		"as Gemini CLI":                {"gemini", box + status + "\n" + next},
		"no Claude box, no program":    {"", "• Done.\n\n› Ask Codex to do anything\n" + status + "\n" + next},
	} {
		if hit, _ := harnessLimitIn(tc.screen, tc.prog, limitTestNow); hit {
			t.Errorf("%s (as %q): read as at its limit:\n%s", why, tc.prog, tc.screen)
		}
	}
	// A person's draft in the box, with rules of its own (a pasted screen):
	// the box ends at its lowest rule. And a program drawn over Claude Code
	// under a box: the footer is the screen's last rows (review).
	shortcuts := "\n" + rule + "\n  ? for shortcuts"
	for why, screen := range map[string]string{
		"a draft with a rule":        "● Done.\n" + rule + "\n❯ look at this:\n  " + rule + "\n" + status + shortcuts,
		"a draft with dashes":        "● Done.\n" + rule + "\n❯ notes\n  --------\n" + status + "\n" + next + shortcuts,
		"a pasted screen in a draft": "● Done.\n" + rule + "\n❯ it showed:\n  " + rule + "\n  ❯\n  " + rule + "\n" + status + shortcuts,
		"an editor over it":          rule + "\n❯ \n" + rule + "\n" + status + "\n~\n~\n\"notes.txt\" 3L, 40B",
		"an editor in insert mode":   rule + "\n❯ \n" + rule + "\n" + status + "\n-- INSERT --",
		"a pager's prompt":           rule + "\n❯ \n" + rule + "\n" + status + "\n:",
		"a pager at its end":         rule + "\n❯ \n" + rule + "\n" + status + "\n(END)",
		"an editor's mode, padded":   rule + "\n❯ \n" + rule + "\n" + status + "\n-- INSERT --   ",
		"an editor recording":        rule + "\n❯ \n" + rule + "\n" + status + "\n-- INSERT --recording @q",
		"an editor's visual line":    rule + "\n❯ \n" + rule + "\n" + status + "\n-- VISUAL LINE --",
		"a pager's prompt, padded":   rule + "\n❯ \n" + rule + "\n" + status + "\n:   ",
		"a pager at its end, padded": rule + "\n❯ \n" + rule + "\n" + status + "\n(END)   ",
		"a dialog under it":          rule + "\n❯ earlier message\n" + rule + "\n" + status + "\n╭──────────────────╮\n│ Allow this?      │\n│ ❯ 1. Yes         │\n╰──────────────────╯",
	} {
		for _, prog := range []string{"claude", ""} {
			if hit, _ := harnessLimitIn(screen, prog, limitTestNow); hit {
				t.Errorf("%s (as %q): read as at its limit:\n%s", why, prog, screen)
			}
		}
	}
	// A status line of its own that draws a bare bar under the footer: the
	// box still ends at the rule that matches its top (review).
	for _, bar := range []string{"  ━━━━────", rule, "----------"} {
		screen := box + status + "\n" + next + "\n" + bar
		for _, prog := range []string{"claude", ""} {
			if hit, _ := harnessLimitIn(screen, prog, limitTestNow); !hit {
				t.Errorf("a status line's bar %q under it (as %q): not read as at its limit", bar, prog)
			}
		}
	}
	// Claude Code's own vim mode, indented under its box (2.1.312 draws
	// "-- INSERT --" and "-- VISUAL --", none in NORMAL), and a screen
	// redrawn at a new width, its two rules unlike (review).
	narrow := strings.Repeat("─", 24)
	for why, screen := range map[string]string{
		"vim mode on its own row, above":  box + "  -- INSERT --\n" + status + "\n" + next,
		"vim mode on its own row, below":  box + status + "\n" + next + "\n  -- INSERT --",
		"vim mode leading the status row": box + "  -- VISUAL -- ⏵⏵ auto mode on    Usage limit reached · limit resets 3pm (America/Los_Angeles)\n" + next,
		"NORMAL, were it drawn":           box + "  -- NORMAL --\n" + status + "\n" + next,
		"rules of two widths":             "● Done.\n" + rule + "\n❯ \n" + narrow + "\n" + status + "\n" + next,
	} {
		for _, prog := range []string{"claude", ""} {
			if hit, _ := harnessLimitIn(screen, prog, limitTestNow); !hit {
				t.Errorf("%s (as %q): not read as at its limit:\n%s", why, prog, screen)
			}
		}
	}
	stale := box + status + "\n" + next + "\n● Back to work.\n" + narrow + "\n❯ \n" + strings.Repeat("─", 20)
	for _, prog := range []string{"claude", ""} {
		if hit, _ := harnessLimitIn(stale, prog, limitTestNow); hit {
			t.Errorf("an older frame's footer above a live box (as %q): read as at its limit:\n%s", prog, stale)
		}
	}
	if !limitedAfterTurn(t, box+status+"\n"+next) {
		t.Error("a session waiting to go on by itself is not held as limited")
	}
}
