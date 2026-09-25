package client

import (
	"testing"
	"time"

	"reminal/internal/session"
)

func TestClassifyAttn(t *testing.T) {
	tests := []struct {
		name        string
		agentActive bool
		tail        string
		settledMs   int64
		want        string
	}{
		{"no foreground program is no badge", false, "harshal@box %", 5000, ""},
		{"active + churning is working", true, "Thinking… (12s · esc to interrupt)", 200, "working"},
		{"active + settled on approval is input", true, "Do you want to proceed?\n❯ 1. Yes\n  2. No", 3000, "input"},
		{"active + settled y/n is input", true, "Run this command? (y/n)", 2500, "input"},
		{"active + settled no prompt is done", true, "> \n? for shortcuts", 4000, "done"},
		{"active + settled yes/no is input", true, "Apply edit to main.go? (yes/no)", 2000, "input"},
		{"working overrides prompt text while churning", true, "Do you want to proceed?", 300, "working"},
		// Claude Code's AskUserQuestion chooser — a settled option list is a
		// prompt even though Claude fired Stop (→ done) to yield for it.
		{"AskUserQuestion chooser is input", true, "❯ 1. Build the detector\n  2. Verify only\nEnter to select · ↑/↓ to navigate · Esc to cancel", 3000, "input"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyAttn(tt.agentActive, tt.tail, tt.settledMs); got != tt.want {
				t.Errorf("classifyAttn() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAttentionProbeTail(t *testing.T) {
	tests := []struct {
		name   string
		render string
		n      int
		want   string
	}{
		{
			name:   "drops trailing blank viewport rows and right-trims",
			render: "one   \ntwo\n\n\n",
			n:      5,
			want:   "one\ntwo",
		},
		{
			name:   "keeps only the last n content rows",
			render: "a\nb\nc\nd\n",
			n:      2,
			want:   "c\nd",
		},
		{
			name:   "blank lines between content are preserved",
			render: "prompt>\n\ntyping\n",
			n:      5,
			want:   "prompt>\n\ntyping",
		},
		{
			name:   "all blank yields empty",
			render: "\n\n\n",
			n:      3,
			want:   "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := attentionProbeTail(tt.render, tt.n); got != tt.want {
				t.Errorf("attentionProbeTail() = %q, want %q", got, tt.want)
			}
		})
	}
}

// A harness with no lifecycle hooks gives us nothing but the screen, so every
// shape of approval prompt it can show has to be recognised here. These are
// transcribed from real sessions; a stuck session that reads as "done" is one
// nobody comes to help.
func TestPromptCuesCoverEachHarness(t *testing.T) {
	cursorApproval := `$  ls -la /home/parallels/work/ && echo "no backend dir" in .

Run this command?
Not in allowlist: echo
→ Run (once) (y)
  Add Shell(echo) to allowlist? (tab)
  Run Everything (shift+tab)
  Skip & tell the agent what to do instead (esc or n)`

	claudeApproval := `Do you want to proceed?
❯ 1. Yes
  2. No, and tell Claude what to do differently (esc)`

	for name, tail := range map[string]string{
		"cursor-agent approval": cursorApproval,
		"claude approval":       claudeApproval,
	} {
		if !attnLooksLikePrompt(tail) {
			t.Errorf("%s was not recognised as a prompt — a session stuck on it reads as done", name)
		}
		if got := classifyAttn(true, tail, attnSettleMs+1); got != "input" {
			t.Errorf("%s classified as %q, want input", name, got)
		}
	}

	// The working spinner must NOT read as a prompt, or every busy agent would
	// look like it needs you.
	working := `· Booping… (2s · esc to interrupt)`
	if attnLooksLikePrompt(working) {
		t.Error("the working spinner was read as a prompt")
	}
	// Ordinary output that merely mentions running something is not a prompt.
	if attnLooksLikePrompt("I will run this command in a moment and report back") {
		t.Error("prose about running a command was read as a prompt")
	}
}

// Prose ABOUT approvals is not a request for one. Agents talk about approving,
// confirming and permissions constantly, and each false positive made a
// finished session read as "needs you".
func TestProseAboutApprovalIsNotAPrompt(t *testing.T) {
	prose := []string{
		"Acknowledged — no action needed, this just confirms frontend has everything they need.",
		"Approve only what the task you delegated plainly requires; anything destructive, escalate.",
		"I approved the read-only lookup; frontend should continue on its own now.",
		"It asked for permission earlier and lead granted it.",
		"Backend is built and both obligations are closed.",
	}
	for _, line := range prose {
		if attnLooksLikePrompt(line) {
			t.Errorf("prose read as a prompt: %q", line)
		}
	}
	// The real things must still register.
	for _, p := range []string{
		"Claude needs your permission to use Bash",
		"Do you want to proceed?\n❯ 1. Yes",
		"Run this command?\n→ Run (once) (y)",
		"Confirm? (y/n)",
	} {
		if !attnLooksLikePrompt(p) {
			t.Errorf("a real prompt was not recognised: %q", p)
		}
	}
}

// cursor-agent's yolo mode prints "Run Everything" in its footer on every
// screen; that is a mode label, not a question.
func TestYoloFooterIsNotAPrompt(t *testing.T) {
	if attnLooksLikePrompt("  Auto                    Run Everything\n  ~/work/frontend") {
		t.Fatal("the mode label read as a prompt")
	}
	if !attnLooksLikePrompt("Run this command?\n→ Run (once)  (y)") {
		t.Fatal("the real approval prompt must still be recognised")
	}
}

// The kernel's name for a process is often not the program: Node renames its
// main thread, so cursor-agent is "MainThread", and the command line is what
// says which agent it is.
func TestProgramFromArgsFindsTheAgentBehindTheName(t *testing.T) {
	cases := []struct {
		args []string
		comm string
		want string
	}{
		{[]string{"/home/u/.local/bin/cursor-agent", "--use-system-ca", "/home/u/.local/share/cursor-agent/versions/1/index.js", "-f"}, "MainThread", "cursor-agent"},
		{[]string{"node", "/usr/lib/node_modules/@google/gemini/bin/gemini.js"}, "node", "gemini"},
		{[]string{"python3", "ingest.py"}, "python3", ""},
		{[]string{"/opt/tool/bin/thing", "serve"}, "MainThread", "thing"},
	}
	for _, c := range cases {
		if got := programFromArgs(c.args, c.comm); got != c.want {
			t.Errorf("programFromArgs(%v, %q) = %q, want %q", c.args, c.comm, got, c.want)
		}
	}
}

// Every agent `reminal integrate` sets up has to be one reminal can recognise
// on sight. Being on this list is what makes reminal read the session's screen
// instead of the stream it was painted with (programScreenText), and an agent
// that draws on the main screen — pi's default, and no alternate screen to give
// it away — reads as a plain terminal until it is named here. That failure is
// quiet: `read_transcript` still answers, just with cursor-move debris instead
// of what is on the screen.
func TestEveryIntegratedAgentIsRecognisedOnSight(t *testing.T) {
	// The binaries cmd/reminal/integrate.go looks for.
	for _, bin := range []string{"claude", "codex", "agy", "opencode", "cursor-agent", "gemini", "qwen", "amp", "pi"} {
		if !isAgentProgram(bin) {
			t.Errorf("%s is integrated but not a known agent program: its screen will not be read", bin)
		}
	}
}

// Claude Code draws the spaces between words as cursor moves, so its dialogs
// render with none; they are prompts all the same.
func TestPromptWithoutSpacesIsAPrompt(t *testing.T) {
	trust := "ClaudeCode'llbeabletoread,edit,andexecutefileshere.\nSecurityguide\n" +
		"❯No,exit\nYes,Itrustthisfolder\nEntertoconfirm·Esctocancel"
	if !attnLooksLikePrompt(trust) {
		t.Fatal("Claude Code's trust dialog, drawn without spaces, was not seen as a prompt")
	}
	styled := "   Yes, I trust this folder\n\n \x1b[38;5;246mEnter\x1b[m \x1b[38;5;246mto\x1b[m \x1b[38;5;246mconfirm\x1b[m " +
		"\x1b[38;5;246m·\x1b[m \x1b[38;5;246mEsc\x1b[m \x1b[38;5;246mto\x1b[m \x1b[38;5;246mcancel\x1b[m"
	if !attnLooksLikePrompt(styled) {
		t.Fatal("Claude Code's trust dialog, as rendered with its styling, was not seen as a prompt")
	}
	if attnLooksLikePrompt("Donethechangesareinandthetestspass.\n❯\n────\n⏵⏵bypasspermissionson") {
		t.Fatal("a finished turn drawn without spaces read as a prompt")
	}
}

func TestResolveAttn(t *testing.T) {
	now := time.Now()
	hook := func(state string, agoSec int) *session.HookState {
		return &session.HookState{State: state, TS: now.Add(-time.Duration(agoSec) * time.Second)}
	}
	cases := []struct {
		name       string
		screen     string
		hs         *session.HookState
		lastOutput time.Time
		want       string
	}{
		// No hook at all: the screen is the only word.
		{"no hook falls back to screen", "working", nil, now, "working"},
		{"no hook, bare shell", "", nil, now, ""},

		// A fresh hook is authoritative.
		{"fresh working hook", "done", hook("working", 1), now, "working"},
		{"fresh input hook is honoured", "done", hook("input", 1), now.Add(-30 * time.Second), "input"},

		// THE BUG: answering an in-chat question resumes the turn with no hook
		// event, so "input" goes stale while the agent works. Output long after
		// the hook disproves the "stopped" claim.
		{"stale input + output after = resumed (screen knows)", "working", hook("input", 300), now, "working"},
		{"stale input + output after, screen blind (Windows)", "", hook("input", 300), now, "working"},
		{"stale done + output after = resumed", "working", hook("done", 300), now, "working"},

		// A genuinely waiting agent emits nothing, so lastActivity stays behind
		// the hook and "needs you" must survive — however long you take.
		{"waiting agent keeps needs-you (no output since)", "done", hook("input", 600), now.Add(-601 * time.Second), "input"},
		{"idle done stays done", "done", hook("done", 600), now.Add(-601 * time.Second), "done"},

		// Output inside the grace is the prompt being painted, not a resume.
		{"output within grace is not a resume", "done", hook("input", 2), now.Add(-1 * time.Second), "input"},

		// A visible prompt still corrects a "done" hook (the question case).
		{"screen prompt beats done hook", "input", hook("done", 1), now.Add(-30 * time.Second), "input"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _ := resolveAttn(c.screen, c.hs, c.lastOutput, time.Time{}, 0, c.screen == "input")
			if got != c.want {
				t.Errorf("resolveAttn(screen=%q) = %q, want %q", c.screen, got, c.want)
			}
		})
	}
}

// The three ways a hook is not to be believed, each seen on a real session:
// written by a harness that has since exited, "working" over a screen that has
// not moved for a minute and a half, and "done" under a chooser drawn at the
// bottom of the screen.
func TestResolveAttnDisbelievesAHookWhenTheScreenKnowsBetter(t *testing.T) {
	now := time.Now()
	hook := func(state string, ago time.Duration) *session.HookState {
		return &session.HookState{State: state, TS: now.Add(-ago)}
	}
	cases := []struct {
		name   string
		screen string
		hs     *session.HookState
		fgAt   time.Time
		idleMs int64
		prompt bool
		want   string
	}{
		// A "working" left by a turn that died; python3 started after it.
		{"hook older than the foreground is ignored", "done", hook("working", 40*time.Second), now.Add(-12 * time.Second), 0, false, "done"},
		{"hook newer than the foreground is believed", "done", hook("working", 0), now.Add(-12 * time.Second), 0, false, "working"},
		// A working harness animates; this one has not drawn anything.
		{"silent working falls back to the screen", "done", hook("working", 0), time.Time{}, hookWorkingSilentMs + 1, false, "done"},
		{"a working harness that just drew is believed", "done", hook("working", 0), time.Time{}, 1000, false, "working"},
		// Claude Code fires Stop when it yields for a question.
		{"a chooser at the bottom beats done", "input", hook("done", time.Second), time.Time{}, 0, true, "input"},
		{"a question only in the transcript above does not", "input", hook("done", time.Second), time.Time{}, 0, false, "done"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _ := resolveAttn(c.screen, c.hs, now.Add(-30*time.Second), c.fgAt, c.idleMs, c.prompt)
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// The screen is read as TEXT. A harness that colours its dialog, or draws the
// spaces between words as cursor moves, is still asking you something — and a
// session sitting on a question that reads as "done" is the whole problem the
// needs-you state exists to solve.
func TestPromptsAreSeenThroughStylingAndSpacing(t *testing.T) {
	styled := "   Yes, I trust this folder\n\n \x1b[38;5;246mEnter\x1b[m \x1b[38;5;246mto\x1b[m " +
		"\x1b[38;5;246mconfirm\x1b[m \x1b[38;5;246m·\x1b[m \x1b[38;5;246mEsc\x1b[m \x1b[38;5;246mto\x1b[m \x1b[38;5;246mcancel\x1b[m"
	if !attnLooksLikePrompt(styled) {
		t.Error("a dialog drawn in colour was not seen as a prompt")
	}
	spaceless := "ClaudeCode'llbeabletoread,edit,andexecutefileshere.\n❯No,exit\nYes,Itrustthisfolder\nEntertoconfirm·Esctocancel"
	if !attnLooksLikePrompt(spaceless) {
		t.Error("a dialog drawn without spaces was not seen as a prompt")
	}
	// cursor-agent has no lifecycle hooks, so its approval is only ever seen
	// on the screen.
	cursorApproval := "Run this command?\nNot in allowlist: echo\n→ Run (once) (y)\n  Skip & tell the agent what to do instead (esc or n)"
	if !attnLooksLikePrompt(cursorApproval) {
		t.Error("cursor-agent's approval was not seen as a prompt")
	}
	if got := classifyAttn(true, cursorApproval, attnSettleMs+1); got != "input" {
		t.Errorf("cursor-agent's approval classified as %q, want input", got)
	}
	for _, notAPrompt := range []string{
		"· Booping… (2s · esc to interrupt)",
		"I will run this command in a moment and report back",
		"this just confirms the build is green; no approval needed",
		"Donethechangesareinandthetestspass.\n❯\n────\n⏵⏵bypasspermissionson",
	} {
		if attnLooksLikePrompt(notAPrompt) {
			t.Errorf("read as a prompt: %q", notAPrompt)
		}
	}
}
