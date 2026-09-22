// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"regexp"
	"strings"
	"time"
	"unicode"

)

// Whether the agent in this session can work at all. A harness whose login
// has expired still draws its prompt, reads the line typed into it, and ends
// the turn at once with "Login expired · Please run /login": to everything
// that watches turns, the message was delivered and handled. It was not —
// and every later one would go the same way, until a person logs it in.
//
// So the bottom of the screen, where harnesses put their status line and
// such errors, is read on every attention tick, and while it says the agent
// is logged out the session reports that instead of an attention state: a
// session that looks idle is one you leave alone, and this one needs you.

// harnessLoggedOut is the state a session reports while its agent cannot
// work for want of a login. It is not one of the attention states — those say
// what the agent is doing, and this says it can do nothing.
const harnessLoggedOut = "logged-out"

// What agent CLIs print when they cannot run for want of a login. Matched
// with all whitespace removed: Claude Code draws the spaces between words as
// cursor movements, so its screen reads "●Loginexpired·Pleaserun/login" and
// "Notloggedin·Run/login" — and must match whether or not spaces render.
//
// harnessLoginNotice is Claude Code's shape: what went wrong, then the
// command to run. The command in the same breath is what tells it from an
// agent's own prose about logins ("…was not logged in, so the redirect to
// /login looped" does not match).
var harnessLoginNotice = regexp.MustCompile(`(loginexpired|notloggedin|invalidapikey|oauthtoken(has)?expired)[·:.,\-]*(please)?run/login`)

// harnessLoginScreen is the sign-in screen other CLIs start on when they
// have no login — a menu of ways to sign in, not an error. A message typed
// into one picks a menu item or answers its question: Amp's "Would you
// like to log in? [(y)es, (n)o]" took the first letter of a welcome as a
// no, and the login was gone. Each is the screen's own heading, specific
// enough not to turn up in an agent's prose.
var harnessLoginScreen = regexp.MustCompile(`youarecurrentlynotsignedin|` + // Antigravity CLI
	`howwouldyouliketoauthenticate|noauthenticationmethodselected|` + // Gemini CLI
	`wouldyouliketologintoamp|noapikeyfound\.startingloginflow|` + // Amp
	`signinwithchatgpt|provideyourownapikey|incorrectapikeyprovided|` + // Codex
	`alibabamodelstudio.{0,3}accessmethod|finishprovidersetup|` + // Qwen Code
	`connectwithanexistingmodelstudioapikey`)

// harnessLoginLines are other CLIs' notices: they count only on a short row
// that begins with them — a status line, not a sentence in an answer.
var harnessLoginLines = []string{
	"authenticationrequired", "pleaselogin", "pleasesignin", "loginrequired",
	"waitingforauth", "pleasesetanauthmethod", "loginexpired", "notloggedin",
}

// harnessShortRow is how long (whitespace removed) such a row may be.
const harnessShortRow = 48

// harnessRows is how much of the bottom of the screen is read: the status
// line and the notice above the prompt, not the conversation. Counted in
// rows that have something on them — a sign-in dialog is mostly its own
// blank rows, and counting those left its heading unread.
const harnessRows = 12

const (
	harnessDownAfter = 2  // ticks the notice must be seen to count
	harnessUpAfter   = 10 // ticks it must be gone to clear (~3 s)
)

// harnessHealth is the debounced state; guarded by Agent.metaMu.
type harnessHealth struct {
	down       bool
	seen, gone int
	since      time.Time
}

// harnessTail is the last n rows of a screen that have anything on them.
func harnessTail(render string, n int) string {
	var rows []string
	for _, row := range strings.Split(render, "\n") {
		if strings.TrimSpace(row) != "" {
			rows = append(rows, row)
		}
	}
	if len(rows) > n {
		rows = rows[len(rows)-n:]
	}
	return strings.Join(rows, "\n")
}

// harnessProblemIn reports whether the bottom of a screen says the agent is
// logged out.
func harnessProblemIn(tail string) bool {
	for _, row := range strings.Split(tail, "\n") {
		flat := strings.ToLower(strings.Join(strings.Fields(row), ""))
		if flat == "" {
			continue
		}
		if harnessLoginNotice.MatchString(flat) || harnessLoginScreen.MatchString(flat) {
			return true
		}
		lead := strings.TrimLeftFunc(flat, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
		if len(lead) <= harnessShortRow {
			for _, p := range harnessLoginLines {
				if strings.HasPrefix(lead, p) {
					return true
				}
			}
		}
	}
	return false
}

// noteHarnessHealth is called with each attention tick's screen.
func (a *Agent) noteHarnessHealth(render string) {
	// The render carries its styling — "\x1b[1mLogin\x1b[m \x1b[1mexpired" —
	// which would sit between the words; the text is what is read.
	bad := harnessProblemIn(harnessTail(stripANSI(render), harnessRows))
	a.metaMu.Lock()
	h := &a.harness
	changed := false
	if bad {
		h.gone = 0
		if h.seen++; !h.down && h.seen >= harnessDownAfter {
			h.down, h.since, changed = true, time.Now(), true
		}
	} else {
		h.seen = 0
		if h.gone++; h.down && h.gone >= harnessUpAfter {
			h.down, changed = false, true
		}
	}
	down := h.down
	a.metaMu.Unlock()
	if changed && !down {
		// Logged in again: the next tick's screen reading decides the state.
		a.setAttnState("")
	}
}

// harnessDown reports whether the agent here is logged out.
func (a *Agent) harnessDown() bool {
	a.metaMu.Lock()
	defer a.metaMu.Unlock()
	return a.harness.down
}

// harnessTurnedAway watches, just after a message was typed in, whether the
// agent turned it away for want of a login — true as soon as it says so;
// false once it is plainly working on it, or after a while.
func (a *Agent) harnessTurnedAway() bool {
	start := time.Now()
	for time.Since(start) < 6*time.Second {
		if a.harnessDown() {
			return true
		}
		a.metaMu.Lock()
		working := a.attnState == "working"
		a.metaMu.Unlock()
		if working && time.Since(start) > 2*time.Second {
			return false
		}
		time.Sleep(attnInterval)
	}
	return a.harnessDown()
}
