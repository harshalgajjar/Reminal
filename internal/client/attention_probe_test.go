package client

import "testing"

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
