// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reminal/internal/session"
)

// A session whose record lost its agent, while its conversation is still
// named, comes back saying so, with the command that resumes it, ready to
// paste: in the folder the conversation ran in, never typed, never one another
// session also had, and nothing when no agent here has the conversation.
func TestARestoreWhoseRecordLostItsAgentSaysHowToResumeIt(t *testing.T) {
	isolateHome(t)
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	project := t.TempDir() // where the conversation ran
	home := t.TempDir()    // where the session's shell is
	const conv = "a1b2c3d4-0001-4e5f-8a9b-0123456789ab"
	writeConvFile := func(cwd string) {
		t.Helper()
		dir := filepath.Join(cfg, "projects", "-somewhere")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		line := `{"type":"user","cwd":"` + cwd + `","sessionId":"` + conv + `"}` + "\n"
		if err := os.WriteFile(filepath.Join(dir, conv+".jsonl"), []byte(line), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	lost := session.Restore{ID: "LOST0001", Cwd: home} // Fg gone
	if err := session.WriteConv(lost.ID, conv); err != nil {
		t.Fatal(err)
	}

	if n := lostAgentNote(lost, nil, "/bin/zsh"); n != "" {
		t.Errorf("no agent here has the conversation, and still: %q", n)
	}

	writeConvFile(project)
	n := lostAgentNote(lost, nil, "/bin/zsh")
	if !strings.Contains(n, "claude --resume "+conv) || !strings.Contains(n, "cd "+project+" && ") {
		t.Errorf("note %q: want the resume, in the folder the conversation ran in", n)
	}

	here := lost
	here.Cwd = project
	if n := lostAgentNote(here, nil, "/bin/zsh"); strings.Contains(n, "cd ") || !strings.Contains(n, "claude --resume "+conv) {
		t.Errorf("note %q: in the conversation's own folder, no cd", n)
	}

	other := session.Restore{ID: "OTHER001", Fg: "claude", Conv: conv}
	if n := lostAgentNote(lost, []session.Restore{other}, "/bin/zsh"); strings.Contains(n, "--resume") || !strings.Contains(n, "another session") {
		t.Errorf("note %q: a conversation another session had is said, never offered", n)
	}

	named := lost
	named.Fg = "claude"
	if n := lostAgentNote(named, nil, "/bin/zsh"); n != "" {
		t.Errorf("a record that names its agent got the lost note: %q", n)
	}

	writeConvFile(project + "\x1b]0;x\a")
	if n := lostAgentNote(lost, nil, "/bin/zsh"); strings.ContainsAny(n, "\x1b\a") {
		t.Errorf("a folder with control characters reached the screen: %q", n)
	}
}
