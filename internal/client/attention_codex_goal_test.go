// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"reminal/internal/session"
)

// codexGoalScreen draws a recorded Codex screen (tmux, 120x40) into a real
// terminal screen and gives back the agent holding it, as the session would,
// its hook having said "done".
func codexGoalScreen(t *testing.T, name string) *Agent {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "harness-screens", name+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	a, _, _ := speechAgent(t)
	a.speech.blocked = nil // the real check
	a.attnFG, a.attnState = "codex", "done"
	a.screen = testScreen(t, 120, 40)
	rows := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	_, _ = a.screen.Write([]byte(strings.Join(rows, "\r\n")))
	return a
}

// Codex's choosers need the person: "input", whatever its hook said, and
// nothing said by voice is typed into them (a Return picks the option under
// the cursor).
//
//   - "Resume paused goal?" (1. Resume goal, 2. Leave paused), when a thread
//     whose goal was paused is resumed: drawn over its conversation,
//     mid-screen, with its input box still at the bottom. "Resume goal" runs
//     turns on its own.
//   - "Update available" at its start (1. Update now, 2. Skip, 3. Skip until
//     next version): at the bottom, with no cue for its "›" or its hint.
//     "Update now" runs npm install.
//
// Recorded with Codex 0.171.0 and 0.170.0 (the update one with 0.170.0, a
// newer one out), with the cursor on the first option and on another.
func TestCodexChoosersAreAQuestion(t *testing.T) {
	for name, question := range map[string]string{
		"codex-resume-goal":            "Resume paused goal?",
		"codex-resume-goal-leave":      "Resume paused goal?",
		"codex-resume-goal-0170":       "Resume paused goal?",
		"codex-resume-goal-0170-leave": "Resume paused goal?",
		"codex-update":                 "Update available",
		"codex-update-skip":            "Update available",
	} {
		t.Run(name, func(t *testing.T) {
			a := codexGoalScreen(t, name)
			render := a.screen.Render()
			if !strings.Contains(render, question) {
				t.Fatalf("the recording does not have %q", question)
			}
			state, asking := attnReadScreen("codex", true, render, 60000, time.Minute, true)
			if state != "input" || !asking {
				t.Errorf("screen: %q (asking %v), want input (asking)", state, asking)
			}
			hs := &session.HookState{State: "done", TS: time.Now().Add(-time.Minute)}
			if got, _ := resolveAttn(state, hs, time.Now().Add(-time.Minute), time.Time{}, 60000, asking, false, false, false); got != "input" {
				t.Errorf("with its hook at done: %q, want input", got)
			}
			if got := a.speechBlocked(); got != speechAsking {
				t.Errorf("speechBlocked = %q, want %q: voice would type into the question", got, speechAsking)
			}
		})
	}
}

// Carrying on: once the goal question is answered, the same conversation and
// box with no question over them are not a question, and voice types in:
// left paused (at rest), and resumed (the goal's turn running, its working
// row over the box).
func TestCodexAfterResumeGoalQuestionIsNotAQuestion(t *testing.T) {
	for _, name := range []string{"codex-resume-goal-left", "codex-resume-goal-resumed", "codex-resume-goal-0170-left", "codex-resume-goal-0170-resumed"} {
		t.Run(name, func(t *testing.T) {
			a := codexGoalScreen(t, name)
			render := a.screen.Render()
			if strings.Contains(render, "Resume paused goal?") {
				t.Fatal("the recording still has the question")
			}
			if state, asking := attnReadScreen("codex", true, render, 60000, time.Minute, true); state == "input" || asking {
				t.Errorf("screen: %q (asking %v), want it not asking", state, asking)
			}
			if got := a.speechBlocked(); got != "" {
				t.Errorf("speechBlocked = %q, want it allowed", got)
			}
		})
	}
}

// Its words in text are not Codex asking: the agent writing them out, a
// message the person sent that starts with a number, and the chooser's shape
// on another agent's screen.
func TestCodexChooserWordsInTextAreNotAQuestion(t *testing.T) {
	for name, c := range map[string]struct{ prog, screen string }{
		"its hint in a sentence": {"codex", "• Codex shows › 1. Resume goal, and under it enter select · esc back.\n\n› Ask Codex to do anything"},
		"the hint, no option":    {"codex", "• The chooser's footer reads:\n  enter select · esc back\n\n› Ask Codex to do anything"},
		"a numbered message":     {"codex", "› 1. fix the build\n\n• Fixed.\n\n› Ask Codex to do anything"},
		"another agent":          {"claude", "› 1. Resume goal\n  2. Leave paused\n  enter select · esc back\n\n> "},
	} {
		if attnDialogOnScreen(c.prog, c.screen) {
			t.Errorf("%s: read as a question", name)
		}
	}
}
