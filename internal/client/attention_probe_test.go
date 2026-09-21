package client

import (
	"testing"
	"time"

	"github.com/reminal/reminal/internal/session"
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
			got, _ := resolveAttn(c.screen, c.hs, c.lastOutput)
			if got != c.want {
				t.Errorf("resolveAttn(screen=%q) = %q, want %q", c.screen, got, c.want)
			}
		})
	}
}
