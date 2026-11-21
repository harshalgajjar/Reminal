package client

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Claude Code fixtures are raw captures: its prompt sign is followed by
// a no-break space, which a fixture rebuilt by hand would lose (and with it
// what the box shape must accept).
func TestVoiceFixturesKeepClaudesNoBreakSpace(t *testing.T) {
	for _, f := range []string{"claude-fresh.txt", "claude-rest.txt", "claude-after-escape.txt"} {
		raw, err := os.ReadFile(filepath.Join("testdata", "voice-screens", f))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), "❯\u00a0") {
			t.Errorf("%s has no \"❯\" followed by a no-break space: not a raw capture", f)
		}
	}
}

// Captured screens of each agent: its box is found at rest, and not in a
// dialog drawn over it or in its place.
func TestVoiceBoxShapeOnCapturedScreens(t *testing.T) {
	for _, c := range []struct {
		prog, file string
		box        bool
	}{
		{"claude", "claude-fresh.txt", true},
		{"claude", "claude-rest.txt", true},
		{"claude", "claude-after-escape.txt", true},
		{"claude", "claude-model.txt", false},
		{"claude", "claude-plan.txt", false},
		{"claude", "claude-question.txt", false},
		{"claude", "claude-resume.txt", false},
		{"claude", "claude-resume-sessions.txt", false},
		{"claude", "claude-permission.txt", false},
		{"qwen", "qwen-start.txt", true},
		{"qwen", "qwen-idle.txt", true},
		{"qwen", "qwen-after-voice.txt", true},
		{"qwen", "qwen-approval.txt", false},
		{"qwen", "qwen-question.txt", false},
		{"gemini", "gemini-start.txt", true},
		{"gemini", "gemini-after-voice.txt", true},
		{"gemini", "gemini-shell-approval.txt", false},
		{"gemini", "gemini-question.txt", false},
	} {
		raw, err := os.ReadFile(filepath.Join("testdata", "voice-screens", c.file))
		if err != nil {
			t.Fatal(err)
		}
		a := gateAgentSized(t, c.prog, "done", strings.ReplaceAll(string(raw), "\n", "\r\n"), 120, 40)
		if got := voiceBoxDrawn(c.prog, a.screen.Render()); got != c.box {
			t.Errorf("%s %s: voiceBoxDrawn = %v, want %v", c.prog, c.file, got, c.box)
		}
	}
}

// A question picker cut so its cursor row sits right under a rule, inside
// the rows the box is looked for in: still a list of choices (the next
// number is further down, under a description row; its help row is there).
// Drafts with a number in them are the box.
func TestVoiceBoxTellsAPickerFromADraft(t *testing.T) {
	rule := strings.Repeat("─", 60)
	for _, c := range []struct {
		name, screen string
		box          bool
	}{
		{"picker, next number under a description row", rule + "\n❯ 1. Alpha\n     the first\n  2. Beta\n     the second\n", false},
		{"picker, help row only", rule + "\n❯ 1. Alpha\n     the first\nEnter to select · ↑/↓ to navigate · Esc to cancel\n", false},
		{"draft that is a number", rule + "\n❯ 42\n" + rule + "\n  ? for shortcuts\n", true},
		{"draft 1. with nothing numbered below", rule + "\n❯ 1. buy milk\n" + rule + "\n  ? for shortcuts\n", true},
		{"draft 1. with a 3. below (not the next number)", rule + "\n❯ 1. buy milk\n" + rule + "\n3. Some note\n", true},
	} {
		if got := voiceBoxDrawn("claude", c.screen); got != c.box {
			t.Errorf("%s: voiceBoxDrawn = %v, want %v", c.name, got, c.box)
		}
	}
}

// The box is bracketed: an edge above the prompt row and one below it. Rows
// a dialog might draw under a rule pass for the box only if they are
// bracketed too and not a numbered list. A box with a draft over several
// rows is still the box.
func TestVoiceBoxNeedsItsClosingEdge(t *testing.T) {
	rule := strings.Repeat("─", 60)
	for _, c := range []struct {
		name, screen string
		box          bool
	}{
		{"a plain option", rule + "\n❯ Yes\n  No\n", false},
		{"a lettered option", rule + "\n❯ a) Yes\n  b) No\n", false},
		{"one numbered option, no help row", rule + "\n❯ 1. Only option\n", false},
		{"help row cut off in a narrow window", rule + "\n❯ 1. Alpha\n     the first\nEnter to sel\n", false},
		{"help row above the cursor", "Enter to select\n" + rule + "\n❯ 1. Alpha\n     the first\n", false},
		{"a draft over three rows", rule + "\n❯ first line of a long\n  draft that runs on\n  for a third row\n" + rule + "\n  ? for shortcuts\n", true},
		{"an empty box", rule + "\n❯ \n" + rule + "\n  ? for shortcuts\n", true},
	} {
		if got := voiceBoxDrawn("claude", c.screen); got != c.box {
			t.Errorf("%s: voiceBoxDrawn = %v, want %v", c.name, got, c.box)
		}
	}
	// Gemini closes its box with "▀".
	if !voiceBoxDrawn("gemini", strings.Repeat("▄", 60)+"\n > hello\n"+strings.Repeat("▀", 60)+"\n") {
		t.Error("gemini box with its closing edge: not found")
	}
	if voiceBoxDrawn("gemini", strings.Repeat("▄", 60)+"\n > hello\n") {
		t.Error("gemini box with no closing edge: found")
	}
}
