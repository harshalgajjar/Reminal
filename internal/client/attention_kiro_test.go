// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import "testing"

// Kiro CLI 3.6 has no hooks: a tool waiting for approval is told from the
// screen alone, by the footer of its dialog and by its lettered chooser.
// A session stuck on one read as done.
func TestKirosApprovalPromptsAreAQuestion(t *testing.T) {
	for name, tail := range map[string]string{
		"a tool's dialog":            "  execute_bash requires approval\n\n  rm -rf build\n\n  y approve · n deny · t trust",
		"a tool's dialog with rules": "  fs_write requires approval\n\n  src/main.go\n\n  y allow once · n deny once · t create allow rule · d create deny rule",
		"the chooser, one pending":   "  (a) Approve once\n  (x) Stop task run",
		"the chooser, several":       "  (a) Approve all pending\n  (x) Stop task run",
		"the chooser's last row":     "  3 tools are waiting\n  (x) Stop task run",
	} {
		if !attnLooksLikePrompt(tail) {
			t.Errorf("%s was not read as a prompt: a session stuck on it reads as done", name)
		}
		if got := classifyAttnFor("kiro", true, tail, attnSettleMs+1, attnStartCap+1, true); got != "input" {
			t.Errorf("%s read as %q, want input", name, got)
		}
	}
	// The same words in what an agent wrote are not its prompt.
	for _, prose := range []string{
		"The tool requires approval, so I asked and you can approve, deny or trust it.",
		"Press y to approve it or n to deny it once the dialog shows.",
		"I would approve once and stop the task run if it asks again.",
		"Approve all pending changes in the review, then merge.",
		"Options: a) approve once b) deny c) stop the task run",
	} {
		if attnLooksLikePrompt(prose) {
			t.Errorf("prose read as a prompt: %q", prose)
		}
	}
}
