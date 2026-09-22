// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"testing"

	"github.com/charmbracelet/x/vt"
)

// The bottom of Claude Code's screen after a message reached it logged out,
// as it was seen on a phone (2026-09-18).
const loggedOutScreen = `● Login expired · Please run /login

✻ Churned for 0s · done 7:33 AM

────────────────────────────────────────────────────
❯
────────────────────────────────────────────────────
  ⏵⏵ auto mode on (shift+tab to cycle) · ← for agents          Not logged in · Run /login
                                                        ✔ Update installed · Restart to update`

// An agent at work on login code, saying so in its answer just above the
// prompt — which must not read as the agent itself being logged out.
const proseScreen = `● The bug was that the session check ran before the cookie was set, so a
  user who had just signed up was not logged in on the first page load, and
  the redirect to /login looped. Fixed in auth/session.go.

────────────────────────────────────────────────────
❯
────────────────────────────────────────────────────
  ⏵⏵ auto mode on (shift+tab to cycle) · ← for agents`

// The same screen as Claude Code actually leaves it — spaces drawn as
// cursor moves, so the words run together (read from vm-two, 2026-09-18).
const loggedOutScreenGlued = `●Loginexpired·Pleaserun/login

✻Brewedfor0s·done7:34AM

──────────────────────────────────────────────────────
❯
──────────────────────────────────────────────────────
⏵⏵automodeon (shift+tabtocycle)·←foragentsNotloggedin·Run/login
✔Updateinstalled·Restarttoupdate`

func TestHarnessLoggedOutIsSeenWithoutSpaces(t *testing.T) {
	if !harnessProblemIn(attentionProbeTail(loggedOutScreenGlued, harnessRows)) {
		t.Fatal("a logged-out Claude Code screen, as rendered, was not recognised")
	}
	footer := "────\n❯\n────\n⏵⏵automodeon (shift+tabtocycle)·←foragentsNotloggedin·Run/login"
	if !harnessProblemIn(footer) {
		t.Fatal("the status line alone, as rendered, was not recognised")
	}
	gluedProse := "●Thebugwasthatauserwhohadjustsignedupwasnotloggedinonthefirstpageload,andthe\n redirectto/loginlooped.Fixedinauth/session.go."
	if harnessProblemIn(gluedProse) {
		t.Fatal("an agent's prose about logins, run together, was read as a lapsed login")
	}
}

func TestHarnessLoggedOutIsSeenAndProseIsNot(t *testing.T) {
	if !harnessProblemIn(attentionProbeTail(loggedOutScreen, harnessRows)) {
		t.Fatal("a logged-out Claude Code screen was not recognised")
	}
	footerOnly := "────\n❯\n────\n  ⏵⏵ auto mode on (shift+tab to cycle)          Not logged in · Run /login"
	if !harnessProblemIn(footerOnly) {
		t.Fatal("the status line's \"Not logged in\" alone was not recognised")
	}
	if harnessProblemIn(attentionProbeTail(proseScreen, harnessRows)) {
		t.Fatal("an agent's own words about logins were read as its login lapsing")
	}
}

// Seen twice in a row it counts; it must be gone for a while to clear.
func TestHarnessHealthDebounces(t *testing.T) {
	a := &Agent{}
	a.noteHarnessHealth(loggedOutScreen)
	if a.harnessDown() {
		t.Fatal("down after one glimpse")
	}
	a.noteHarnessHealth(loggedOutScreen)
	if !a.harnessDown() {
		t.Fatal("not down after two")
	}
	for i := 0; i < harnessUpAfter-1; i++ {
		a.noteHarnessHealth(proseScreen)
	}
	if !a.harnessDown() {
		t.Fatal("cleared too soon")
	}
	a.noteHarnessHealth(proseScreen)
	if a.harnessDown() {
		t.Fatal("did not clear once the login came back")
	}
}

// End to end through the terminal emulator the probe reads: Claude Code's
// bytes — styled words, spaces as cursor moves — rendered, then judged.
// (Hand-written screens hid that the render carries its styling.)
func TestHarnessLoggedOutThroughTheEmulator(t *testing.T) {
	e := vt.NewEmulator(100, 10)
	_, _ = e.Write([]byte("\x1b[38;5;174m\u25cf\x1b[39m \x1b[1mLogin\x1b[1Cexpired\x1b[0m\x1b[1C\u00b7\x1b[1CPlease\x1b[1Crun\x1b[1C/login\r\n\r\n"))
	_, _ = e.Write([]byte("\x1b[2m\u23f5\u23f5 auto mode on\x1b[0m\x1b[30C\x1b[31mNot\x1b[1Clogged\x1b[1Cin\x1b[1C\u00b7\x1b[1CRun\x1b[1C/login\x1b[0m"))
	a := &Agent{}
	a.noteHarnessHealth(e.Render())
	a.noteHarnessHealth(e.Render())
	if !a.harnessDown() {
		t.Fatalf("not seen through the emulator; render was %q", e.Render())
	}
}

// Other CLIs start on a sign-in screen when they have no login. These are the
// bottoms of their real screens.
func TestHarnessSignInScreensAreLoggedOut(t *testing.T) {
	screens := map[string]string{
		"agy": " Welcome to the Antigravity CLI. You are currently not signed in.\n Select login method:\n" +
			" > 1. Google OAuth\n   2. Use a Google Cloud project\n   ↑/↓ Navigate · enter Select",
		// As they are drawn: a dialog is mostly its own blank rows.
		"gemini": "│   How would you like to authenticate for this project?                       │\n" +
			"│                                                                              │\n" +
			"│   No authentication method selected.                                         │\n" +
			"│                                                                              │\n" +
			"│   (Use Enter to select)                                                      │\n" +
			"│                                                                              │\n" +
			"│   Terms of Services and Privacy Notice for Gemini CLI                        │\n" +
			"│                                                                              │\n" +
			"│   https://geminicli.com/docs/resources/tos-privacy/                          │\n" +
			"╰──────────────────────────────────────────────────────────────────────────────╯\n" +
			"│   ● 1. Sign in with Google                                                   │\n" +
			"│     2. Use Gemini API Key                                                    │\n" +
			"│   (Use Enter to select)                                                      │",
		"amp":                "No API key found. Starting login flow...\nWould you like to log in to Amp? [(y)es, (n)o]: ",
		"codex":              "  Sign in with ChatGPT to use Codex as part of your paid plan\n  › 1. Sign in with ChatGPT\n    2. Provide your own API key",
		"codex, turned away": "■ unexpected status 401 Unauthorized: Incorrect API key provided:\nkey at https://platform.openai.com/account/api-keys.",
		"qwen": "  │ Alibaba ModelStudio · Access Method                                      │\n" +
			"  │                                                                          │\n" +
			"  │ › Coding Plan                                                            │\n" +
			"  │   For individual developers · Weekly quota included                      │\n" +
			"  │                                                                          │\n" +
			"  │   Token Plan                                                             │\n" +
			"  │                                                                          │\n" +
			"  │   Standard API Key                                                       │\n" +
			"  │   Connect with an existing ModelStudio API key                           │\n" +
			"  │                                                                          │\n" +
			"  │ Enter to select, ↑↓ to navigate, Esc to go back                          │\n" +
			"  └──────────────────────────────────────────────────────────────────────────┘",
	}
	for name, s := range screens {
		if !harnessProblemIn(harnessTail(s, harnessRows)) {
			t.Errorf("%s's sign-in screen was not seen as logged out", name)
		}
	}
	for _, ok := range []string{
		"  › Ask Codex to do anything   ? for shortcuts",
		" Type your message or @path/to/file",
		"I added a sign-in page; the user signs in with Google.",
	} {
		if harnessProblemIn(ok) {
			t.Errorf("read as logged out: %q", ok)
		}
	}
}
