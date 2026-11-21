package client

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// gateAgentSized is gateAgent on a screen of the given size (captured
// screens are as wide as the terminal they came from).
func gateAgentSized(t *testing.T, fg, state, screen string, cols, rows int) *Agent {
	t.Helper()
	a, _, _ := speechAgent(t)
	a.speech.blocked = nil // the real check
	a.attnFG, a.attnState = fg, state
	a.screen = testScreen(t, cols, rows)
	_, _ = a.screen.Write([]byte(screen))
	return a
}

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
		{"qwen empty with its placeholder", "qwen", rule + "\n\x1b[38;5;183m> \x1b[0m\x1b[4m \x1b[0m\x1b[38;5;103m Type your message or @path/to/file\n" + rule + "\n", false},
		{"qwen draft", "qwen", rule + "\n\x1b[38;5;183m> \x1b[0mhello\x1b[4m \x1b[0m\n" + rule + "\n", true},
		{"gemini empty with its placeholder", "gemini", strings.Repeat("▄", 60) + "\n\x1b[38;5;183m> \x1b[0m\x1b[7m \x1b[0m\x1b[38;5;248m Type your message or @path/to/file\n" + strings.Repeat("▀", 60) + "\n", false},
		{"gemini placeholder words without its colour", "gemini", strings.Repeat("▄", 60) + "\n >   Type your message or @path/to/file\n" + strings.Repeat("▀", 60) + "\n", false},
		{"qwen empty, zero-width space", "qwen", rule + "\n\x1b[38;5;183m> \x1b[0m\u200b\n" + rule + "\n", false},
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

// Raw styled captures of each agent's box, empty and with a draft (Gemini
// 0.69.0 and Qwen 0.28.0 at 120, 80 and 60 columns; Claude Code 2.1.328 and
// 2.1.326): empty boxes typed into, drafts refused.
func TestSpeechDraftCapturesOfEachAgent(t *testing.T) {
	size := func(name string) (int, int) {
		switch {
		case strings.Contains(name, "w80"):
			return 80, 24
		case strings.Contains(name, "w60"):
			return 60, 24
		}
		return 120, 40
	}
	for _, c := range []struct{ prog, dir string }{{"gemini", "gemini-drafts"}, {"qwen", "qwen-drafts"}, {"claude", "claude-drafts"}} {
		files, _ := filepath.Glob(filepath.Join("testdata", "voice-screens", "styled", c.dir, "*.ans"))
		if len(files) == 0 {
			t.Fatalf("no captures in %s", c.dir)
		}
		for _, f := range files {
			name := filepath.Base(f)
			cols, rows := size(name)
			got := styledScreen(t, c.prog, filepath.Join(c.dir, name), cols, rows).speechBlocked()
			empty := (strings.Contains(name, "empty") || strings.Contains(name, "first-run") ||
				strings.Contains(name, "after-turn") || strings.Contains(name, "after-clear-2"))
			switch {
			case empty && got != "":
				t.Errorf("%s %s (empty box): speechBlocked = %q, want it allowed", c.prog, name, got)
			case !empty && got == "":
				t.Errorf("%s %s (a draft): typed into, want it refused", c.prog, name)
			}
		}
	}
}

// Claude Code drew no placeholder in any captured state; a suggestion shown
// to a signed-in user is read as a placeholder when it is drawn faint only
// (typed text never is), so voice still works at that empty box. Typed
// text beside it is a draft.
func TestVoiceClaudeFaintSuggestionIsNotADraft(t *testing.T) {
	rule := strings.Repeat("─", 60)
	box := func(row string) string { return rule + "\n" + row + "\n" + rule + "\n" }
	if voiceBoxDraft("claude", box("❯ \x1b[0m\x1b[7m \x1b[0m\x1b[2mTry \"fix the tests\"")) {
		t.Error("a faint suggestion after the cursor read as a draft")
	}
	if !voiceBoxDraft("claude", box("❯ hi \x1b[2mTry \"fix the tests\"")) {
		t.Error("typed text beside a faint suggestion not read as a draft")
	}
	if !voiceBoxDraft("claude", box("❯ Try \"fix the tests\"")) {
		t.Error("the same words not faint (typed) not read as a draft")
	}
}

// The placeholder is read by its words, whatever its colour: rewritten from
// the real captures with the same grey in true colour (a terminal with
// COLORTERM=truecolor), in another grey (another theme), and with no colour
// at all (NO_COLOR, a dumb terminal): each empty box typed into. A narrow
// window that cuts the placeholder still reads as empty; text that is not
// the placeholder's start is a draft.
func TestVoicePlaceholderIsReadByItsWords(t *testing.T) {
	sgr := regexp.MustCompile(`\x1b\[[0-9;:]*m`)
	for _, c := range []struct{ prog, file, grey string }{
		{"qwen", "qwen-drafts/fresh-empty-w120.ans", "\x1b[38;5;103m"},
		{"gemini", "gemini-drafts/fresh-empty-w120.ans", "\x1b[38;5;248m"},
	} {
		raw, err := os.ReadFile(filepath.Join("testdata", "voice-screens", "styled", c.file))
		if err != nil {
			t.Fatal(err)
		}
		for name, screen := range map[string]string{
			"as captured":  string(raw),
			"true colour":  strings.ReplaceAll(string(raw), c.grey, "\x1b[38;2;168;168;168m"),
			"another grey": strings.ReplaceAll(string(raw), c.grey, "\x1b[38;5;242m"),
			"no colour":    sgr.ReplaceAllString(string(raw), ""),
		} {
			if !strings.Contains(string(raw), c.grey) {
				t.Fatalf("%s: the capture no longer holds %q", c.file, c.grey)
			}
			a := gateAgentSized(t, c.prog, "done", strings.ReplaceAll(screen, "\n", "\r\n"), 120, 40)
			if got := a.speechBlocked(); got != "" {
				t.Errorf("%s %s: speechBlocked = %q, want it allowed", c.prog, name, got)
			}
		}
	}
	rule := strings.Repeat("─", 40)
	for _, c := range []struct {
		row   string
		draft bool
	}{
		{"> Type your message or @pa", false}, // cut at a narrow box's edge
		{"> Type your message or @path/to/file", false},
		{"> Type your", true}, // too little of it to tell from a draft
		{"> Type your message please", true},
	} {
		if got := voiceBoxDraft("qwen", rule+"\n"+c.row+"\n"+rule+"\n"); got != c.draft {
			t.Errorf("qwen %q: voiceBoxDraft = %v, want %v", c.row, got, c.draft)
		}
	}
}

// Colour in its colon form reads as in its semicolon form.
func TestStyledCellsReadColonColours(t *testing.T) {
	for _, c := range []struct{ seq, fg string }{
		{"\x1b[38:5:103mx", "38;5;103"},
		{"\x1b[38;5;103mx", "38;5;103"},
		{"\x1b[38:2::168:168:168mx", "38;2;168;168;168"},
		{"\x1b[38:2:168:168:168mx", "38;2;168;168;168"},
		{"\x1b[38;2;168;168;168mx", "38;2;168;168;168"},
	} {
		if cells := styledCells(c.seq); len(cells) != 1 || cells[0].fg != c.fg {
			t.Errorf("%q: fg %q, want %q", c.seq, cells[0].fg, c.fg)
		}
	}
	rule := strings.Repeat("─", 40)
	if voiceBoxDraft("qwen", rule+"\n\x1b[38;5;153m> \x1b[0m\x1b[4m \x1b[0m\x1b[38:5:103m Type your\n"+rule+"\n") {
		t.Error("qwen placeholder start in the colon colour form read as a draft")
	}
}

// Said at a box holding a draft: nothing is typed, no Return, and the viewer
// is told why. Said at the same box empty: the words and the Return go.
func TestSpeechTypesNothingAfterADraft(t *testing.T) {
	for name, c := range map[string]struct {
		file  string
		typed int
	}{
		"draft": {"claude-w120/c05d-draft-1-buy-milk.ans", 0},
		"empty": {"claude-w120/c03-after-turn.ans", 2},
	} {
		a := styledScreen(t, "claude", c.file, 120, 40)
		var typed []string
		a.speech.typeKeys = func(b []byte) error { typed = append(typed, string(b)); return nil }
		speechIn(t, a, map[string]any{"kind": "said", "text": "and then"})
		time.Sleep(EnterSettle + 300*time.Millisecond)
		if len(typed) != c.typed {
			t.Errorf("%s: typed %q, want %d writes", name, typed, c.typed)
		}
	}
}

// Only an agent whose box reminal knows is read for a draft. Every other
// agent is typed into as before, whatever its screen holds; and an agent
// whose box is not on screen is not refused for that.
func TestSpeechDraftRuleLeavesOtherAgentsAsTheyWere(t *testing.T) {
	rule := strings.Repeat("─", 60)
	box := "\r\n" + rule + "\r\n> half a sentence\r\n" + rule + "\r\n"
	for _, prog := range []string{"codex", "amp", "goose", "opencode", "kilo", "cursor-agent", "agy", "pi", "copilot"} {
		if voiceBoxDraft(prog, strings.ReplaceAll(box, "\r\n", "\n")) {
			t.Errorf("%s: read for a draft, but reminal does not know its box", prog)
		}
	}
	for _, prog := range []string{"codex", "amp", "goose"} {
		if got := gateAgent(t, prog, "done", box).speechBlocked(); got != "" {
			t.Errorf("%s with text on screen: speechBlocked = %q, want it typed into as before", prog, got)
		}
	}
	// Claude Code with no box on screen (scrolled output only) is typed
	// into as before: the draft rule asks for nothing to be there.
	if got := gateAgent(t, "claude", "done", "\r\n● Done. The tests pass.\r\n").speechBlocked(); got != "" {
		t.Errorf("claude with no box on screen: speechBlocked = %q, want it typed into as before", got)
	}
}
