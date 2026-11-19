package client

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func styledScreen(t *testing.T, prog, rel string, cols, rows int) *Agent {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "voice-screens", "styled", rel))
	if err != nil {
		t.Fatal(err)
	}
	return gateAgentSized(t, prog, "done", strings.ReplaceAll(string(raw), "\n", "\r\n"), cols, rows)
}

// Raw captures with their styles (Claude Code 2.1.328 at 120, 80 and 60
// columns; Qwen Code 0.28.0): an empty box is typed into, its cursor cell
// and its placeholder are not typed text; a box holding anything typed is
// refused with its own notice.
func TestSpeechRefusesABoxWithADraftInIt(t *testing.T) {
	sizes := map[string][2]int{"w120": {120, 40}, "w80": {80, 24}, "w60": {60, 24}}
	empty := []string{"c01-fresh-auto-banner", "c02-fresh-manual", "c03-after-turn", "c02qb-after-esc-question", "c06b-after-esc-perm", "c12b-after-esc-plan"}
	drafts := []string{"c05-draft-3-typed-lines", "c05a-draft-4-typed-lines", "c05b-draft-6-pasted", "c05d-draft-1-buy-milk", "c05e-draft-42", "c05i-hash-with-text", "c10-slash-menu"}
	for w, size := range sizes {
		for _, c := range empty {
			if got := styledScreen(t, "claude", "claude-"+w+"/"+c+".ans", size[0], size[1]).speechBlocked(); got != "" {
				t.Errorf("claude %s %s (empty box): speechBlocked = %q, want it allowed", w, c, got)
			}
		}
		for _, c := range drafts {
			if got := styledScreen(t, "claude", "claude-"+w+"/"+c+".ans", size[0], size[1]).speechBlocked(); got != speechDraft {
				t.Errorf("claude %s %s (draft): speechBlocked = %q, want %q", w, c, got, speechDraft)
			}
		}
	}
	for _, c := range []string{"s-bare-1-menu", "s-mo-1-menu", "s-cl-1-menu"} { // "/", "/mo", "/cl" in the box
		if got := styledScreen(t, "claude", "claude-slash/"+c+".ans", 120, 40).speechBlocked(); got != speechDraft {
			t.Errorf("claude %s (slash command in the box): speechBlocked = %q, want %q", c, got, speechDraft)
		}
	}
	for _, c := range []string{"idle", "start", "after-voice"} { // Qwen's placeholder in 256-colour 103
		if got := styledScreen(t, "qwen", "qwen/"+c+".ans", 120, 40).speechBlocked(); got != "" {
			t.Errorf("qwen %s (empty box, placeholder): speechBlocked = %q, want it allowed", c, got)
		}
	}
}

// The same box with what each agent draws in it, as rows of the screen.
func TestVoiceBoxDraftCells(t *testing.T) {
	rule := strings.Repeat("─", 60)
	box := func(row string) string { return rule + "\n" + row + "\n" + rule + "\n" }
	for _, c := range []struct {
		name, prog, screen string
		draft              bool
	}{
		{"claude empty, cursor at the start", "claude", box("❯ \x1b[0m\x1b[7m "), false},
		{"claude empty, no cursor", "claude", box("❯ "), false},
		{"claude draft before the cursor", "claude", box("❯ hello\x1b[0m\x1b[7m "), true},
		{"claude placeholder (not pinned): a draft", "claude", box("❯ \x1b[7mT\x1b[0m\x1b[2mry \"fix the tests\""), true},
		{"qwen empty with its placeholder", "qwen", rule + "\n\x1b[38;5;183m> \x1b[0m\x1b[4m \x1b[0m\x1b[38;5;103m Type your message or @path/to/file\n" + rule + "\n", false},
		{"qwen draft", "qwen", rule + "\n\x1b[38;5;183m> \x1b[0mhello\x1b[4m \x1b[0m\n" + rule + "\n", true},
		{"gemini empty with its placeholder", "gemini", strings.Repeat("▄", 60) + "\n >   Type your message or @path/to/file\n" + strings.Repeat("▀", 60) + "\n", false},
		{"gemini draft", "gemini", strings.Repeat("▄", 60) + "\n > hello\n" + strings.Repeat("▀", 60) + "\n", true},
		{"claude draft on a second row", "claude", rule + "\n❯ first\n  second\n" + rule + "\n", true},
	} {
		if got := voiceBoxDraft(c.prog, c.screen); got != c.draft {
			t.Errorf("%s: voiceBoxDraft = %v, want %v", c.name, got, c.draft)
		}
	}
}

// Before the Return, what is in the box is the said line itself: that is
// not a draft of the person's, and the Return goes.
func TestSpeechOwnWordsAreNotADraft(t *testing.T) {
	rule := strings.Repeat("─", 60)
	a := gateAgent(t, "claude", "done", "\r\n"+rule+"\r\n❯ [voice] hello there\r\n"+rule+"\r\n  ? for shortcuts\r\n")
	if got := a.speechBlocked(); got != speechDraft {
		t.Fatalf("before the words, a box holding text: speechBlocked = %q, want %q", got, speechDraft)
	}
	if got := a.speechBlockedAfterWords(); got != "" {
		t.Fatalf("after our own words: speechBlockedAfterWords = %q, want it allowed", got)
	}
}
