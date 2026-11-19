package client

import (
	"strings"
	"time"

	"reminal/internal/session"
)

// Words said out loud are typed in only where they can do nothing but reach
// an agent as a message. Elsewhere they would act: at a permission prompt a
// stray "1" or "y" picks an option and the Return confirms the highlighted
// one; in a plain shell the line runs as a command; at a password prompt the
// words are taken as the password. So the session process, not the page,
// decides, and it decides from what is in front now, not what the viewer
// last saw.
//
// Why words are not typed, as the viewer is told (speechNotice):
const (
	speechNoAgent = "no-agent" // no agent in front: a shell, or another program
	speechAsking  = "asking"   // a prompt on screen waits for an answer there
	speechHidden  = "hidden"   // the terminal hides what is typed (a password)
	speechUnsent  = "unsent"   // typed, but a prompt came up before the Return: left unsent in the box
	speechNoHook  = "no-hook"  // an agent reminal has no positive reason to think is at its box
)

// speechBlocked says why words may not be typed now, or "" if they may.
//
// The agent is known by the program in front (inFront: asked of the
// terminal now on Unix); the state matters only for a prompt. On Windows,
// where it is the probe's, it may not have been read yet (a session just
// started, or taken over by a restarted reminal): it is asked to look.
//
// On Windows the program is the agent among the shell's children (a console
// has no foreground process group), and whether input is hidden cannot be
// asked, so there the gate rests on the prompt check.
func (a *Agent) speechBlocked() string {
	if a.speech.blocked != nil {
		return a.speech.blocked()
	}
	state, fg, since := a.inFront()
	for end := time.Now().Add(speechLookFor); fg == "" && time.Now().Before(end); state, fg, since = a.inFront() {
		a.kickAttention()
		time.Sleep(50 * time.Millisecond)
	}
	switch {
	case a.term != nil && a.term.HidesInput():
		return speechHidden
	case !isAgentProgram(fg):
		return speechNoAgent
	case attnPromptMarks[fg] == nil && !a.hookReportedSince(since):
		return speechNoHook
	case state == "input" || a.promptOnScreenNow(fg):
		return speechAsking
	}
	return ""
}

// speechLookFor is how long words wait for the probe's first look.
const speechLookFor = time.Second

// inFront is the attention state, the program in front, and when that
// program started. On Unix the program is asked of the terminal now (its
// foreground process group), never taken from the probe's last look: an
// agent that has just exited leaves a shell there, and a said line must not
// run in it. Where the terminal cannot be asked (Windows), it is the
// probe's, and its start is when the probe first saw it in front.
func (a *Agent) inFront() (state, fg string, since time.Time) {
	a.metaMu.Lock()
	state, fg, since = a.attnState, a.attnFG, a.attnFGAt
	a.metaMu.Unlock()
	if a.term != nil {
		if pgrp := a.term.ForegroundPgrp(); pgrp > 0 {
			fg = foregroundProgram(pgrp, attentionForegroundName(pgrp))
			if t, ok := session.ProcStartTime(pgrp); ok {
				since = t
			}
		}
	}
	return state, strings.TrimPrefix(fg, "-"), since
}

// hookReportedSince is the positive reason to type into an agent with no box
// mark of its own: its hook (reminal integrate) has reported a state for this
// session since the program in front started. Without it, a dialog drawn in
// words reminal does not know (a tool approval, a trust or consent screen)
// would take the words and the Return; with it, the hook's "input" and the
// screen read still guard each moment.
func (a *Agent) hookReportedSince(since time.Time) bool {
	if a.speech.hooked != nil {
		return a.speech.hooked()
	}
	if since.IsZero() {
		return false
	}
	// A turn's state, or that it is up and reading its input (Claude Code's
	// SessionStart, written apart from turn states): either counts.
	if hs := session.ReadHookStateAnyAge(a.sessionID); hs != nil && !hs.TS.Before(since) {
		return true
	}
	ready := session.HookReadyAt(a.sessionID)
	return !ready.IsZero() && !ready.Before(since)
}

// promptOnScreenNow reads the screen itself: the attention state settles a
// moment after a prompt is drawn, and words arriving in that moment must not
// answer it. Two tests: a prompt's wording at the bottom of the screen, and,
// for an agent whose input box has a mark of its own (attnPromptMarks:
// OpenCode, Kilo), that mark not drawn now, as it is not when a dialog
// (a permission prompt, a picker, a menu) is drawn over the box. For those
// agents the test is positive: words go in only while their box is there.
func (a *Agent) promptOnScreenNow(prog string) bool {
	a.screenMu.Lock()
	scr := a.screen
	var render string
	if scr != nil {
		render = scr.Render()
	}
	a.screenMu.Unlock()
	if scr == nil {
		return attnPromptMarks[prog] != nil // its box cannot be seen: not typed into
	}
	if attnPromptMarks[prog] != nil && !attnPromptDrawn(prog, attentionProbeTail(render, attnTailRows)) {
		return true
	}
	return attnLooksLikePrompt(attentionProbeTail(render, attnPromptRows))
}
