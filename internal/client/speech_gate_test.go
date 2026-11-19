package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"reminal/internal/session"
)

// Agent at its prompt, a screen showing it: words go in.
func gateAgent(t *testing.T, fg, state, screen string) *Agent {
	t.Helper()
	return gateAgentSized(t, fg, state, screen, 80, 24)
}

// gateAgentSized is gateAgent on a screen of the given size (captured
// screens are as wide as the terminal they came from).
func gateAgentSized(t *testing.T, fg, state, screen string, cols, rows int) *Agent {
	t.Helper()
	a, _, _ := speechAgent(t)
	a.speech.blocked = nil                            // the real check
	a.speech.hooked = func() string { return "done" } // an integrated agent whose hook has reported
	a.attnFG, a.attnState = fg, state
	a.screen = testScreen(t, cols, rows)
	_, _ = a.screen.Write([]byte(screen))
	return a
}

// claudeAtBox is Claude Code at its input box: the box's top edge, its prompt
// row, its bottom edge (what voiceBoxMarks looks for).
const claudeAtBox = "\r\n────────────────────────────────────────────────────────────\r\n❯ \r\n────────────────────────────────────────────────────────────\r\n  ? for shortcuts\r\n"

const claudeApprovalScreen = "\r\n Bash command\r\n   rm -rf build\r\n\r\n Do you want to proceed?\r\n ❯ 1. Yes\r\n   2. No, and tell Claude what to do differently (esc)\r\n"

func TestSpeechBlockedReadsWhatIsInFront(t *testing.T) {
	for _, c := range []struct {
		name, fg, state, screen, want string
	}{
		{"agent at its prompt", "claude", "done", claudeAtBox, ""},
		{"agent working", "claude", "working", "✻ Thinking… (esc to interrupt)\r\n" + claudeAtBox, ""},
		{"permission prompt", "claude", "input", claudeApprovalScreen, speechAsking},
		// Drawn a moment ago: the state has not settled yet, the screen says it.
		{"permission prompt not settled", "claude", "working", claudeApprovalScreen, speechAsking},
		{"bare shell", "zsh", "", "$ \r\n", speechNoAgent},
		{"login shell", "-zsh", "", "$ \r\n", speechNoAgent},
		{"another program", "vim", "done", "~\r\n", speechNoAgent},
		{"agent exited, state left over", "bash", "done", "$ \r\n", speechNoAgent},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := gateAgent(t, c.fg, c.state, c.screen).speechBlocked(); got != c.want {
				t.Fatalf("speechBlocked = %q, want %q", got, c.want)
			}
		})
	}
}

// Words said while a permission prompt is up are not typed, no Return is
// sent, and the viewer is told why.
func TestSpeechSaidAtAPermissionPromptTypesNothing(t *testing.T) {
	for _, kind := range []string{"said", "part"} {
		a := gateAgent(t, "claude", "input", claudeApprovalScreen)
		var typed []string
		a.speech.typeKeys = func(b []byte) error { typed = append(typed, string(b)); return nil }
		speechIn(t, a, map[string]any{"kind": kind, "text": "yes 1"})
		time.Sleep(EnterSettle + 300*time.Millisecond)
		if len(typed) != 0 {
			t.Fatalf("%s: typed %q at a permission prompt", kind, typed)
		}
	}
}

// A prompt drawn after the words went in, before the Return: no Return.
func TestSpeechReturnHeldWhenAPromptAppears(t *testing.T) {
	a, typed, mu := speechAgent(t)
	calls := 0
	a.speech.blocked = func() string {
		calls++
		if calls > 1 {
			return speechAsking
		}
		return ""
	}
	speechIn(t, a, map[string]any{"kind": "said", "text": "and then"})
	time.Sleep(EnterSettle + 300*time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(*typed) != 1 || (*typed)[0] != "[voice] and then" {
		t.Fatalf("typed %q, want the words and no Return", *typed)
	}
}

func TestSpeechNoticeSaysWhy(t *testing.T) {
	b, _ := json.Marshal(speechOut{Notice: speechAsking})
	if string(b) != `{"notice":"asking"}` {
		t.Fatalf("notice payload %s", b)
	}
}

// An agent stuck calling speak is told, not read out without end.
func TestSpeakIsBounded(t *testing.T) {
	a, _, _ := speechAgent(t)
	now := time.Unix(1000, 0)
	a.speech.now = func() time.Time { return now }
	speechIn(t, a, map[string]any{"kind": "listening", "on": true})
	for i := 0; i < speechMaxPerMinute; i++ {
		if got, _ := a.speak("again"); got == "too-much" {
			t.Fatalf("line %d refused", i)
		}
	}
	if got, _ := a.speak("again"); got != "too-much" {
		t.Fatalf("line past the bound: %q", got)
	}
	now = now.Add(time.Minute)
	speechIn(t, a, map[string]any{"kind": "listening", "on": true})
	if got, _ := a.speak("again"); got == "too-much" {
		t.Fatal("still refused a minute later")
	}
}

// A transcribed "1", "2" or "y" as the whole utterance is exactly what a
// permission prompt takes as its answer: at one, nothing is typed, for a
// part as for a whole thought.
func TestSpeechLoneOptionKeyAtAPromptTypesNothing(t *testing.T) {
	for _, kind := range []string{"said", "part"} {
		for _, text := range []string{"1", "2", "y", "Yes.", "1."} {
			a := gateAgent(t, "claude", "input", claudeApprovalScreen)
			var mu sync.Mutex
			var typed []string
			a.speech.typeKeys = func(b []byte) error { mu.Lock(); typed = append(typed, string(b)); mu.Unlock(); return nil }
			speechIn(t, a, map[string]any{"kind": kind, "text": text})
			time.Sleep(EnterSettle + 200*time.Millisecond)
			mu.Lock()
			if len(typed) != 0 {
				t.Errorf("%s %q: typed %q at a permission prompt", kind, text, typed)
			}
			mu.Unlock()
		}
	}
}

// A fresh agent, or one in a session a restarted reminal has just taken
// over: the probe has not said what is in front yet. It is not refused as
// "no agent" when the probe, asked to look, finds one.
func TestSpeechWaitsForTheProbesFirstLook(t *testing.T) {
	a := gateAgent(t, "", "", claudeAtBox)
	go func() {
		time.Sleep(200 * time.Millisecond)
		a.metaMu.Lock()
		a.attnFG, a.attnState = "claude", "done"
		a.metaMu.Unlock()
	}()
	if got := a.speechBlocked(); got != "" {
		t.Fatalf("speechBlocked = %q for an agent the probe found a moment later", got)
	}
	// Still nothing in front after the wait: refused.
	b := gateAgent(t, "", "", "$ \r\n")
	if got := b.speechBlocked(); got != speechNoAgent {
		t.Fatalf("speechBlocked = %q with nothing found, want %q", got, speechNoAgent)
	}
}

// OpenCode and Kilo draw a dialog (a permission prompt, a picker, a menu)
// over their input box, in words no cue knows; for them words go in only
// while the box's own mark is on screen.
func TestSpeechTypesIntoOpenCodeOnlyAtItsBox(t *testing.T) {
	const box = "\r\n  ┃  Ask anything\r\n  ┃\r\n  ╹▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀\r\n   enter send\r\n"
	const permission = "\r\n  △ Permission required\r\n    ← Access external directory /tmp\r\n    Patterns - /tmp/*\r\n\r\n   Allow once   Allow always   Reject\r\n   ⇆ select  enter confirm\r\n"
	for _, prog := range []string{"opencode", "kilo"} {
		if got := gateAgent(t, prog, "done", box).speechBlocked(); got != "" {
			t.Errorf("%s at its box: speechBlocked = %q, want it allowed", prog, got)
		}
		if got := gateAgent(t, prog, "working", permission).speechBlocked(); got != speechAsking {
			t.Errorf("%s at a permission prompt: speechBlocked = %q, want %q", prog, got, speechAsking)
		}
	}
}

// No screen to read: an agent whose box has a mark is not typed into (its
// box cannot be seen); one without a mark rests on its state, as before.
func TestSpeechWithNoScreenFailsClosedForMarkedAgents(t *testing.T) {
	a, _, _ := speechAgent(t)
	a.speech.blocked = nil
	a.speech.hooked = func() string { return "done" }
	a.attnFG, a.attnState = "claude", "done" // has a box mark: its box cannot be seen
	if got := a.speechBlocked(); got != speechAsking {
		t.Errorf("claude, no screen: %q, want %q", got, speechAsking)
	}
	a.attnFG = "copilot" // no box mark: rests on its hook
	if got := a.speechBlocked(); got != "" {
		t.Errorf("copilot, no screen, done: %q, want it allowed", got)
	}
}

// The positive rule: an agent with no box mark is typed into only once its
// hook (reminal integrate) has reported for this session since the program
// in front started. Agents reminal knows but cannot hear from (not
// integrated, or not an integrate target) get nothing typed, whatever their
// screen says: their own dialogs are in words no list can keep up with.
func TestSpeechNeedsAHookForAgentsWithoutABoxMark(t *testing.T) {
	// Screens captured from real CLIs in Docker: each took words and a Return
	// as its answer before this rule.
	screens := map[string]string{
		"qwen":   "\r\n ? Shell touch /tmp/APPROVED (make a file)\r\n    touch /tmp/APPROVED\r\n  Allow execution of: 'touch'?\r\n  › 1. Yes, allow once\r\n    2. Always allow run 'touch *' commands in this project\r\n    3. Always allow run 'touch *' commands for this user\r\n    4. No, suggest changes (esc)\r\n\r\n\r\n ⠏ Waiting for user confirmation...\r\n",
		"gemini": "\r\n╭──────────────────────────────╮\r\n│ ? Shell touch /tmp/APPROVED   │\r\n│ Allow execution of [Shell]?   │\r\n│ ● 1. Allow once               │\r\n│   2. Allow for this session   │\r\n│   3. No, suggest changes (esc)│\r\n╰──────────────────────────────╯\r\n",
		"agy":    "\r\n Do you trust the contents of this project?\r\n > Yes, I trust this folder\r\n   No, exit\r\n enter Confirm\r\n",
		"goose":  "\r\n Tool approval request { \"tool_name\": \"shell\" }\r\n *  Goose would like to call the above tool, do you allow?\r\n |  > Allow (Allow the tool call once)\r\n |    Always Allow\r\n |    Deny\r\n |    Cancel\r\n",
	}
	for prog, screen := range screens {
		a := gateAgent(t, prog, "done", screen)
		a.speech.hooked = nil // the real hook state: none written
		t.Setenv("HOME", t.TempDir())
		a.sessionID = "GATE" + strings.ToUpper(prog)
		if got := a.speechBlocked(); got != speechNoHook {
			t.Errorf("%s with no hook report: speechBlocked = %q, want %q", prog, got, speechNoHook)
		}
	}
}

// A hook report counts only if it came after the program in front started:
// one left by an agent that ran here before (another agent, or the same one
// before it was restarted) is not this agent saying anything.
func TestSpeechHookReportMustBeFromThisRun(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	a := gateAgent(t, "claude", "done", claudeAtBox)
	a.speech.hooked = nil
	a.sessionID = "GATEHOOK"
	if err := session.WriteHookState(a.sessionID, "done"); err != nil {
		t.Fatal(err)
	}
	a.attnFGAt = time.Now().Add(-time.Minute) // in front since before the report
	if got := a.speechBlocked(); got != "" {
		t.Fatalf("hook reported since it started: speechBlocked = %q, want it allowed", got)
	}
	a.attnFGAt = time.Now().Add(time.Minute) // started after the report
	if got := a.speechBlocked(); got != speechNoHook {
		t.Fatalf("report from before it started: speechBlocked = %q, want %q", got, speechNoHook)
	}
	// Its own "input" still blocks once it has reported, for an agent whose
	// box reminal does not know (the hook's word stands).
	a.attnFG = "copilot"
	a.attnFGAt = time.Now().Add(-time.Minute)
	if err := session.WriteHookState(a.sessionID, "input"); err != nil {
		t.Fatal(err)
	}
	a.attnState = "input"
	if got := a.speechBlocked(); got != speechAsking {
		t.Fatalf("hook says input: speechBlocked = %q, want %q", got, speechAsking)
	}
}

// A freshly started Claude Code reports "ready" (its SessionStart hook)
// before any turn: words go in from its first moment, not after one typed turn.
func TestSpeechReadyReportCountsForAFreshAgent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	a := gateAgent(t, "claude", "done", claudeAtBox)
	a.speech.hooked = nil
	a.sessionID = "GATEREADY"
	a.attnFGAt = time.Now().Add(-time.Second)
	if got := a.speechBlocked(); got != speechNoHook {
		t.Fatalf("before its ready: speechBlocked = %q, want %q", got, speechNoHook)
	}
	if err := session.MarkHookReady(a.sessionID); err != nil {
		t.Fatal(err)
	}
	if got := a.speechBlocked(); got != "" {
		t.Fatalf("after its ready: speechBlocked = %q, want it allowed", got)
	}
}

// Antigravity's hook reports only a turn's start and end, never that it
// waits on the person: after its first turn it is typed into only while its
// last report says the turn is over. A tool approval drawn mid-turn (the
// state still "working") gets nothing.
func TestSpeechAgentWhoseHookCannotSayWaitingIsTypedIntoOnlyAtRest(t *testing.T) {
	const approval = "\r\n Run command: touch /tmp/APPROVED\r\n > Allow once\r\n   Allow always\r\n   Deny\r\n enter Confirm\r\n"
	a := gateAgent(t, "agy", "working", approval)
	a.speech.hooked = func() string { return "working" }
	if got := a.speechBlocked(); got != speechBusy {
		t.Errorf("agy mid-turn at a tool approval: speechBlocked = %q, want %q", got, speechBusy)
	}
	b := gateAgent(t, "agy", "done", "> \r\n")
	b.speech.hooked = func() string { return "done" }
	if got := b.speechBlocked(); got != "" {
		t.Errorf("agy at rest after a turn: speechBlocked = %q, want it allowed", got)
	}
	// An agent whose hook does report waiting is typed into mid-turn (its
	// "input" guards each prompt), as Claude Code is while it works.
	c := gateAgent(t, "claude", "working", "✻ Thinking… (esc to interrupt)\r\n"+claudeAtBox)
	c.speech.hooked = func() string { return "working" }
	if got := c.speechBlocked(); got != "" {
		t.Errorf("claude mid-turn: speechBlocked = %q, want it allowed", got)
	}
}

// The screens of real agents (testdata/voice-screens: Qwen Code 0.28.0 and
// Gemini CLI 0.69.0 captured in Docker; Claude Code 2.1.326 as its screens
// read at rest, at a tool approval and at the speak tool's approval). With
// the hook reported, words go in only while the agent's box is on screen:
// every dialog replaces it, whatever its words, including the kinds their
// hooks never report (Qwen's question picker).
func TestSpeechTypesOnlyWhileTheAgentsBoxIsOnScreen(t *testing.T) {
	for _, c := range []struct{ prog, file, want string }{
		{"claude", "claude-fresh.txt", ""},
		{"claude", "claude-rest.txt", ""},
		{"claude", "claude-after-escape.txt", ""},
		{"claude", "claude-question.txt", speechAsking},
		{"claude", "claude-plan.txt", speechAsking},
		{"claude", "claude-model.txt", speechAsking},
		{"claude", "claude-resume.txt", speechAsking},
		{"claude", "claude-resume-sessions.txt", speechAsking},
		{"claude", "claude-permission.txt", speechAsking},
		{"qwen", "styled/qwen/start.ans", ""}, // its placeholder is told from a draft by its colour
		{"qwen", "styled/qwen/idle.ans", ""},
		{"qwen", "styled/qwen/after-voice.ans", ""},
		{"qwen", "qwen-approval.txt", speechAsking},
		{"qwen", "qwen-question.txt", speechAsking},
		{"gemini", "gemini-start.txt", ""},
		{"gemini", "gemini-after-voice.txt", ""},
		{"gemini", "gemini-shell-approval.txt", speechAsking},
		{"gemini", "gemini-question.txt", speechAsking},
	} {
		raw, err := os.ReadFile(filepath.Join("testdata", "voice-screens", c.file))
		if err != nil {
			t.Fatal(err)
		}
		screen := strings.ReplaceAll(string(raw), "\n", "\r\n")
		a := gateAgentSized(t, c.prog, "working", screen, 120, 40)
		if got := a.speechBlocked(); got != c.want {
			t.Errorf("%s on %s: speechBlocked = %q, want %q", c.prog, c.file, got, c.want)
		}
	}
}

// A box mark is not a reason on its own: the agent's hook must have
// reported too (OpenCode without its plugin is not typed into).
func TestSpeechBoxNeedsAHookToo(t *testing.T) {
	const box = "\r\n  ┃  Ask anything\r\n  ┃\r\n  ╹▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀\r\n   enter send\r\n"
	a := gateAgent(t, "opencode", "done", box)
	a.speech.hooked = func() string { return "" }
	if got := a.speechBlocked(); got != speechNoHook {
		t.Fatalf("opencode at its box, no hook: speechBlocked = %q, want %q", got, speechNoHook)
	}
}

// Claude Code reports nothing when a prompt is dismissed with Esc: its last
// report stays "input" while it is back at its box (captured: the question
// picker, a tool permission and the plan approval, each Esc'd). For an
// agent whose box reminal knows, the box on screen with no prompt wording
// means the prompt is gone: typed. Where the box is not on screen, the
// "input" stands.
func TestSpeechStaleInputAtTheBoxIsTypedInto(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "voice-screens", "claude-after-escape.txt"))
	if err != nil {
		t.Fatal(err)
	}
	a := gateAgentSized(t, "claude", "input", strings.ReplaceAll(string(raw), "\n", "\r\n"), 120, 40)
	a.speech.hooked = func() string { return "input" }
	if got := a.speechBlocked(); got != "" {
		t.Fatalf("Esc'd prompt, hook still input, box on screen: speechBlocked = %q, want it allowed", got)
	}
	raw, err = os.ReadFile(filepath.Join("testdata", "voice-screens", "claude-question.txt"))
	if err != nil {
		t.Fatal(err)
	}
	b := gateAgentSized(t, "claude", "input", strings.ReplaceAll(string(raw), "\n", "\r\n"), 120, 40)
	b.speech.hooked = func() string { return "input" }
	if got := b.speechBlocked(); got != speechAsking {
		t.Fatalf("question picker up, hook input: speechBlocked = %q, want %q", got, speechAsking)
	}
}

// Claude Code's box with a draft in it: a draft that is a number is still
// the box; so is its shell mode ("!"). A numbered row with another under it
// is a list of choices, not the box. (A hand-typed draft that starts like
// "1. " is refused by the prompt wording list, which reads it as a choice.)
func TestSpeechClaudeBoxDraftsAndShellMode(t *testing.T) {
	rule := strings.Repeat("─", 60)
	box := func(row string) string {
		return "\r\n" + rule + "\r\n" + row + "\r\n" + rule + "\r\n  ? for shortcuts\r\n"
	}
	for _, c := range []struct{ name, screen, want string }{
		{"draft that is a number", box("❯ 42"), speechDraft}, // the box, with a draft in it
		{"empty, no-break space", box("❯ "), ""},
		{"a list of choices under a rule", "\r\n" + rule + "\r\n❯ 1. Yes\r\n  2. No\r\n" + rule + "\r\n", speechAsking},
	} {
		if got := gateAgent(t, "claude", "done", c.screen).speechBlocked(); got != c.want {
			t.Errorf("%s: speechBlocked = %q, want %q", c.name, got, c.want)
		}
	}
}

// The Claude Code fixtures are raw captures: its prompt sign is followed by
// a no-break space, which a fixture rebuilt by hand would lose (and with it
// what the box shape must accept).
func TestVoiceFixturesKeepClaudesNoBreakSpace(t *testing.T) {
	for _, f := range []string{"claude-fresh.txt", "claude-rest.txt", "claude-after-escape.txt"} {
		raw, err := os.ReadFile(filepath.Join("testdata", "voice-screens", f))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), "❯ ") {
			t.Errorf("%s has no \"❯\" followed by a no-break space: not a raw capture", f)
		}
	}
}

// Shell mode: Return runs the line as a command, no model in between. Claude
// Code, Qwen and Gemini draw "!" for it: never typed into, and said so.
func TestSpeechRefusesShellMode(t *testing.T) {
	for _, c := range []struct{ prog, screen string }{
		{"claude", "\r\n" + strings.Repeat("─", 60) + "\r\n! ls -la\r\n" + strings.Repeat("─", 60) + "\r\n  ! for bash mode\r\n"},
		{"qwen", "\r\n" + strings.Repeat("─", 60) + "\r\n!   Type your command\r\n" + strings.Repeat("─", 60) + "\r\n"},
		{"gemini", "\r\n" + strings.Repeat("▄", 60) + "\r\n ! Type your shell command\r\n" + strings.Repeat("▀", 60) + "\r\n"},
	} {
		if got := gateAgent(t, c.prog, "done", c.screen).speechBlocked(); got != speechShell {
			t.Errorf("%s in shell mode: speechBlocked = %q, want %q", c.prog, got, speechShell)
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
		{"draft that is a number", rule + "\n❯ 42\n" + rule + "\n  ? for shortcuts\n", true},
		{"draft 1. with nothing numbered below", rule + "\n❯ 1. buy milk\n" + rule + "\n  ? for shortcuts\n", true},
		{"draft 1. with a 3. below (not the next number)", rule + "\n❯ 1. buy milk\n" + rule + "\n3. Some note\n", true},
	} {
		if got := voiceBoxDrawn("claude", c.screen); got != c.box {
			t.Errorf("%s: voiceBoxDrawn = %v, want %v", c.name, got, c.box)
		}
	}
}

// The box is bracketed: an edge above the prompt row and one below it. Rows
// a dialog might draw under a rule pass for the box only if they are
// bracketed too and not a numbered list; each of these is refused. A box
// with a draft over several rows is still the box.
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
		{"a draft over three rows", rule + "\n❯ first line of a long\n  draft that runs on\n  for a third row\n" + rule + "\n  ? for shortcuts\n", true},
		{"an empty box", rule + "\n❯ \n" + rule + "\n  ? for shortcuts\n", true},
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
