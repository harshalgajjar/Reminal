// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"strings"
	"testing"
)

// loginRowCase is a row a CLI draws when it cannot run for want of a login,
// as it draws it: prog's own, a sign-in screen's or a notice's (or, refused,
// a key it was given and turned away).
type loginRowCase struct {
	prog, row string
	refused   bool // a key it was given and refused (harnessKeyRefusals)
	bottom    bool // read only on the last few rows: its status line
	notice    bool // Claude Code's notice shape (harnessNoticeRow), read on its own row only
}

var loginRowCases = []loginRowCase{
	{prog: "agy", row: " Welcome to the Antigravity CLI. You are currently not signed in."},
	{prog: "agy", row: " Sign in required", bottom: true},
	// Antigravity 1.5.0: a sign-in that lapsed mid-session, and its sign-in
	// steps.
	{prog: "agy", row: " Authentication expired. Please log back in."},
	{prog: "agy", row: " Open this URL in your local browser to sign in:"},
	{prog: "agy", row: " Open this link in a browser to log in with Google:"},
	{prog: "agy", row: " Then paste the authorization code, or the full URL of the page you land on, and press Enter:", bottom: true},

	{prog: "codex", row: "  Sign in with ChatGPT to use Codex as part of your paid plan"},
	{prog: "codex", row: "■ Your access token could not be refreshed because your refresh token has expired. Please log out and sign in again."},
	{prog: "codex", row: "■ Your access token could not be refreshed because your refresh token was revoked. Please log out and sign in again."},
	{prog: "codex", row: "■ Your access token could not be refreshed because you have since logged out or signed in to another account. Please sign in again."},
	{prog: "codex", row: "■ Your session has expired. Please reauthenticate."},
	// Codex 0.166.0: a token it could not
	// refresh, said without why, its sign-in that could not be recovered,
	// and a ChatGPT login with no account in it.
	{prog: "codex", row: "■ Your access token could not be refreshed. Please log out and sign in again."},
	{prog: "codex", row: "■ Your authentication session could not be refreshed automatically. Please log out and sign in again."},
	{prog: "codex", row: "■ ChatGPT account ID not available, please re-run `codex login`"},
	{prog: "codex", row: "■ ChatGPT account ID not available, please re-run codex login"},
	// Codex 0.167.0: the device-code sign-in's three rows, and an enterprise
	// identity provider's session it cannot keep.
	{prog: "codex", row: "  Preparing device code login"},
	{prog: "codex", row: "■ enterprise IdP session has no refresh token; sign in again"},
	{prog: "codex", row: "■ enterprise IdP credentials were removed; sign in again"},
	{prog: "codex", row: "■ enterprise IdP identity changed; sign in again and reconnect"},
	// Qwen Code 0.26.9: a message turned away for want of a login, its /auth
	// flow, its signed-out state, no provider, signing in through the
	// browser, and a plan without Qwen Code (the plan's name is a hole:
	// empty, or a name).
	{prog: "qwen", row: "  Not signed in. Run /auth to sign in."},
	{prog: "qwen", row: "  Your Qwen session has expired. Run /auth to sign in again."},
	{prog: "qwen", row: "  Your login credentials are no longer valid. Run /auth to sign in again."},
	{prog: "qwen", row: "  Signed out of Qwen. Use /auth to sign in again."},
	{prog: "qwen", row: "  Signed out of Qwen. Use /auth to sign in with another account."},
	{prog: "qwen", row: "  Use /auth to sign in or configure a model first."},
	{prog: "qwen", row: "  Opening chat.qwen.ai in your browser… Sign in there to connect Qwen Code."},
	{prog: "qwen", row: "  You're signed in with a Free account, which doesn't include Qwen Code. Choose Plus or Pro in your browser; this terminal continues automatically after you subscribe."},
	{prog: "qwen", row: "  You're signed in with a  account, which doesn't include Qwen Code. Choose Plus or Pro in your browser; this terminal continues automatically after you subscribe."},

	{prog: "amp", row: "Would you like to log in to Amp? [(y)es, (n)o]: "},
	{prog: "amp", row: "No API key found. Starting login flow..."},
	{prog: "amp", row: "No API key found. Run `amp login` first."},
	{prog: "amp", row: "Authentication failed. Run `amp login` and try again."},
	{prog: "amp", row: "API key required. Please run `amp login` first."},
	{prog: "amp", row: "API key required for headless mode. Please run `amp login` first."},
	{prog: "amp", row: "Session expired and could not be refreshed. Run 'amp login' to authenticate."},
	{prog: "amp", row: "Your session has expired. You need to log in again.", bottom: true},
	{prog: "amp", row: "Access token expired. Run 'amp login' to refresh."},
	{prog: "amp", row: "Invalid or missing API key. Run 'amp login' to authenticate."},

	{prog: "claude", row: " ✢ Opening browser to sign in…"},
	{prog: "claude", row: " Browser didn't open? Use the url below to sign in (c to copy)"},
	{prog: "claude", row: "● OAuth token revoked · Please run /login", notice: true},
	{prog: "claude", row: "● Login expired · Run /login to sign in again, or re-authenticate your Anthropic profile"},
	{prog: "claude", row: "● Not signed in to Claude. Run /login first."},
	{prog: "claude", row: "  ⎿  Failed to authenticate: OAuth token revoked. Please log in again or contact your administrator."},
	{prog: "claude", row: "  ⎿  Failed to authenticate: OAuth session expired and could not be refreshed"},
	{prog: "claude", row: "● Invalid API key · Fix external API key", refused: true},
	// Claude Code 2.1.317–318: a login that
	// reached the end of its time, the Cloud gateway's own, and the sign-in
	// step's new words.
	{prog: "claude", row: "● Login reached its time limit · Please run /login", notice: true},
	{prog: "claude", row: "  ⎿  Failed to authenticate: this login reached its time limit"},
	{prog: "claude", row: "  ⎿  Failed to authenticate: this login reached its time limit · Sign in again to continue"},
	{prog: "claude", row: "  ⎿  Failed to authenticate: this login reached its time limit and cannot be renewed. Run `claude auth login` to sign in again, or use `claude setup-token` and set CLAUDE_CODE_OAUTH_TOKEN for unattended jobs"},
	{prog: "claude", row: "  ⎿  Not signed in to the Cloud gateway — run /login."},
	{prog: "claude", row: "  ⎿  Cloud gateway token expired — refresh ANTHROPIC_AUTH_TOKEN and restart."},
	{prog: "claude", row: " ✢ Opening browser to sign in with your Claude account…"},

	{prog: "gemini", row: "✕ Gemini API key is missing or not configured."},

	{prog: "qwen", row: "  │   Connect with an existing ModelStudio API key                           │"},
	{prog: "qwen", row: "✕ No API key found: set OPENAI_API_KEY or security.auth.apiKey."},
	{prog: "qwen", row: "✕ Qwen OAuth credentials expired. Please use /auth to re-authenticate with qwen-oauth."},
	{prog: "qwen", row: "✕ Refresh token expired or invalid. Please use '/auth' to re-authenticate."},
	{prog: "qwen", row: `✕ [API Error: Invalid API key. Coding Plan API keys start with "sk-sp-". Please check. (Status: 401)]`, refused: true},
	{prog: "qwen", row: `✕ Invalid API key. Pay-as-you-go keys start with "sk-" or "sk-ws-". Token Plan keys ("sk-sp-") need the Token Plan entry instead.`, refused: true},
	{prog: "qwen", row: "✕ Model API authentication failed (HTTP 401). Check the configured provider credentials.", refused: true},

	{prog: "opencode", row: "  ┃  Anthropic API key is missing. Pass it using the 'apiKey' parameter or the ANTHROPIC_API_KEY environment variable."},
	{prog: "opencode", row: "  ┃  Unauthorized: request was blocked by a gateway or proxy. Your authentication token may be missing or expired — try running `opencode auth login <your provider URL>` to re-authenticate."},

	{prog: "goose", row: "Authentication failed — check your API key. Run `goose configure` to update it."},
	{prog: "goose", row: "Muse Code API key is missing. Sign in again."},
	{prog: "goose", row: "Meta session expired. Sign in to Muse Code again."},

	{prog: "crush", row: `Please sign in to Hyper again. You can also run "crush login hyper --force".`},
	{prog: "crush", row: "You are not logged in to any platform.", bottom: true},

	{prog: "cursor-agent", row: "                                               Press any key to log in..."},
	{prog: "cursor-agent", row: " If your browser didn't open, click this link to log in:"},
	{prog: "cursor-agent", row: " Press q to show a QR code to log in from another device"},
	{prog: "cursor-agent", row: "Authentication required. Please run 'cursor-agent login' to authenticate."},
	{prog: "cursor-agent", row: "Error: Authentication required. Please run 'agent login' first, or set CURSOR_API_KEY environment variable."},
	{prog: "cursor-agent", row: "Authentication failed: your Cursor credentials or API key are invalid or expired. If you set CURSOR_API_KEY, check that it is correct, or run `cursor-agent login` to re-authenticate."},
	{prog: "cursor-agent", row: "Your stored authentication is invalid. Please log in again.", bottom: true},
	{prog: "cursor-agent", row: "⚠ Warning: The provided API key is invalid.", refused: true},

	{prog: "pi", row: " Error: No API key found for the selected model."},
	{prog: "pi", row: " Error: No API key found for anthropic."},
	{prog: "pi", row: ` Error: Authentication failed for "anthropic". Credentials may have expired or network is unavailable. Run '/login anthropic' to re-authenticate.`},
	{prog: "pi", row: " Error: Your session has expired. Please reauthenticate."},

	// Sign-in screens' rows (as captured in testdata/harness-screens, or as
	// each CLI's release writes them).
	{prog: "gemini", row: "│   How would you like to authenticate for this project?                       │"},
	{prog: "gemini", row: "│   No authentication method selected.                                         │"},
	{prog: "gemini", row: "Please visit the following URL to authorize the application:"},
	{prog: "gemini", row: "Enter the authorization code:", bottom: true},
	{prog: "gemini", row: "│ Enter Gemini API Key                                                         │"},
	{prog: "qwen", row: "  │ Alibaba ModelStudio · Access Method                                      │"},
	{prog: "qwen", row: "  │ Connect a Provider                                                       │"},
	{prog: "qwen", row: "  │   Manually connect a local server, proxy, or unsupported provider        │"},
	{prog: "qwen", row: "  Finish provider setup"},
	{prog: "qwen", row: "No auth type is selected. Please configure an auth type (e.g. via settings or `--auth-type`) before running in non-interactive mode."},
	{prog: "claude", row: " Select login method:"},
	{prog: "claude", row: " ❯ 1. Claude account with subscription · Pro, Max, Team, or Enterprise"},
	{prog: "claude", row: "   2. Anthropic Console account · API usage billing"},
	{prog: "claude", row: " Paste code here if prompted >"},
	{prog: "goose", row: "error: No provider configured. Run 'goose configure' first."},
	{prog: "goose", row: "Provider is not configured. Run `goose configure` to set it up."},
	{prog: "crush", row: "  To start, let's choose a provider and model."},
	{prog: "crush", row: "  Let's authenticate with Hyper"},
	{prog: "crush", row: "Error: no providers configured - please run 'crush' to set up a provider interactively"},
	{prog: "codex", row: "  Finish signing in via your browser"},
	{prog: "pi", row: "0.0%/0 (auto)                           unknown"},

	// What any CLI may say of its login on its own status rows.
	{prog: "gemini", row: " Authentication required.", bottom: true},
	{prog: "claude", row: "  Authentication required · Sign in again to continue", bottom: true},
	{prog: "claude", row: " Please log in with a permitted organization: claude auth login", bottom: true},
	{prog: "claude", row: "  login required — run /login", bottom: true},
	{prog: "claude", row: " Login expired", bottom: true},
	{prog: "claude", row: " Not logged in", bottom: true},
	{prog: "claude", row: " Waiting for authorization… (^C to cancel)", bottom: true},
	{prog: "qwen", row: " Waiting for auth... (Press ESC or CTRL+C to cancel)", bottom: true},
	{prog: "gemini", row: " Waiting for authentication...", bottom: true},
	{prog: "goose", row: "Waiting for authentication callback...", bottom: true},
	{prog: "gemini", row: "Please set an Auth method in your ~/.gemini/settings.json or specify one of the following environment variables before running: GEMINI_API_KEY, GOOGLE_GENAI_USE_VERTEXAI, GOOGLE_GENAI_USE_GCA", bottom: true},
}

// loginRowFrame is what sits under a row in these screens: an input box,
// three rows, so a row above it is not among the last few (the CLI's own
// status), and the plain words read there (harnessLoginLines) stay out of it.
const loginRowFrame = "\n────────────────────────\n❯\n────────────────────────"

// loggedOutBy reports whether tail reads, with prog here, as prog unable to
// run for want of a login, or turning away its key.
func loggedOutBy(tail, prog string) bool {
	return harnessProblemIn(tail, prog) || harnessKeyRefusedBy(tail, prog)
}

// screenWith is a screen with rows where the case reads them: above the
// input box, or (bottom) on the last rows, below it.
func (c loginRowCase) screenWith(rows string) string {
	if c.bottom {
		return "● Done with the build." + loginRowFrame + "\n" + rows
	}
	return "● Done with the build.\n" + rows + loginRowFrame
}

// words is the row's text without the marks the CLI draws before it.
func (c loginRowCase) words() string {
	return strings.TrimLeftFunc(c.row, func(r rune) bool { return strings.ContainsRune(" ■●⎿✕✢›⚠│┃", r) })
}

// halves splits the row's words where a terminal would wrap them: at the
// space nearest their middle that does not end a sentence (a row that ends
// one is never read as wrapping onto the next).
func (c loginRowCase) halves() (string, string) {
	w := strings.TrimRight(c.words(), " │")
	best := -1
	for i := 1; i < len(w)-1; i++ {
		if w[i] != ' ' || strings.ContainsAny(w[i-1:i], ".!?") {
			continue
		}
		if best < 0 || abs(i-len(w)/2) < abs(best-len(w)/2) {
			best = i
		}
	}
	return w[:best], w[best+1:]
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// Each CLI's own row is read as it is drawn, and wrapped at the terminal's
// width; with that CLI here, and with no program known.
func TestEachCLIsLoginRowIsRead(t *testing.T) {
	for _, c := range loginRowCases {
		tail := harnessTail(c.screenWith(c.row), harnessRows)
		if c.refused {
			if !harnessKeyRefusedBy(tail, c.prog) {
				t.Errorf("%s: its refused key was not read: %q", c.prog, c.row)
			}
			continue
		}
		for _, prog := range []string{c.prog, ""} {
			if !harnessProblemIn(tail, prog) {
				t.Errorf("%s (as %q): not read as logged out: %q", c.prog, prog, c.row)
			}
		}
		if c.notice {
			continue
		}
		first, second := c.halves()
		lead := c.row[:len(c.row)-len(c.words())]
		wrapped := harnessTail(c.screenWith(lead+first+"\n  "+second), harnessRows)
		if !harnessProblemIn(wrapped, c.prog) {
			t.Errorf("%s: wrapped onto two rows, not read as logged out:\n%s\n  %s", c.prog, lead+first, second)
		}
	}
}

// The same words in an agent's own text are not the CLI: a sentence that
// starts with them and carries on, a quote of them in an answer or on a row
// of its own, and a wrapped row of prose that starts in the middle of them
// or carries on after them.
func TestLoginWordsInAnAgentsOwnTextAreNotALogin(t *testing.T) {
	for _, c := range loginRowCases {
		w := c.words()
		first, second := c.halves()
		lead := c.row[:len(c.row)-len(w)]
		for _, s := range []struct{ what, rows string }{
			{"carries on", strings.TrimRight(c.row, " │") + " — so I changed the setting and it works now."},
			{"quoted in an answer", "● The CLI printed \"" + strings.TrimRight(w, " │") + "\" before the fix."},
			{"quoted on its own row", "  \"" + strings.TrimRight(w, " │") + "\""},
			{"wrapped, from its middle", "● Earlier the tool printed " + first + "\n  " + second + " and then went on."},
			{"wrapped, carrying on", lead + first + "\n  " + second + " and then went on."},
		} {
			// A row whose first half is itself a whole row a CLI draws
			// ("Waiting for authentication" of "… callback...") reads as
			// that row, wrapped or not: nothing on a screen tells them apart.
			if s.what == "wrapped, carrying on" && loggedOutBy(harnessTail(c.screenWith(lead+first), harnessRows), c.prog) {
				t.Logf("%s: %q alone is a whole row: its wrap is not told apart", c.prog, lead+first)
				continue
			}
			tail := harnessTail(c.screenWith(s.rows), harnessRows)
			for _, prog := range []string{c.prog, ""} {
				if loggedOutBy(tail, prog) {
					t.Errorf("%s (as %q), %s: read as logged out:\n%s", c.prog, prog, s.what, s.rows)
				}
			}
		}
	}
	// What the wording a CLI uses elsewhere reads like (Codex's own
	// messages that start with its menu's words, and go on).
	for _, s := range []struct{ prog, row string }{
		{"codex", "■ Sign in with ChatGPT auth; API key auth cannot load remote plugin catalogs"},
		{"codex", "■ Couldn't load local sign-in. Sign in with ChatGPT and retry."},
		{"codex", "  Sign in with ChatGPT"},
		{"pi", " No API key found for the deploy step, so I set it in .env."},
		// The service's words for a key Gemini CLI cannot use, in an
		// answer: not in the JSON of its own error.
		{"gemini", "● The service said: API key not valid. Please pass a valid API key."},
		{"gemini", "  API key not valid. Please pass a valid API key."},
	} {
		if loggedOutBy(harnessTail("● Done.\n"+s.row+loginRowFrame, harnessRows), s.prog) {
			t.Errorf("%s: read as logged out: %q", s.prog, s.row)
		}
	}
}

// On the CLI's own status rows, a server's login (an MCP server's, a
// policy's) is not the agent's: a status line only with what it ends on.
func TestAnotherLoginOnTheStatusRowsIsNotTheAgents(t *testing.T) {
	for _, row := range []string{
		" Authentication required for MCP Server: github",
		" Authentication required for A2A agent: planner",
		" Authentication required for policy limits",
		" Please sign in to the dashboard first",
		" Login required for the staging site",
		" Waiting for authorization from the reviewer",
	} {
		for _, prog := range []string{"claude", "gemini", ""} {
			if loggedOutBy(harnessTail("● Done.\n"+row+"\n❯", harnessRows), prog) {
				t.Errorf("as %q: read as logged out: %q", prog, row)
			}
		}
	}
}

// A row read only on the CLI's own status rows is not one higher up, in the
// conversation.
func TestABottomRowHigherUpIsNotALogin(t *testing.T) {
	for _, c := range loginRowCases {
		if !c.bottom {
			continue
		}
		if loggedOutBy(harnessTail("● Done.\n"+c.row+loginRowFrame, harnessRows), c.prog) {
			t.Errorf("%s: above its input box, read as logged out: %q", c.prog, c.row)
		}
	}
}

// Codex's sign-in menu is its items together: its first, then the next
// below it (its own description between). One numbered item alone is an
// agent's list ("1. Sign in with ChatGPT" in its answer), not the menu.
func TestCodexsLoginMenuIsItsItemsTogether(t *testing.T) {
	menu := "> 1. Sign in with ChatGPT\n     Usage included with Plus, Pro, Business, and Enterprise plans\n" +
		"  2. Sign in with Device Code\n     Sign in from another device with a one-time code\n  3. Provide your own API key"
	if !harnessProblemIn(menu+loginRowFrame, "codex") {
		t.Error("its menu, items together, was not read as logged out")
	}
	for _, answer := range []string{
		"● To get going:\n\n  1. Sign in with ChatGPT\n\n  Then run the tests again.",
		"● Two ways to set it up:\n  1. Provide your own API key\n  2. Ask an admin for one",
		"  3. Provide your own API key",
	} {
		if harnessProblemIn(answer+loginRowFrame, "codex") {
			t.Errorf("an answer's numbered item read as Codex's menu:\n%s", answer)
		}
	}
}

// Codex 0.167.0's device-code steps are its only together: the first, then
// the second within the next few rows (the link between them). An agent's
// own numbered list that starts with the first step's words is not it.
func TestCodexsDeviceCodeStepsAreItsTogether(t *testing.T) {
	steps := "  Preparing device code login\n\n  1. Open this link in your browser and sign in\n     https://auth.openai.com/device?user_code=ABCD-EFGH\n\n  2. Enter this one-time code after you are signed in\n     ABCD-EFGH"
	if !harnessProblemIn(steps+loginRowFrame, "codex") {
		t.Error("its device-code steps, together, were not read as logged out")
	}
	for _, answer := range []string{
		"● To get the token:\n  1. Open this link in your browser and sign in\n  2. Copy the key into .env\n  3. Run the tests again.",
		"  2. Enter this one-time code after you are signed in",
	} {
		if harnessProblemIn(answer+loginRowFrame, "codex") {
			t.Errorf("an answer's numbered step read as Codex's device-code login:\n%s", answer)
		}
	}
}

// Antigravity's "You are currently not signed in." is its screen's only after
// its heading's words: alone, it is a sentence anyone may write.
func TestAntigravitysLoginHeadingIsItsOnlyWhole(t *testing.T) {
	if harnessProblemIn("● Done.\n You are currently not signed in."+loginRowFrame, "agy") {
		t.Error("the words alone read as its sign-in screen")
	}
}

// What an agent writes that reads like a CLI's login lines and is not: a
// code block or a table quoting Claude Code's, two sentences on two rows,
// rows of two paragraphs, a CLI's plain words with neither its mark nor a
// place on its status rows, and a word in pi's that is no provider of pi's.
func TestAnAgentsOwnTextIsNotALoginAfterAll(t *testing.T) {
	for _, c := range []struct{ name, prog, screen string }{
		{"a code block quoting Claude Code", "claude", "● Here is what it prints:\n\n      Not signed in to Claude. Run /login first.\n\n  That is the old build."},
		{"two sentences on two rows", "claude", "● It said:\n  Not signed in to Claude.\n  Run /login first.\n  So I signed in."},
		{"a table's cell", "claude", "  │ Not signed in to Claude. Run /login first. │ old build │"},
		{"a box's one cell", "claude", "  │ Not signed in to Claude. Run /login first. │"},
		{"Claude's words on a row of their own, no mark", "claude", "  Failed to authenticate: OAuth session expired and could not be refreshed"},
		{"a row and the next paragraph's", "codex", "■ Your access token could not be refreshed because your refresh token\n\nhas expired. Please log out and sign in again."},
		{"Amp's two sentences on two rows", "amp", "● Authentication failed.\n  Run amp login and try again."},
		{"Amp's plain words in an answer", "amp", "  Your session has expired. You need to log in again."},
		{"Cursor's plain words in an answer", "cursor-agent", "  Your stored authentication is invalid. Please log in again."},
		{"Crush's plain words in an answer", "crush", "  You are not logged in to any platform"},
		{"Qwen's words with no mark of its own", "qwen", "  Refresh token expired or invalid. Please use /auth to re-authenticate."},
		{"Codex's words with no mark of its own", "codex", "  Your session has expired. Please reauthenticate."},
		{"pi's words with no Error: of its own", "pi", "  Your session has expired. Please reauthenticate."},
		{"pi naming no provider of its own", "pi", " Error: No API key found for staging-server"},
	} {
		for _, prog := range []string{c.prog, ""} {
			if loggedOutBy(c.screen+loginRowFrame, prog) {
				t.Errorf("%s (as %q): read as logged out:\n%s", c.name, prog, c.screen)
			}
		}
	}
	// On the status rows: words that open what follows (a list, a form).
	for _, c := range []struct{ prog, screen string }{
		{"agy", "● Done." + loginRowFrame + "\n Sign in required:"},
		{"claude", "● Done." + loginRowFrame + "\n Login expired:"},
	} {
		if loggedOutBy(c.screen, c.prog) {
			t.Errorf("%s: read as logged out:\n%s", c.prog, c.screen)
		}
	}
	// Through what the probe reads: a paragraph's end kept, so a row is not
	// read with the next paragraph's.
	a := &Agent{}
	screen := "■ Your access token could not be refreshed because your refresh token\n\nhas expired. Please log out and sign in again." + loginRowFrame
	a.noteHarnessHealth(screen, "codex", false)
	a.noteHarnessHealth(screen, "codex", false)
	if a.harnessDown() {
		t.Error("two paragraphs, read through the probe's tail, were read as one notice")
	}
}

// A server's login on the status rows does not hide the agent's own: each
// row is read for itself.
func TestAServersLoginDoesNotHideTheAgentsOwn(t *testing.T) {
	screen := "● Done." + loginRowFrame + "\n Authentication required for MCP Server: github\n Authentication required."
	for _, prog := range []string{"gemini", ""} {
		if !harnessProblemIn(screen, prog) {
			t.Errorf("as %q: the agent's own \"Authentication required.\" was missed", prog)
		}
	}
}

// A screen's worth of rows, many times over: reading it costs no more than
// the rows it has, and a row too long to be a notice is not read as one.
func BenchmarkHarnessLoginRowsOnAHugeTail(b *testing.B) {
	tail := strings.Repeat("● The build passed; the deploy step signs in to the registry and pushes the image, then waits.\n\n", 400) +
		strings.Repeat("x", 20000) + loginRowFrame
	for _, prog := range []string{"opencode", ""} {
		b.Run("as "+prog, func(b *testing.B) {
			for b.Loop() {
				harnessProblemIn(tail, prog)
			}
		})
	}
}

// A Claude Code session at work, a message typed into its input box that
// quotes a CLI's login wording, wrapped, its last row the quote alone — the
// screen of a working session once read as logged out, and what was sent to it held
// (its words changed; its rows, its spacing and how they run together as
// Claude Code draws them, as they were).
const quotingLoginWordsScreen = `BothPRsalsocatchrealnoticesthatmainmissed.Theauthorshipandwordingarefine.

✻Brewedfor4m54s·done5:03PM

──────────────────────────────────────────────────────────────────────────────────────────────────────────
❯ Onemoreforthereview:thefollow-upturnstheremainingloginpatternsintowholerows.Notethechange:
  "AuthenticationrequiredforMCPServer/A2A/policy"nolongerreadsasloggedout.Checkthatitcan'thidetheagent'sown
  "Authenticationrequired".
──────────────────────────────────────────────────────────────────────────────────────────────────────────
⏵⏵bypasspermissionson (shift+tabtocycle)✔Updateinstalled·Restarttoupdate`

func TestAMessageQuotingLoginWordsIsNotALogin(t *testing.T) {
	for _, s := range []string{quotingLoginWordsScreen, strings.ReplaceAll(quotingLoginWordsScreen, `"Authenticationrequired".`, `Authenticationrequired.`)} {
		for _, prog := range []string{"claude", ""} {
			if harnessProblemIn(harnessTail(s, harnessRows), prog) || harnessProblemIn(harnessHealthTail(s, harnessRows), prog) {
				t.Errorf("as %q: a message at the prompt quoting login words read as logged out:\n%s", prog, s)
			}
		}
		a := &Agent{}
		a.noteHarnessHealth(s, "claude", false)
		a.noteHarnessHealth(s, "claude", false)
		if a.harnessDown() {
			t.Error("the session was marked logged out")
		}
	}
}

// What a CLI's status rows say of its login in words that drift from one
// version to the next, and what an agent's answer says in the same words.
func TestLoginStatusWordingAndAnAnswersRows(t *testing.T) {
	box := "\n────\n❯ \n────\n"
	for _, c := range []struct {
		name, prog, tail string
		want             bool
	}{
		{"its own \"Authentication required.\"", "gemini", "answer" + box + " Authentication required.", true},
		{"\"Authentication required · Sign in again to continue\"", "claude", "answer" + box + "  Authentication required · Sign in again to continue", true},
		{"\"Authentication required. Run /login\"", "claude", "answer" + box + "  Authentication required. Run /login", true},
		{"\"Authentication required (press Enter to sign in)\"", "gemini", "answer" + box + " Authentication required (press Enter to sign in)", true},
		{"\"Authentication required: run /auth\"", "qwen", "answer" + box + " Authentication required: run /auth", true},
		{"\"Authentication required - please sign in\"", "gemini", "answer" + box + " Authentication required - please sign in", true},
		{"its status with a hint after it on the row", "claude", "answer" + box + "  Authentication required · Sign in again to continue      ? for shortcuts", true},
		{"its status wrapped onto two rows", "claude", "answer" + box + "  Authentication required · Sign in again\n  to continue", true},
		{"\"Not logged in · Run /login\" alone", "gemini", "answer" + box + " Not logged in · Run /login", true},
		{"\"Login expired. Please run /login\", no program known", "", "answer" + box + " Login expired. Please run /login", true},
		{"Claude Code's menu with another plan", "claude", " Select login method:\n\n ❯ 1. Claude account with subscription · Pro, Max, Team, Enterprise, or Free\n   2. Anthropic Console account · API usage billing\n", true},
		{"the menu's item alone, another plan", "claude", "answer\n ❯ 1. Claude account with subscription · Pro, Max, Team, Enterprise, or Free" + box, true},
		{"its own notice, led by ●", "claude", "answer" + box + "● Authentication required · Sign in again to continue", true},
		{"an MCP server's login", "gemini", "answer" + box + " Authentication required for MCP Server: github", false},
		{"an A2A agent's login", "gemini", "answer" + box + " Authentication required for A2A agent: planner", false},
		{"an answer's last row \"Authentication required.\"", "claude", "● The staging API returned 401:\n  Authentication required.", false},
		{"an answer's last row \"Not logged in\"", "claude", "● Checked the dashboard session. Result:\n  Not logged in", false},
		// Codex's and Gemini CLI's answers start with their own marks (review).
		{"Codex's answer (•), its last row \"Login required: …\"", "codex", "• I checked the CI logs. The registry step failed:\n  Login required: run docker login with the bot token.", false},
		{"the same, no program known", "", "• I checked the CI logs. The registry step failed:\n  Login required: run docker login with the bot token.", false},
		{"Gemini CLI's answer (✦), its last row \"Authentication required.\"", "gemini", "✦ The staging API answered 401:\n  Authentication required.", false},
		{"a tool's output's last row", "claude", "  ⎿  Fetched https://staging.example.com/api\n     Authentication required.", false},
		{"a person's text at the prompt", "claude", "────\n❯ why does it say\n  Authentication required.\n────", false},
		{"what an agent goes on to write", "gemini", "answer" + box + " Authentication required: run the tests again and it works", false},
		{"Droid's state carried on in an answer", "droid", "● It printed that this Droid is not signed in, and I went on with the next file.\n", false},
		{"a bundle label of Droid's, never recorded on a screen, ending an answer", "droid", "● It said:\n  This Droid is not signed in\n", false},
		{"Droid's login sentence in an answer, no menu under it", "droid", "● Please login with your Factory account to continue.\n  Then run droid again and the tests pass.\n", false},
		{"Codex's IdP row carried on in an answer", "codex", "● The server said enterprise IdP session has no refresh token; sign in again, so I stopped.\n› ", false},
		// Claude Code 2.1.319: a claude.ai login that ended while another
		// login profile pays for the usage: the session goes on, and the
		// footer and its warning only say so; not a login wanted.
		{"Claude Code's footer after a claude.ai login ended, usage billed elsewhere", "claude", "answer" + box + "  claude.ai login ended · usage now billed to Acme Corp · /login to sign in", false},
		{"Claude Code's warning of the same", "claude", "  Warning: the claude.ai login on this machine has ended. This run uses the sign-in from ant auth login instead, and its usage is billed to Acme Corp. Run claude auth login to sign in again, or claude auth logout to remove the ended login and keep that sign-in without this warning." + box, false},
		// Qwen Code 0.26.9: rows that say the session is fine, and its
		// notices quoted or carried on in an agent's own words.
		{"Qwen's models that would not load, signed in", "qwen", "answer" + box + "  Could not load models. Your account is still signed in. Please try again.", false},
		{"Qwen signed in", "qwen", "answer" + box + "  Signed in successfully", false},
		{"Qwen signed in as", "qwen", "answer" + box + "  Signed in as someone@example.com", false},
		{"a Qwen command for subscribers only", "qwen", "answer" + box + "  This command is only available with Qwen Subscription login. Use /auth to sign in to Qwen Subscription.", false},
		{"Qwen's notice in an answer's words", "qwen", "● The docs say: not signed in. Run /auth to sign in, then try again." + box, false},
		{"Qwen's notice quoted", "qwen", "  \"Your Qwen session has expired. Run /auth to sign in again.\" is what it printed." + box, false},
	} {
		if got := harnessProblemIn(c.tail, c.prog); got != c.want {
			t.Errorf("%s (as %q): logged out %v, want %v:\n%s", c.name, c.prog, got, c.want, c.tail)
		}
	}
}

// A status line's "what to do" is only its command or its sign-in words, to
// the row's end: an agent's sentence that starts as one and goes on, as the
// bottom row or right above a bare prompt, is not a login (review).
func TestLoginStatusHowToEndsTheRow(t *testing.T) {
	const notice = "Login expired · Please run /login"
	for _, tail := range []string{
		" so I paused the deploy.",
		", then retry.",
		" so I paused the deploy and will pick the work up again after lunch.",
	} {
		for _, glyph := range []string{"", "● ", "  ⎿  ", "• "} {
			for _, screen := range []string{glyph + notice + tail, glyph + notice + tail + "\n\n❯ "} {
				for _, prog := range []string{"claude", "codex", ""} {
					if harnessProblemIn(screen, prog) {
						t.Errorf("as %q: read as logged out: %q", prog, screen)
					}
				}
			}
		}
	}
	for _, row := range []string{
		" Login expired · Please run /login",
		" Login expired. Please run /login.",
		" Login required · run `codex login`",
		" Not logged in. Run `agent login` first.",
		" Not logged in. Run `cursor-agent login` first.",
		" Not logged in · Run /login to sign in again",
		" Authentication required: use /auth",
		" Authentication required · Please re-authenticate",
		" Authentication required · Sign in again to continue      ? for shortcuts",
		" Authentication required (press Enter to sign in)",
	} {
		for _, prog := range []string{"claude", "cursor-agent", ""} {
			for _, screen := range []string{row, "● Done.\n────\n❯ \n────\n" + row} {
				if !harnessProblemIn(screen, prog) {
					t.Errorf("as %q: its status row not read as logged out: %q", prog, screen)
				}
			}
		}
	}
	// A command's "first" ends the row too; what goes on after it does not.
	for _, prog := range []string{"codex", "cursor-agent", ""} {
		if screen := " Login required · run `codex login` first, then retry"; harnessProblemIn(screen, prog) {
			t.Errorf("as %q: read as logged out: %q", prog, screen)
		}
	}
}
