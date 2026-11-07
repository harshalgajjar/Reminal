// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"

	"reminal/internal/session"
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
	if !harnessProblemIn(attentionProbeTail(loggedOutScreenGlued, harnessRows), "claude") {
		t.Fatal("a logged-out Claude Code screen, as rendered, was not recognised")
	}
	footer := "────\n❯\n────\n⏵⏵automodeon (shift+tabtocycle)·←foragentsNotloggedin·Run/login"
	if !harnessProblemIn(footer, "claude") {
		t.Fatal("the status line alone, as rendered, was not recognised")
	}
	gluedProse := "●Thebugwasthatauserwhohadjustsignedupwasnotloggedinonthefirstpageload,andthe\n redirectto/loginlooped.Fixedinauth/session.go."
	if harnessProblemIn(gluedProse, "claude") {
		t.Fatal("an agent's prose about logins, run together, was read as a lapsed login")
	}
}

func TestHarnessLoggedOutIsSeenAndProseIsNot(t *testing.T) {
	if !harnessProblemIn(attentionProbeTail(loggedOutScreen, harnessRows), "claude") {
		t.Fatal("a logged-out Claude Code screen was not recognised")
	}
	footerOnly := "────\n❯\n────\n  ⏵⏵ auto mode on (shift+tab to cycle)          Not logged in · Run /login"
	if !harnessProblemIn(footerOnly, "claude") {
		t.Fatal("the status line's \"Not logged in\" alone was not recognised")
	}
	if harnessProblemIn(attentionProbeTail(proseScreen, harnessRows), "claude") {
		t.Fatal("an agent's own words about logins were read as its login lapsing")
	}
}

// Seen twice in a row it counts; it must be gone for a while to clear.
func TestHarnessHealthDebounces(t *testing.T) {
	a := &Agent{}
	a.noteHarnessHealth(loggedOutScreen, "claude", false)
	if a.harnessDown() {
		t.Fatal("down after one glimpse")
	}
	a.noteHarnessHealth(loggedOutScreen, "claude", false)
	if !a.harnessDown() {
		t.Fatal("not down after two")
	}
	for i := 0; i < harnessUpAfter-1; i++ {
		a.noteHarnessHealth(proseScreen, "claude", false)
	}
	if !a.harnessDown() {
		t.Fatal("cleared too soon")
	}
	a.noteHarnessHealth(proseScreen, "claude", false)
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
	a.noteHarnessHealth(e.Render(), "claude", false)
	a.noteHarnessHealth(e.Render(), "claude", false)
	if !a.harnessDown() {
		t.Fatalf("not seen through the emulator; render was %q", e.Render())
	}
}

// Other CLIs start on a sign-in screen when they have no login. These are the
// bottoms of their real screens.
func TestHarnessSignInScreensAreLoggedOut(t *testing.T) {
	screens := map[string]string{
		// Claude Code 2.1.301 with no login, as a fresh machine shows it: its
		// login menu, then, a way picked, its sign-in link.
		"claude":             " Claude Code can be used with your Claude subscription or billed based on API usage through your\n Console account.\n\n Select login method:\n\n ❯ 1. Claude account with subscription · Pro, Max, Team, or Enterprise\n   2. Anthropic Console account · API usage billing\n   3. 3rd-party platform · Amazon Bedrock, Microsoft Foundry, Google Vertex AI",
		"claude, signing in": " Browser didn't open? Use the url below to sign in (c to copy)\n\n https://claude.com/cai/oauth/authorize?code=true&client_id=x&response_type=code\n\n\n Paste code here if prompted >",
		// Claude Code 2.1.296, 47 columns wide, just after its first way was
		// picked: the spinner before the link.
		"claude, opening the browser": "Welcome to Claude Code v2.1.296\n\n ✢ Opening browser to sign in…",
		// The link's heading wrapped beside "(c to copy)", drawn before the
		// rest of the step.
		"claude, signing in narrow": " Browser didn't open? Use the url     (c to \n below to sign in                     copy)\nhttps://claude.com/cai/oauth/authorize?code=tru\ne&client_id=9d1c250a-e61b-44d9-88ed-5944d1962f5",
		"agy": " Welcome to the Antigravity CLI. You are currently not signed in.\n Select login method:\n" +
			" > 1. Google OAuth\n   2. Use a Google Cloud project\n   ↑/↓ Navigate · enter Select",
		// Droid 1.6.0 with no login: the bottom of its screen (recorded in
		// Docker, testdata/harness-screens/droid.txt).
		"droid": "Please login with your Factory account to continue.\n\n> Login\n  Sign in to a Factory Custom Deployment\n  Exit",
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
		// pi 0.73.1 with no provider logged in, 47 columns wide (a cloud
		// machine's): its editor, then its status line.
		"pi": "───────────────────────────────────────────────\n" +
			"\n───────────────────────────────────────────────\n/work\n0.0%/0 (auto)                           unknown",
		// And a message sent to it, turned away.
		"pi, turned away": " Error: No API key found for the selected      \n model.                                        \n" +
			" Use /login to log into a provider via OAuth   \n or API key. See:                              \n" +
			"───────────────────────────────────────────────\n\n───────────────────────────────────────────────\n/work",
		"amp":   "No API key found. Starting login flow...\nWould you like to log in to Amp? [(y)es, (n)o]: ",
		"codex": "  Sign in with ChatGPT to use Codex as part of your paid plan\n  › 1. Sign in with ChatGPT\n    2. Provide your own API key",
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
		if prog, _, _ := strings.Cut(name, ","); !harnessProblemIn(harnessTail(s, harnessRows), prog) {
			t.Errorf("%s's sign-in screen was not seen as logged out", name)
		}
	}
	for _, ok := range []string{
		"  › Ask Codex to do anything   ? for shortcuts",
		" Type your message or @path/to/file",
		"I added a sign-in page; the user signs in with Google.",
	} {
		if harnessProblemIn(ok, "") {
			t.Errorf("read as logged out: %q", ok)
		}
	}
	// Claude Code's conversation naming its login words is not its menu.
	for _, ok := range []string{
		"● To use the API key instead, select login method 2 (Anthropic Console account · API usage billing).",
		"  The docs say: paste code here if prompted, then press Enter.",
	} {
		if harnessProblemIn(ok, "claude") {
			t.Errorf("Claude Code's words read as its login menu: %q", ok)
		}
	}
}

// A lead relaying what it saw on a report's screen quotes the notice word
// for word, mid-sentence. That is not the lead being logged out — read as one,
// it marked a working lead logged out and held its mail (seen in a test run,
// 2026-09-24; both screens below are as they were).
func TestAQuotedLoginNoticeIsNotTheAgents(t *testing.T) {
	lead := `● This confirms it: frontend2's Claude Code session shows "Login expired · Please run /login" — it
  needs an actual person to complete a login/auth flow, which isn't something I can do on its behalf.
  Needs your attention: frontend2 (n_8eaa75, on ubuntu-linux-22-04-02-desktop, ~/design) is logged out
  of Claude Code. It's sitting at a prompt asking for /login. Since that requires re-authenticating
  with a real account/credential, I can't push that through myself — could you log into that terminal?
  Work queued for frontend2 will stay held until then.
✻ Baked for 11s · done 7:08 AM
──────────────────────────────────────────────────────────────────────────────────────────────────────
❯
──────────────────────────────────────────────────────────────────────────────────────────────────────
  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents
                                                              ✔ Update installed · Restart to update`
	if harnessProblemIn(attentionProbeTail(lead, harnessRows), "claude") {
		t.Fatal("a lead quoting its report's login notice was read as the lead logged out")
	}
	// A quote that wraps so the notice begins a row is still a quote.
	wrapped := "● This confirms it: frontend2's session shows\n  \"Login expired · Please run /login\" — it needs a person.\n" +
		"────────────────\n❯\n────────────────\n  ⏵⏵ auto mode on (shift+tab to cycle)"
	if harnessProblemIn(attentionProbeTail(wrapped, harnessRows), "claude") {
		t.Fatal("a wrapped quote of the notice was read as the agent logged out")
	}

	report := `● Login expired · Please run /login
✻ Churned for 0s · done 7:08 AM
──────────────────────────────────────────────────────────────────────────────────────────────────────
❯
──────────────────────────────────────────────────────────────────────────────────────────────────────
  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents
                                                                          Not logged in · Run /login
                                                              ✔ Update installed · Restart to update`
	if !harnessProblemIn(attentionProbeTail(report, harnessRows), "claude") {
		t.Fatal("the report that really is logged out was not seen as one")
	}
	// Either place alone is enough: the conversation line, or the status line.
	if !harnessProblemIn("● Login expired · Please run /login\n❯", "claude") {
		t.Fatal("Claude Code's own notice on a line of its own was missed")
	}
	if !harnessProblemIn("❯\n  ⏵⏵ bypass permissions on (shift+tab to cycle)          Not logged in · Run /login", "claude") {
		t.Fatal("the status line's notice was missed")
	}
}

// Another program's login, printed by one of the agent's tools, is not the
// agent's. Such a line sits in the conversation above the prompt; a CLI's own
// notice sits on its bottom rows.
func TestAToolsLoginMessageIsNotTheAgents(t *testing.T) {
	working := `● Checking the deploy.

● Bash(netlify status)
  ⎿  Not logged in. Please log in to see site status.

● I'll ask you to log in to Netlify, then check again.

────────────────────────────────────────────────────
❯
────────────────────────────────────────────────────
  ⏵⏵ auto mode on (shift+tab to cycle) · ← for agents`
	if harnessProblemIn(attentionProbeTail(working, harnessRows), "claude") {
		t.Fatal("a tool's output about another service's login was read as the agent logged out")
	}
	// A CLI saying it of itself, on its own bottom rows, still counts.
	for _, own := range []string{
		"some output\nmore output\n> \n Waiting for auth… (esc to cancel)",
		"answer\n\nPlease log in to continue\n> ",
	} {
		if !harnessProblemIn(attentionProbeTail(own, harnessRows), "claude") {
			t.Fatalf("a CLI's own login notice was missed:\n%s", own)
		}
	}
}

// A Claude Code session's screen as it was when it was read three times as
// logged out (2026-10-14): at work, its tool having printed Codex's sign-in
// menu (a check of Codex's login). Codex's sign-in screen is Codex's; this is
// Claude Code, logged in. (Only its paths are changed.)
const anotherAgentsSignInScreen = `  ⎿  Shell cwd was reset to /home/dev/app

⏺ Bash(cd /home/dev/app/scripts/logins-probe && cat > run.sh
      <<'EOF'
      #!/bin/sh…)
  ⎿  SigninwithChatGPT
        sent:
     == codex exec, OPENAI_API_KEY
        sent:
     +18 lines (5m 34s · timeout 10m)
     (ctrl+b to run in background)

✻ Mustering… (38m 52s · ↓ 34.0k tokens)
  ⎿  Tip: Use /clear to start fresh when switching topics and free up context

──────────────────────────────────────────────────────────────────────────────────────────────────────────
❯ 
──────────────────────────────────────────────────────────────────────────────────────────────────────────
  ⏵⏵ bypass permissions on (shift+tab to cycle) · esc to interrupt · ← 2 agents`

func TestAnotherAgentsSignInScreenIsNotThisAgents(t *testing.T) {
	if harnessProblemIn(harnessTail(anotherAgentsSignInScreen, harnessRows), "claude") {
		t.Fatal("Codex's sign-in menu, printed by a tool, read as Claude Code logged out")
	}
	a := &Agent{}
	for i := 0; i < 5; i++ {
		a.noteHarnessHealth(anotherAgentsSignInScreen, "claude", false)
	}
	if a.harnessDown() {
		t.Fatal("the session was marked logged out")
	}
	// Codex's own sign-in screen, with Codex running, still is.
	codex := "  Sign in with ChatGPT to use Codex as part of your paid plan\n  › 1. Sign in with ChatGPT\n    2. Provide your own API key"
	if !harnessProblemIn(harnessTail(codex, harnessRows), "codex") {
		t.Fatal("Codex's own sign-in screen was missed")
	}
	// A program the probe does not name as an agent ("node": an agent run
	// from a path it does not know) is not known: every sign-in screen counts.
	gemini := "│   How would you like to authenticate for this project?   │\n│   ● 1. Sign in with Google   │"
	if !harnessProblemIn(harnessTail(gemini, harnessRows), "node") {
		t.Fatal("Gemini's sign-in screen under \"node\" was missed")
	}
	// And Claude Code's notice is Claude Code's alone: a Codex session's tool
	// printing it is not Codex logged out.
	if harnessProblemIn(harnessTail(loggedOutScreen, harnessRows), "codex") {
		t.Fatal("Claude Code's notice read as Codex logged out")
	}
}

// A turn its hook says has been going for a while, its screen still moving,
// is an agent that can work: what the screen says of logins does not make it
// logged out, and a session marked so is cleared at once.
func TestASessionSeenAtWorkIsNotLoggedOut(t *testing.T) {
	now := time.Now()
	for _, c := range []struct {
		hs      *session.HookState
		settled int64
		want    bool
		what    string
	}{
		{&session.HookState{State: "working", TS: now.Add(-38 * time.Minute)}, 300, false, "past the hook's TTL"},
		{&session.HookState{State: "working", TS: now.Add(-5 * time.Minute)}, 300, true, "a long turn, its spinner moving"},
		{&session.HookState{State: "working", TS: now.Add(-2 * time.Second)}, 300, false, "a turn just begun (a logged-out one ends at once)"},
		{&session.HookState{State: "working", TS: now.Add(-5 * time.Minute)}, 5000, false, "a still screen"},
		{&session.HookState{State: "done", TS: now.Add(-5 * time.Minute)}, 300, false, "a finished turn"},
		{nil, 300, false, "no hook"},
	} {
		if got := harnessAtWork(c.hs, now, c.settled); got != c.want {
			t.Errorf("%s: at work %v, want %v", c.what, got, c.want)
		}
	}
	a := &Agent{}
	a.noteHarnessHealth(loggedOutScreen, "claude", false)
	a.noteHarnessHealth(loggedOutScreen, "claude", false)
	if !a.harnessDown() {
		t.Fatal("not down")
	}
	a.noteHarnessHealth(loggedOutScreen, "claude", true)
	if a.harnessDown() {
		t.Fatal("seen at work, still logged out")
	}
}

// Claude Code logged out, its first way to sign in picked (a line typed into
// its login menu and its Enter): the spinner while it opens a browser is
// still logged out. Read as anything else for ~3 s, its logged-out state
// cleared, and it was taken for an agent at work, then done.
func TestClaudeSignInSpinnerKeepsItLoggedOut(t *testing.T) {
	menu := " Select login method:\n ❯ 1. Claude account with subscription · Pro,\n      Max, Team, or Enterprise\n   2. Anthropic Console account · API usage\n      billing"
	spinner := "Welcome to Claude Code v2.1.296\n\n ✳ Opening browser to sign in…"
	a := &Agent{}
	for i := 0; i < harnessDownAfter; i++ {
		a.noteHarnessHealth(menu, "claude", false)
	}
	if !a.harnessDown() {
		t.Fatal("the login menu was not read as logged out")
	}
	for i := 0; i < 2*harnessUpAfter; i++ {
		a.noteHarnessHealth(spinner, "claude", false)
	}
	if !a.harnessDown() {
		t.Fatal("while it opens a browser to sign in, it was no longer read as logged out")
	}
}

// pi with a model is not logged out: its status line names the model and its
// context, and its conversation may quote its own words about logins.
func TestPiWithAModelIsNotLoggedOut(t *testing.T) {
	for _, ok := range []string{
		"────────────────────────────────────────────────────────────────────────────────\n\n" +
			"────────────────────────────────────────────────────────────────────────────────\n/tmp\n" +
			"↑5 ↓3 0.0%/128k (auto)                                                    mock-1",
		" The user saw \"Error: No API key found for the selected model.\" in their app.\n/tmp\n0.4%/128k (auto)   mock-1",
		"0.0%/0 (auto)   unknown is what the status line said",
		// Its tools' output, and its words about a 401, are not its own.
		" The API answers Error: 401 when the token is stale.\n/tmp\n0.4%/128k (auto)   mock-1",
		" Error: 4013 items synced\n/tmp\n0.4%/128k (auto)   mock-1",
	} {
		if harnessProblemIn(harnessTail(ok, harnessRows), "pi") {
			t.Errorf("pi with a model read as logged out:\n%s", ok)
		}
	}
}

// A message typed into pi with no model: it works for over 2 s, then says it
// has no API key. That is a message turned away, not one taken; another
// agent working that long has taken it.
func TestPiTurningAMessageAwayLateIsTurnedAway(t *testing.T) {
	for _, tc := range []struct {
		prog string
		want bool
	}{{"pi", true}, {"claude", false}} {
		a := &Agent{attnFG: tc.prog, attnState: "working"}
		// Waited for before the next: it reads the clock a later test sets.
		said := make(chan struct{})
		go func() {
			defer close(said)
			time.Sleep(3 * time.Second)
			for i := 0; i < harnessDownAfter; i++ {
				a.noteHarnessHealth(" Error: No API key found for the selected model.\n/work\n0.0%/0 (auto)   unknown", tc.prog, false)
			}
		}()
		if got := a.harnessTurnedAway(); got != tc.want {
			t.Errorf("%s, working, then turned away after 3 s: %v, want %v", tc.prog, got, tc.want)
		}
		<-said
	}
}

// Cursor CLI turning away the API key it was given, as it prints it and
// exits: its words stay above the shell's prompt, the shell in front. That
// is logged out — and, unlike a missing login, a refused key: the key is
// what to change. Read as Cursor's, not the shell's, and not from a
// sentence that only names the words.
func TestACursorKeyItWasGivenAndRefusedIsSaidAsSo(t *testing.T) {
	refusedThenShell := "⚠ Warning: The provided API key is invalid.\n" +
		"The API key was loaded from the CURSOR_API_KEY environment variable.\n" +
		"Please check you have the right key, create a new one, or authenticate without it.\n" +
		"user@host:~/work$ "
	inFront := "⚠ Warning: The provided API key is invalid.\n" +
		"The API key was loaded from the CURSOR_API_KEY environment variable.\n" +
		"Please check you have the right key, create a new one, or authenticate without it."
	for _, c := range []struct {
		name, screen, prog string
	}{
		{"exited, the shell in front", refusedThenShell, "bash"},
		{"still in front", inFront, "cursor-agent"},
	} {
		tail := harnessTail(c.screen, harnessRows)
		if got := harnessKeyRefusedIn(tail); got != "cursor-agent" {
			t.Errorf("%s: refused key read as %q, want cursor-agent", c.name, got)
		}
		a := &Agent{}
		// Cursor CLI was in front, then (exited) the shell.
		a.noteForeground("cursor-agent", 0)
		a.noteForeground(c.prog, 0)
		a.noteHarnessHealth(c.screen, c.prog, false)
		// Exited on it, the shell in front: its last words, down at once.
		// Still in front: as any notice, on its second read.
		if exited := c.prog == "bash"; a.harnessDown() != exited {
			t.Errorf("%s: down after one read %v, want %v", c.name, a.harnessDown(), exited)
		}
		a.noteHarnessHealth(c.screen, c.prog, false)
		if !a.harnessDown() || a.harnessKeyRefused() != "cursor-agent" {
			t.Errorf("%s: down %v, refused %q; want down, by cursor-agent", c.name, a.harnessDown(), a.harnessKeyRefused())
		}
		// Its key changed and it runs again, at its prompt: cleared.
		for i := 0; i < harnessUpAfter; i++ {
			a.noteHarnessHealth(" → Plan, search, build anything\n  Auto", "cursor-agent", false)
		}
		if a.harnessDown() || a.harnessKeyRefused() != "" {
			t.Errorf("%s: still down %v / refused %q once it runs", c.name, a.harnessDown(), a.harnessKeyRefused())
		}
	}
	// No login at all: logged out, but no key was refused.
	a := &Agent{}
	noLogin := "  Press any key to log in..."
	a.noteHarnessHealth(noLogin, "cursor-agent", false)
	a.noteHarnessHealth(noLogin, "cursor-agent", false)
	if !a.harnessDown() || a.harnessKeyRefused() != "" {
		t.Errorf("no login: down %v, refused %q; want down, none refused", a.harnessDown(), a.harnessKeyRefused())
	}
	// Another agent's sentence naming the words is not Cursor refusing a key.
	prose := "I checked the logs: the provided API key is invalid, so the deploy step failed."
	if harnessProblemIn(prose, "claude") || harnessKeyRefusedIn(prose) != "" {
		t.Error("a sentence naming the words was read as Cursor refusing its key")
	}
}

// OpenCode 1.19.3 with an OpenAI key the service refuses, as captured: it
// stays in front, at its prompt, and answers each message with the
// service's words. Logged out, by a key it was given and that was refused —
// on its second read, as any notice; cleared once it works. Not from its
// own words naming the error, nor while another program is in front.
func TestAnOpenCodeKeyItWasGivenAndRefusedIsSaidAsSo(t *testing.T) {
	refused := "  ┃  Incorrect API key provided: sk-STAND****eady. You can find your API key\n" +
		"  ┃  at https://platform.openai.com/account/api-keys.\n  ┃\n     ▣  Build · GPT-5.6 Terra Pro\n  ┃\n  ┃\n  ┃\n" +
		"  ┃  Build · GPT-5.6 Terra Pro OpenAI\n  ╹" + strings.Repeat("▀", 74) + "\n" +
		"   /work                                           tab agents  ctrl+p commands"
	// Codex passes on the same words: whose they are is the program's in
	// front, asked by name; asked unnamed, the answer is the same each time.
	tail := harnessTail(refused, harnessRows)
	if !harnessKeyRefusedBy(tail, "opencode") {
		t.Fatal("not read as OpenCode refusing its key")
	}
	if first := harnessKeyRefusedIn(tail); first == "" {
		t.Fatal("not read as a key refused")
	} else {
		for i := 0; i < 50; i++ {
			if got := harnessKeyRefusedIn(tail); got != first {
				t.Fatalf("read as %q, then %q: not the same each time", first, got)
			}
		}
	}
	a := &Agent{}
	a.noteForeground("opencode", 0)
	a.noteHarnessHealth(refused, "opencode", false)
	a.noteHarnessHealth(refused, "opencode", false)
	if !a.harnessDown() || a.harnessKeyRefused() != "opencode" {
		t.Fatalf("down %v, refused %q; want down, by opencode", a.harnessDown(), a.harnessKeyRefused())
	}
	working := "  ┃  say hi\n     ▣  Build · GPT-5.6 Terra Pro\n  Hi! How can I help?\n  ╹" + strings.Repeat("▀", 74) + "\n   /work   tab agents  ctrl+p commands"
	for i := 0; i < harnessUpAfter; i++ {
		a.noteHarnessHealth(working, "opencode", false)
	}
	if a.harnessDown() || a.harnessKeyRefused() != "" {
		t.Errorf("still down %v / refused %q once it works", a.harnessDown(), a.harnessKeyRefused())
	}
	prose := "  ┃  The log says Incorrect API key provided, so the key is wrong.\n   /work   tab agents  ctrl+p commands"
	if harnessKeyRefusedIn(prose) != "" || harnessProblemIn(prose, "opencode") {
		t.Error("OpenCode's words about a refused key read as its own")
	}
	b := &Agent{}
	b.noteForeground("claude", 0)
	b.noteHarnessHealth(refused, "claude", false)
	b.noteHarnessHealth(refused, "claude", false)
	if b.harnessDown() {
		t.Error("OpenCode's words read as Claude Code's, with Claude Code in front")
	}
}

// "The provided API key is invalid" from anything but Cursor CLI — a curl to
// some API, a script — at a plain shell's prompt says nothing about an
// agent's login: the agent stays up, however often it is read. Only Cursor CLI
// in front, or in front just before the shell, makes those words its own.
func TestAKeyRefusedPhraseAtAPlainShellLeavesTheAgentUp(t *testing.T) {
	screen := "Warning: The provided API key is invalid.\n" +
		"user@host:~/work$ "
	for _, c := range []struct {
		name   string
		before []string // the programs in front, in turn, before the read
	}{
		{"a shell that never ran an agent", []string{"bash"}},
		{"curl just before the shell", []string{"bash", "curl", "bash"}},
		{"Cursor CLI, then another program, then the shell", []string{"cursor-agent", "bash", "curl", "bash"}},
	} {
		a := &Agent{}
		for _, p := range c.before {
			a.noteForeground(p, 0)
		}
		for i := 0; i < harnessDownAfter+2; i++ {
			a.noteHarnessHealth(screen, "bash", false)
		}
		if a.harnessDown() || a.harnessKeyRefused() != "" {
			t.Errorf("%s: down %v, refused %q; want the agent up", c.name, a.harnessDown(), a.harnessKeyRefused())
		}
	}
	// Control: the same screen just after Cursor CLI exited is its refusal.
	a := &Agent{}
	a.noteForeground("cursor-agent", 0)
	a.noteForeground("bash", 0)
	a.noteHarnessHealth(screen, "bash", false)
	if !a.harnessDown() || a.harnessKeyRefused() != "cursor-agent" {
		t.Errorf("just after Cursor CLI: down %v, refused %q; want down, by cursor-agent", a.harnessDown(), a.harnessKeyRefused())
	}
}

// Claude Code turning away a login it was given puts the command first:
// "● Please run /login · API Error: 401 OAuth access token is invalid."
// That is logged out too — and a sentence quoting it is not.
func TestClaudeRefusingItsLoginIsLoggedOut(t *testing.T) {
	screen := " ▐▛███▛█   Claude Code v2.1.296\n" +
		"❯ hello\n" +
		"● Please run /login · API Error: 401 OAuth access token is invalid.\n" +
		"✻ Worked for 2s · done 10:25 AM\n" +
		"────────────────────────────────────────\n" +
		"❯  \n" +
		"────────────────────────────────────────\n" +
		"  ⏸ manual mode on · ? for shortcuts"
	if !harnessProblemIn(harnessTail(screen, harnessRows), "claude") {
		t.Error("Claude Code's \"Please run /login · API Error: 401\" was not read as logged out")
	}
	for _, prose := range []string{
		"● The log said: please run /login · API Error: 401, so I retried with a new token.",
		"● I fixed the 401: the app now asks users to run /login · API Error handling is done.",
	} {
		if harnessProblemIn(prose, "claude") {
			t.Errorf("a sentence naming it was read as logged out: %q", prose)
		}
	}
}

// pi with a login its provider refuses answers each message with the
// service's 401 on a row of its own, at its prompt. With pi in front that is
// logged out, by a refused login; "Error: 401" at a plain shell's prompt (a
// curl, a script) is not, nor pi's words about a 401 in the middle of a row,
// nor a number that starts with 401.
func TestPiRefusedLoginIsLoggedOutOnlyAsPisOwn(t *testing.T) {
	for name, s := range map[string]string{
		"anthropic": " Error: 401\n" +
			" {\"type\":\"error\",\"error\":{\"type\":\"authentication_error\",\"message\":\"OAuth access\n" +
			" token is invalid.\"},\"request_id\":\"req_011Cg7iwkH9FtQmsb66Xt71T\"}\n" +
			strings.Repeat("─", 80) + "\n\n" + strings.Repeat("─", 80) + "\n/work\n" +
			"0.0%/1.0M (auto)                            (anthropic) claude-opus-4-7 • medium",
		"openai": " Error: 401 Incorrect API key provided: sk-STAND*****eady. You can find your API\n" +
			"/work\n0.0%/400k (auto)                                  (openai) gpt-5 • medium",
	} {
		a := &Agent{}
		a.noteForeground("pi", 0)
		for i := 0; i < harnessDownAfter; i++ {
			a.noteHarnessHealth(s, "pi", false)
		}
		if !a.harnessDown() || a.harnessKeyRefused() != "pi" {
			t.Errorf("pi refused by %s: down %v, refused %q; want down, by pi", name, a.harnessDown(), a.harnessKeyRefused())
		}
	}
	for _, c := range []struct{ prog, screen string }{
		{"bash", "Error: 401 Unauthorized\nuser@host:~$ "},
		{"bash", "error: 401\nuser@host:~$ "},
		{"pi", " The API answers Error: 401 when the token is stale.\n/tmp\n0.4%/128k (auto)   mock-1"},
		{"pi", " Error: 4013 items synced\n/tmp\n0.4%/128k (auto)   mock-1"},
	} {
		a := &Agent{}
		a.noteForeground(c.prog, 0)
		for i := 0; i < harnessDownAfter+2; i++ {
			a.noteHarnessHealth(c.screen, c.prog, false)
		}
		if a.harnessDown() {
			t.Errorf("%s in front, %q: read as logged out", c.prog, c.screen)
		}
	}
}

// Claude Code refusing the login it was given is said as that — a login it
// has, refused — not as one it never had: only while Claude Code is in front.
// "Login expired" (no login at all) is logged out with nothing refused, and
// the same row at a plain shell that never ran it is no refusal of Claude's.
func TestClaudeRefusingItsLoginIsSaidAsRefused(t *testing.T) {
	row := "● Please run /login · API Error: 401 OAuth access token is invalid."
	a := &Agent{}
	a.noteForeground("claude", 0)
	screen := "❯ hello\n" + row + "\n❯  "
	a.noteHarnessHealth(screen, "claude", false)
	a.noteHarnessHealth(screen, "claude", false)
	if !a.harnessDown() || a.harnessKeyRefused() != "claude" {
		t.Errorf("in front: down %v, refused %q; want down, by claude", a.harnessDown(), a.harnessKeyRefused())
	}
	// No login at all: logged out, nothing refused.
	b := &Agent{}
	b.noteForeground("claude", 0)
	expired := "❯ hello\n● Login expired · Please run /login\n❯  "
	b.noteHarnessHealth(expired, "claude", false)
	b.noteHarnessHealth(expired, "claude", false)
	if !b.harnessDown() || b.harnessKeyRefused() != "" {
		t.Errorf("no login: down %v, refused %q; want down, none refused", b.harnessDown(), b.harnessKeyRefused())
	}
	// At a plain shell that never ran Claude Code: not its refusal (whether
	// the row reads as logged out there is harnessLoginRefused's, unchanged).
	c := &Agent{}
	c.noteForeground("bash", 0)
	atShell := row + "\nuser@host:~/work$ "
	for i := 0; i < harnessDownAfter+2; i++ {
		c.noteHarnessHealth(atShell, "bash", false)
	}
	if r := c.harnessKeyRefused(); r != "" {
		t.Errorf("at a plain shell: refused by %q; want none", r)
	}
	// A sentence quoting the words is not a refusal.
	if r := harnessKeyRefusedIn("The log said Please run /login · API Error: 401, so I retried."); r != "" {
		t.Errorf("a sentence quoting the words read as %q refusing its login", r)
	}
}

// Codex with a key the service refuses says so on a row of its own ("■
// unexpected status 401 Unauthorized: Incorrect API key provided: …"). With
// Codex in front, or just before the shell, that is logged out, by a refused
// key. The same words at a plain shell (a curl, a script, OpenAI's SDK), in a
// sentence, or with OpenCode in front are not Codex's; and Codex's words are
// Codex's even where OpenCode's (which pass on the same service's) would read
// alike.
func TestACodexKeyRefusedIsCodexsOnlyAsItsOwn(t *testing.T) {
	refused := "■ unexpected status 401 Unauthorized: Incorrect API key provided:\nkey at https://platform.openai.com/account/api-keys.\n\n› Ask Codex to do anything"
	bare := "Incorrect API key provided: sk-STAND****eady.\n› Ask Codex to do anything"
	for _, c := range []struct {
		name   string
		before []string
		prog   string
		screen string
	}{
		{"in front", []string{"codex"}, "codex", refused},
		{"in front, the words alone", []string{"codex"}, "codex", bare},
		{"just exited", []string{"codex", "bash"}, "bash", refused + "\nuser@host:~$ "},
	} {
		a := &Agent{}
		for _, p := range c.before {
			a.noteForeground(p, 0)
		}
		for i := 0; i < harnessDownAfter; i++ {
			a.noteHarnessHealth(c.screen, c.prog, false)
		}
		if !a.harnessDown() || a.harnessKeyRefused() != "codex" {
			t.Errorf("%s: down %v, refused %q; want down, by codex", c.name, a.harnessDown(), a.harnessKeyRefused())
		}
	}
	for _, c := range []struct {
		before []string
		prog   string
		screen string
	}{
		{[]string{"bash"}, "bash", "Incorrect API key provided: sk-abc.\nuser@host:~$ "},
		{[]string{"bash"}, "bash", "openai.AuthenticationError: Error code: 401 - Incorrect API key provided\nuser@host:~$ "},
		{[]string{"bash", "curl", "bash"}, "bash", "unexpected status 401 Unauthorized: Incorrect API key provided:\nuser@host:~$ "},
		{[]string{"codex", "bash", "curl", "bash"}, "bash", "Incorrect API key provided: sk-abc.\nuser@host:~$ "},
		{[]string{"codex"}, "codex", "• The log says Incorrect API key provided, so I swapped the key.\n› Ask Codex to do anything"},
		{[]string{"claude"}, "claude", "Incorrect API key provided: sk-abc.\n❯  "},
	} {
		a := &Agent{}
		for _, p := range c.before {
			a.noteForeground(p, 0)
		}
		for i := 0; i < harnessDownAfter+2; i++ {
			a.noteHarnessHealth(c.screen, c.prog, false)
		}
		if a.harnessDown() || a.harnessKeyRefused() != "" {
			t.Errorf("after %v, %q: down %v, refused %q; want up", c.before, c.screen, a.harnessDown(), a.harnessKeyRefused())
		}
	}
	// OpenCode's own refusal, with OpenCode in front, stays OpenCode's.
	o := &Agent{}
	o.noteForeground("opencode", 0)
	oc := "  ┃  Incorrect API key provided: sk-STAND****eady.\n   /work   tab agents  ctrl+p commands"
	for i := 0; i < harnessDownAfter; i++ {
		o.noteHarnessHealth(oc, "opencode", false)
	}
	if !o.harnessDown() || o.harnessKeyRefused() != "opencode" {
		t.Errorf("OpenCode in front: down %v, refused %q; want down, by opencode", o.harnessDown(), o.harnessKeyRefused())
	}
}
