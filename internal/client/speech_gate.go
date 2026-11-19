package client

import (
	"strings"
	"time"
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
	state, fg := a.inFront()
	for end := time.Now().Add(speechLookFor); fg == "" && time.Now().Before(end); state, fg = a.inFront() {
		a.kickAttention()
		time.Sleep(50 * time.Millisecond)
	}
	switch {
	case a.term != nil && a.term.HidesInput():
		return speechHidden
	case !isAgentProgram(fg):
		return speechNoAgent
	case state == "input" || a.promptOnScreenNow(fg):
		return speechAsking
	}
	return ""
}

// speechLookFor is how long words wait for the probe's first look.
const speechLookFor = time.Second

// inFront is the attention state and the program in front. On Unix the
// program is asked of the terminal now (its foreground process group),
// never taken from the probe's last look: an agent that has just exited
// leaves a shell there, and a said line must not run in it. Where the
// terminal cannot be asked (Windows), it is the probe's.
func (a *Agent) inFront() (state, fg string) {
	a.metaMu.Lock()
	state, fg = a.attnState, a.attnFG
	a.metaMu.Unlock()
	if a.term != nil {
		if pgrp := a.term.ForegroundPgrp(); pgrp > 0 {
			fg = foregroundProgram(pgrp, attentionForegroundName(pgrp))
		}
	}
	return state, strings.TrimPrefix(fg, "-")
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
