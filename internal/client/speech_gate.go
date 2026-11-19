package client

import (
	"regexp"
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
	speechBusy    = "busy"     // mid-turn, in an agent whose hook cannot say when it waits on the person
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
	}
	if why := a.hookBlocks(fg, since); why != "" {
		return why
	}
	// A hook's "input" can outlive its prompt: Claude Code reports nothing
	// when the person dismisses one with Esc, and its last report stays
	// "input" at its box until the next prompt. For an agent whose box
	// reminal knows, the screen read now decides: the box on screen with no
	// prompt wording means the prompt is gone. Without a known box, the
	// hook's word stands.
	if a.promptOnScreenNow(fg) || (state == "input" && !hasBox(fg)) {
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

// HookReportsWaiting is, by program, whether the agent's `reminal integrate`
// hook reports when it waits on the person ("input": a tool approval, a
// question). For one that does, its hook guards each such moment once it has
// reported at all. For one that cannot (Antigravity reports only a turn's
// start and end), a dialog it draws mid-turn would go unseen, so it is typed
// into only while its last report says its turn is over. An agent with no
// hook is not here: it never reports, and is not typed into, box or no box.
// cmd/reminal's tests keep this table equal to what each integration's
// hooks map.
var HookReportsWaiting = map[string]bool{
	"claude": true, "copilot": true, "gemini": true, "qwen": true, // hooks with an "input" (or split "notify") event
	"opencode": true, "kilo": true, // the plugin's permission.* events
	"pi":  true,  // its extension
	"agy": false, // PreInvocation and Stop only
}

// hookBlocks says why the hook gives no positive reason to type into prog
// now, or "" if it does: it must have reported for this session since the
// program started (a turn's state, or Claude Code's start-up "ready"), and,
// for an agent whose hook cannot report waiting, its last report must be
// that its turn is over.
func (a *Agent) hookBlocks(prog string, since time.Time) string {
	last := a.hookReportSince(since)
	switch {
	case last == "":
		return speechNoHook
	case !HookReportsWaiting[prog] && last != "done" && last != "ready":
		return speechBusy
	}
	return ""
}

// hookReportSince is the agent's last report for this session since the
// program in front started: a turn's state, or "ready" (written apart from
// turn states), whichever came last; "" if it has said nothing since.
func (a *Agent) hookReportSince(since time.Time) string {
	if a.speech.hooked != nil {
		return a.speech.hooked()
	}
	if since.IsZero() {
		return ""
	}
	last, at := "", time.Time{}
	if hs := session.ReadHookStateAnyAge(a.sessionID); hs != nil && !hs.TS.Before(since) {
		last, at = hs.State, hs.TS
	}
	if ready := session.HookReadyAt(a.sessionID); !ready.IsZero() && !ready.Before(since) && ready.After(at) {
		last = "ready"
	}
	return last
}

// voiceBoxMarks are, by program, the shape of the agent's input box at rest,
// for voice alone: words go in only while it is on screen. A dialog the
// agent draws (a tool approval, a question's picker, a trust or consent
// screen) replaces the box or is drawn over it, whatever its words, so the
// box being there is the positive test the hook cannot give for every kind
// of prompt (Gemini's and Qwen's hooks report tool approvals only). Kept
// apart from attnPromptMarks, which also decides the attention state: a mark
// that misses a layout here costs a refused line, not a wrong badge.
// Each is the box's top edge with the prompt row right under it (its text
// after the prompt sign captured): Claude Code's "❯" (or "!" in its shell
// mode), Qwen's and Gemini's ">". The sign is followed by a space, a no-break
// space (Claude Code draws one) or nothing.
var voiceBoxMarks = map[string]*regexp.Regexp{
	"claude": regexp.MustCompile(`(?m)^[ \t]*─{20,}\S?[ \t]*\n[ \t]*[❯!]([ \t\x{00a0}][^\n]*)?$`),
	"qwen":   regexp.MustCompile(`(?m)^[ \t]*─{20,}\S?[ \t]*\n>([ \t\x{00a0}][^\n]*)?$`),
	"gemini": regexp.MustCompile(`(?m)^[ \t]*▄{20,}\S?[ \t]*\n[ \t]*>([ \t\x{00a0}][^\n]*)?$`),
}

// voiceBoxRows is how many non-empty rows from the bottom the box is looked
// for in: at rest an agent's box sits right above its footer. Close to the
// bottom, because Gemini draws the messages already sent in the same box
// shape, and a dialog drawn under one of them must not pass for the box.
// Blank rows are skipped (some agents paint every row, some leave a gap).
// More than about four footer rows under a box push it out: refused, the
// safe side.
const voiceBoxRows = 7

var (
	numberedRow = regexp.MustCompile(`^[ \t\x{00a0}]*[0-9]+\.[ \t\x{00a0}]`)
)

func voiceBoxDrawn(prog, render string) bool {
	re := voiceBoxMarks[prog]
	if re == nil {
		return true
	}
	var rows []string
	for _, row := range strings.Split(stripANSI(render), "\n") {
		if strings.TrimSpace(row) != "" {
			rows = append(rows, strings.TrimRight(row, " "))
		}
	}
	if len(rows) > voiceBoxRows {
		rows = rows[len(rows)-voiceBoxRows:]
	}
	text := strings.Join(rows, "\n")
	for _, m := range re.FindAllStringSubmatchIndex(text, -1) {
		// A prompt row that reads as a numbered choice ("❯ 1. Yes") is one
		// only when another numbered row follows it, as in a list of choices;
		// a draft that starts with a number ("❯ 1. buy milk", "❯ 42") is not.
		after := ""
		if m[2] >= 0 {
			after = text[m[2]:m[3]]
		}
		if numberedRow.MatchString(after) {
			next := strings.SplitN(text[m[1]:], "\n", 3)
			if len(next) > 1 && numberedRow.MatchString(next[1]) {
				continue
			}
		}
		return true
	}
	return false
}

// promptOnScreenNow reads the screen itself: the attention state settles a
// moment after a prompt is drawn, and words arriving in that moment must not
// answer it. Three tests: a prompt's wording at the bottom of the screen; for
// OpenCode and Kilo (attnPromptMarks) their box's own mark not drawn now; and
// for Claude Code, Qwen and Gemini (voiceBoxMarks) their input box not on
// screen now. For agents with a box, the test is positive: words go in only
// while it is there.
func (a *Agent) promptOnScreenNow(prog string) bool {
	a.screenMu.Lock()
	scr := a.screen
	var render string
	if scr != nil {
		render = scr.Render()
	}
	a.screenMu.Unlock()
	if scr == nil {
		return attnPromptMarks[prog] != nil || voiceBoxMarks[prog] != nil // its box cannot be seen: not typed into
	}
	if attnPromptMarks[prog] != nil && !attnPromptDrawn(prog, attentionProbeTail(render, attnTailRows)) {
		return true
	}
	if !voiceBoxDrawn(prog, render) {
		return true
	}
	return attnLooksLikePrompt(attentionProbeTail(render, attnPromptRows))
}

// hasBox reports whether reminal knows the shape of prog's input box, so
// that whether the box is on screen can be read.
func hasBox(prog string) bool { return attnPromptMarks[prog] != nil || voiceBoxMarks[prog] != nil }
