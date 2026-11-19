// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// goose asks before it runs a tool ("Goose would like to call the above
// tool, do you allow?", then Allow / Always Allow / Deny / Cancel), and waits
// for the person: the session needs them, never "done". Read on the bottom
// five rows too, where the question is not (it is the 6th from the bottom,
// above its four choices and the closing rule), so nothing is typed into the
// chooser: a letter typed there moves its cursor, and Return takes the
// choice. Recorded with goose 1.60.1 and (older) 1.59.2 in tmux, 120 columns.
func TestGoosesToolApprovalNeedsYou(t *testing.T) {
	for _, name := range []string{"goose-approval", "goose-approval-older"} {
		raw, err := os.ReadFile(filepath.Join("testdata", "harness-screens", name+".txt"))
		if err != nil {
			t.Fatal(err)
		}
		screen := string(raw)
		// The cursor where the chooser starts, and wherever a key moves it
		// (each choice selected shows "> " before it; only the first carries
		// its hint, as recorded).
		const first = "|  > Allow (Allow the tool call once)\n|    Always Allow\n|    Deny\n|    Cancel"
		if !strings.Contains(screen, first) {
			t.Fatalf("%s: the recording does not have the chooser as expected", name)
		}
		for at, choices := range map[string]string{
			"Allow":        first,
			"Always Allow": "|    Allow\n|  > Always Allow (Always allow the tool call)\n|    Deny\n|    Cancel",
			"Deny":         "|    Allow\n|    Always Allow\n|  > Deny (Deny the tool call)\n|    Cancel",
			"Cancel":       "|    Allow\n|    Always Allow\n|    Deny\n|  > Cancel (Cancel the session)",
		} {
			s := strings.Replace(screen, first, choices, 1)
			if got := classifyAttnFor("goose", true, attentionProbeTail(s, attnTailRows), 60000, time.Minute, true); got != "input" {
				t.Errorf("%s, on %s: %q, want input (needs you)", name, at, got)
			}
			if !attnLooksLikePrompt(attentionProbeTail(s, attnPromptRows)) {
				t.Errorf("%s, on %s: the bottom rows are not read as a question: voice would type into it", name, at)
			}
		}
		if harnessProblemIn(harnessTail(screen, harnessRowsFor("goose")), "goose") {
			t.Errorf("%s: read as signed out", name)
		}
	}
}

// The same words in an agent's own text are not goose asking: a table of
// them, a sentence naming the choices, its question quoted and gone on from.
func TestGooseApprovalWordsInTextAreNotAQuestion(t *testing.T) {
	for name, tail := range map[string]string{
		"a table":      "| Choice | What it does |\n| Allow | once |\n| Always Allow | from now on |\n| Deny | no |\n| Cancel | stops |",
		"a list":       "goose offers Allow, Always Allow, Deny and Cancel.\n|    Allow\n|    Always Allow\n|    Deny\n|    Cancel",
		"its question": "It asked \"Goose would like to call the above tool, do you allow?\" and I chose Allow.\nDone.",
	} {
		if attnLooksLikePrompt(tail) {
			t.Errorf("%s: read as a question", name)
		}
	}
}

// goose told it cannot run for want of a sign-in, after the message it was
// given: its cursor-agent provider not signed in to cursor-agent (the row
// wrapped at 120 columns), and a provider with nothing set up ("Provider is
// not configured.", with no "Run goose configure" after it). Signed out, not
// done. Recorded with goose 1.60.1.
func TestGoosesProviderSignInErrorsAreSignedOut(t *testing.T) {
	for _, name := range []string{"goose-cursor-signedout", "goose-muse-unconfigured"} {
		raw, err := os.ReadFile(filepath.Join("testdata", "harness-screens", name+".txt"))
		if err != nil {
			t.Fatal(err)
		}
		if !harnessProblemIn(harnessTail(string(raw), harnessRowsFor("goose")), "goose") {
			t.Errorf("%s: not read as signed out", name)
		}
	}
}
