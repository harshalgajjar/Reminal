// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"reminal/internal/session"
)

// useHelp makes each agent's flags what its real --help says (captured in
// testdata/help from the versions the harness rig runs), never asking a
// binary on the machine running the tests.
func useHelp(t *testing.T) {
	t.Helper()
	files := map[string]string{"claude": "claude", "codex": "codex-resume", "gemini": "gemini", "qwen": "qwen",
		"cursor-agent": "cursor-agent", "opencode": "opencode", "pi": "pi", "copilot": "copilot", "kilo": "kilo"}
	prev := flagSpecFor
	flagSpecFor = func(bin string) flagSpec {
		b, err := os.ReadFile("testdata/help/" + files[bin] + ".txt")
		if err != nil {
			return nil
		}
		return parseHelpFlags(string(b))
	}
	t.Cleanup(func() { flagSpecFor = prev })
}

func TestResumeArgv(t *testing.T) {
	useHelp(t)
	cases := []struct {
		name string
		r    session.Restore
		want []string
	}{
		{"claude by id keeps its flags, drops its prompt",
			session.Restore{Fg: "claude", FgArgs: []string{"node", "/opt/claude/cli.js", "--model", "opus", "--dangerously-skip-permissions", "fix the tests"}, Conv: "a1b2c3d4-e5f6"},
			[]string{"claude", "--model", "opus", "--dangerously-skip-permissions", "--resume", "a1b2c3d4-e5f6"}},
		{"claude without an id continues the latest",
			session.Restore{Fg: "claude", FgArgs: []string{"claude"}},
			[]string{"claude", "--continue"}},
		{"an old resume is replaced, not doubled",
			session.Restore{Fg: "claude", FgArgs: []string{"claude", "--resume", "old-conversation", "-c"}, Conv: "new-conversation"},
			[]string{"claude", "--resume", "new-conversation"}},
		{"print mode was never interactive",
			session.Restore{Fg: "claude", FgArgs: []string{"claude", "-p", "hi"}}, nil},
		{"codex by id",
			session.Restore{Fg: "codex", FgArgs: []string{"codex", "--full-auto"}, Conv: "0199aaaa-bbbb"},
			[]string{"codex", "resume", "0199aaaa-bbbb"}},
		{"codex exec is one-shot",
			session.Restore{Fg: "codex", FgArgs: []string{"codex", "exec", "do it"}}, nil},
		{"cursor-agent latest keeps -f",
			session.Restore{Fg: "cursor-agent", FgArgs: []string{"cursor-agent", "-f"}},
			[]string{"cursor-agent", "-f", "--continue"}},
		{"qwen by id",
			session.Restore{Fg: "qwen", FgArgs: []string{"node", "/usr/local/bin/qwen"}, Conv: "2e03a898-8ab6"},
			[]string{"qwen", "--resume", "2e03a898-8ab6"}},
		{"gemini resumes only by index or latest",
			session.Restore{Fg: "gemini", FgArgs: []string{"node", "/usr/local/bin/gemini", "--resume", "latest"}, Conv: "6b229456-6e4c"},
			[]string{"gemini", "--resume", "latest"}},
		{"pi keeps a provider given as a flag",
			session.Restore{Fg: "pi", FgArgs: []string{"pi", "--provider", "fake", "-e", "/x/p.ts"}},
			[]string{"pi", "--provider", "fake", "-e", "/x/p.ts", "--continue"}},
		{"not an agent", session.Restore{Fg: "vim", FgArgs: []string{"vim", "x"}}, nil},
	}
	for _, c := range cases {
		if got := resumeArgv(c.r); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestShellJoinQuotes(t *testing.T) {
	got := shellJoin([]string{"claude", "--append-system-prompt", "be brief; rm -rf /", "--model", "o'pus"})
	want := `claude --append-system-prompt 'be brief; rm -rf /' --model 'o'\''pus'`
	if got != want {
		t.Errorf("got %s\nwant %s", got, want)
	}
}

// Several sessions of one agent in one folder, all ended by one reboot: an
// exact id resumes exactly; "latest" would hand every one of them the same
// conversation, so the agent's own list is opened instead — or, with none,
// nothing is started and the note says how.
func TestResumePlanSharedFolder(t *testing.T) {
	useHelp(t)
	a := session.Restore{ID: "A", Fg: "claude", FgArgs: []string{"claude"}, Cwd: "/w", Conv: "11111111-aaaa"}
	b := session.Restore{ID: "B", Fg: "claude", FgArgs: []string{"claude"}, Cwd: "/w/", Conv: "22222222-bbbb"}
	c := session.Restore{ID: "C", Fg: "claude", FgArgs: []string{"claude", "--model", "opus"}, Cwd: "/w"} // never reported an id
	all := []session.Restore{a, b, c}

	for _, x := range []session.Restore{a, b} {
		argv, note := resumePlan(x, all)
		if want := []string{"claude", "--resume", x.Conv}; !reflect.DeepEqual(argv, want) || note != "" {
			t.Errorf("%s: got %q %q, want its own conversation %q", x.ID, argv, note, want)
		}
	}
	if argv, note := resumePlan(c, all); !reflect.DeepEqual(argv, []string{"claude", "--model", "opus", "--resume"}) || note == "" {
		t.Errorf("C (no id, folder shared): got %q %q, want the picker and a note", argv, note)
	}
	// Alone in its folder, "latest" is exact.
	if argv, note := resumePlan(c, []session.Restore{c, {ID: "D", Fg: "claude", Cwd: "/elsewhere"}, {ID: "E", Fg: "codex", Cwd: "/w"}}); !reflect.DeepEqual(argv, []string{"claude", "--model", "opus", "--continue"}) || note != "" {
		t.Errorf("C alone: got %q %q, want --continue", argv, note)
	}

	x := session.Restore{ID: "X", Fg: "codex", FgArgs: []string{"codex"}, Cwd: "/w"}
	y := session.Restore{ID: "Y", Fg: "codex", FgArgs: []string{"codex"}, Cwd: "/w"}
	if argv, _ := resumePlan(x, []session.Restore{x, y}); !reflect.DeepEqual(argv, []string{"codex", "resume"}) {
		t.Errorf("codex shared: got %q, want its picker", argv)
	}
	g := session.Restore{ID: "G", Fg: "gemini", FgArgs: []string{"gemini"}, Cwd: "/w"}
	h := session.Restore{ID: "H", Fg: "gemini", FgArgs: []string{"gemini"}, Cwd: "/w"}
	if argv, note := resumePlan(g, []session.Restore{g, h}); argv != nil || !strings.Contains(note, "--list-sessions") {
		t.Errorf("gemini shared: got %q %q, want nothing started and how to find it", argv, note)
	}
}

// cursor-agent is a launcher for node: its own flags begin after the script
// that names it, not after the launcher — --use-system-ca is node's.
func TestResumeFlagsPastInterpreter(t *testing.T) {
	useHelp(t)
	dir := t.TempDir() + "/cursor-agent/versions/2026.09.26"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := dir + "/index.js"
	if err := os.WriteFile(script, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	r := session.Restore{ID: "B", Fg: "cursor-agent", Cwd: "/w",
		FgArgs: []string{"/root/.local/bin/cursor-agent", "--use-system-ca", script, "-f", "--model", "gpt"}}
	peer := session.Restore{ID: "C", Fg: "cursor-agent", Cwd: "/w"}
	if argv, _ := resumePlan(r, []session.Restore{r, peer}); !reflect.DeepEqual(argv, []string{"cursor-agent", "-f", "--model", "gpt", "--resume"}) {
		t.Errorf("got %q", argv)
	}
	// A directory that happens to carry the name is not the program.
	c := session.Restore{Fg: "claude", FgArgs: []string{"claude", "--add-dir", t.TempDir() + "/claude-notes", "--model", "opus"}}
	if argv := resumeArgv(c); !reflect.DeepEqual(argv, []string{"claude", "--add-dir", c.FgArgs[2], "--model", "opus", "--continue"}) {
		t.Errorf("claude: got %q", argv)
	}
}

// The flags a person started an agent with come back with it — read against
// each agent's real --help: its permission and model flags with their
// values, never a prompt, never a flag its resume would refuse.
func TestResumeKeepsStartFlags(t *testing.T) {
	useHelp(t)
	cases := []struct {
		name string
		r    session.Restore
		want []string
	}{
		{"claude",
			session.Restore{Fg: "claude", Conv: "c1c1c1c1-0000", FgArgs: []string{"claude", "--dangerously-skip-permissions", "--model", "opus", "--permission-mode", "plan", "--add-dir", "/tmp/x", "fix the tests"}},
			[]string{"claude", "--dangerously-skip-permissions", "--model", "opus", "--permission-mode", "plan", "--add-dir", "/tmp/x", "--resume", "c1c1c1c1-0000"}},
		{"claude, a flag its help does not list, with a value",
			session.Restore{Fg: "claude", FgArgs: []string{"claude", "--made-up-flag", "surely-not-a-prompt", "--model=opus"}},
			[]string{"claude", "--model=opus", "--continue"}},
		{"codex: what its resume takes, not --full-auto",
			session.Restore{Fg: "codex", FgArgs: []string{"codex", "--dangerously-bypass-approvals-and-sandbox", "-m", "gpt-5", "-c", "model_reasoning_effort=high", "-p", "work", "--full-auto", "do the thing"}},
			[]string{"codex", "resume", "--dangerously-bypass-approvals-and-sandbox", "-m", "gpt-5", "-c", "model_reasoning_effort=high", "-p", "work", "--last"}},
		{"codex -p is a profile, not a one-shot",
			session.Restore{Fg: "codex", Conv: "0199-aaaa-bbbb", FgArgs: []string{"codex", "-p", "work"}},
			[]string{"codex", "resume", "-p", "work", "0199-aaaa-bbbb"}},
		{"gemini --yolo, -i's prompt not sent again",
			session.Restore{Fg: "gemini", FgArgs: []string{"node", "/usr/local/bin/gemini", "--yolo", "-m", "gemini-2.5-pro", "-i", "start on the tests"}},
			[]string{"gemini", "--yolo", "-m", "gemini-2.5-pro", "--resume", "latest"}},
		{"qwen --approval-mode yolo",
			session.Restore{Fg: "qwen", Conv: "q1q1q1q1-0000", FgArgs: []string{"node", "/usr/local/bin/qwen", "--approval-mode", "yolo"}},
			[]string{"qwen", "--approval-mode", "yolo", "--resume", "q1q1q1q1-0000"}},
		{"cursor-agent -f and a model",
			session.Restore{Fg: "cursor-agent", FgArgs: []string{"cursor-agent", "-f", "--model", "sonnet-4", "write docs"}},
			[]string{"cursor-agent", "-f", "--model", "sonnet-4", "--continue"}},
		{"opencode's model",
			session.Restore{Fg: "opencode", FgArgs: []string{"opencode", "-m", "anthropic/claude", "--agent", "build"}},
			[]string{"opencode", "-m", "anthropic/claude", "--agent", "build", "--continue"}},
		{"pi's provider",
			session.Restore{Fg: "pi", FgArgs: []string{"pi", "--provider", "anthropic", "--model", "opus"}},
			[]string{"pi", "--provider", "anthropic", "--model", "opus", "--continue"}},
		{"a flag after -- is part of a prompt",
			session.Restore{Fg: "claude", FgArgs: []string{"claude", "--model", "opus", "--", "--dangerously-skip-permissions"}},
			[]string{"claude", "--model", "opus", "--continue"}},
	}
	for _, c := range cases {
		if got := resumeArgv(c.r); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

// Every flag the real helps list comes out with the right arity.
func TestParseHelpFlags(t *testing.T) {
	useHelp(t)
	want := map[string]map[string]bool{
		"claude":       {"--dangerously-skip-permissions": false, "--model": true, "-p": false, "--print": false, "--resume": true, "--add-dir": true},
		"codex":        {"--dangerously-bypass-approvals-and-sandbox": false, "-m": true, "--model": true, "-c": true, "-p": true, "-s": true, "--last": false},
		"gemini":       {"-y": false, "--yolo": false, "-m": true, "--approval-mode": true, "-r": true},
		"qwen":         {"-y": false, "--yolo": false, "--approval-mode": true, "-c": false, "-r": true},
		"cursor-agent": {"-f": false, "--force": false, "--model": true, "--resume": true, "--continue": false},
		"opencode":     {"-m": true, "--model": true, "-c": false, "--continue": false},
		"pi":           {"--provider": true, "--model": true, "-p": false, "--print": false, "-c": false},
	}
	for bin, flags := range want {
		spec := flagSpecFor(bin)
		for f, takes := range flags {
			got, ok := spec[f]
			if !ok {
				t.Errorf("%s: %s not found in its help", bin, f)
			} else if got != takes {
				t.Errorf("%s: %s takes a value = %v, want %v", bin, f, got, takes)
			}
		}
	}
}

// The resume is typed into the session's own shell, so it is quoted for it.
func TestShellCommandPerShell(t *testing.T) {
	argv := []string{"claude", "--append-system-prompt", "it's brief; no $HOME", "--resume", "c1-c1"}
	for shell, want := range map[string]string{
		"/bin/zsh":                               `claude --append-system-prompt 'it'\''s brief; no $HOME' --resume c1-c1`,
		`C:\Program Files\PowerShell\7\pwsh.exe`: `claude --append-system-prompt 'it''s brief; no $HOME' --resume c1-c1`,
		`C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`: `claude --append-system-prompt 'it''s brief; no $HOME' --resume c1-c1`,
		`C:\Windows\System32\cmd.exe`:                               `claude --append-system-prompt "it's brief; no $HOME" --resume c1-c1`,
	} {
		if got := shellCommand(argv, shell); got != want {
			t.Errorf("%s:\n got %s\nwant %s", shell, got, want)
		}
	}
}

// On Windows an npm-installed agent runs as node with its package's script —
// found by the package, whichever separator the path uses.
func TestProgramFromWindowsNpmArgs(t *testing.T) {
	for want, args := range map[string][]string{
		"qwen":   {`C:\rtest\node\node.exe`, `C:\rtest\npm/node_modules/@qwen-code/qwen-code/cli-entry.js`},
		"pi":     {`C:\rtest\node\node.exe`, `C:\rtest\npm/node_modules/@earendil-works/pi-coding-agent/dist/bundle/cli.js`},
		"claude": {`C:\Users\x\AppData\Roaming\npm\node_modules\@anthropic-ai\claude-code\bin\claude.exe`},
		"gemini": {`C:\rtest\node\node.exe`, `C:\rtest\npm\node_modules\@google\gemini-cli\bundle\gemini.js`},
		"codex":  {`C:\rtest\node\node.exe`, `C:\rtest\npm\node_modules\@openai\codex\bin\codex.js`},
	} {
		if got := programFromArgs(args, "node"); got != want {
			t.Errorf("%q: got %q, want %q", args, got, want)
		}
	}
	// pi is still pi only where it cannot mean a file called pi.
	if got := programFromArgs([]string{"python3", "/home/x/pi/pi.py"}, "python3"); got == "pi" {
		t.Errorf("a script in a folder called pi was taken for pi")
	}
}

// A record that names an agent never comes back silent: when nothing can be
// started, a line says why. A record naming none says nothing.
func TestResumePlanSaysWhyNothingIsStarted(t *testing.T) {
	useHelp(t)
	for _, r := range []session.Restore{
		{ID: "P", Fg: "claude", FgArgs: []string{"claude", "-p", "summarize the repo"}, Cwd: "/w"}, // a one-shot run
		{ID: "U", Fg: "someagent", FgArgs: []string{"someagent"}, Cwd: "/w"},                       // one reminal cannot resume
	} {
		argv, note := resumePlan(r, []session.Restore{r})
		if argv != nil || note == "" || !strings.Contains(note, r.Fg) {
			t.Errorf("%s (%s): got %q %q, want nothing started and a line naming %s and why", r.ID, r.FgArgs, argv, note, r.Fg)
		}
	}
	none := session.Restore{ID: "N", Cwd: "/w"}
	if argv, note := resumePlan(none, []session.Restore{none}); argv != nil || note != "" {
		t.Errorf("no agent: got %q %q, want nothing at all", argv, note)
	}
}
