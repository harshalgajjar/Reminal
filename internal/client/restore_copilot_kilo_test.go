// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"reminal/internal/session"
)

// Copilot CLI and Kilo Code CLI come back on their own conversation: by the
// id their hook said, with --resume=<id> (Copilot 1.0.106's help writes it
// joined: its value is optional) and --session <id> (Kilo 7.8.27, as
// OpenCode); else their latest (--continue). Their flags stay, a prompt
// does not, and a run that was one-shot is not resumed.
func TestCopilotAndKiloResume(t *testing.T) {
	useHelp(t)
	for _, c := range []struct {
		name string
		r    session.Restore
		want []string
	}{
		{"copilot by id keeps its flags, drops its prompt",
			session.Restore{Fg: "copilot", FgArgs: []string{"node", "/usr/local/bin/copilot", "--model", "gpt-5", "--allow-all-tools", "-i", "fix the tests"}, Conv: "0cb916db-26aa-40f2-86b5-1ba81b225fd2"},
			[]string{"copilot", "--model", "gpt-5", "--allow-all-tools", "--resume=0cb916db-26aa-40f2-86b5-1ba81b225fd2"}},
		{"copilot without an id continues the latest",
			session.Restore{Fg: "copilot", FgArgs: []string{"copilot"}},
			[]string{"copilot", "--continue"}},
		{"copilot: an old resume is replaced, not doubled",
			session.Restore{Fg: "copilot", FgArgs: []string{"copilot", "--resume", "old-one"}, Conv: "new-one-0000"},
			[]string{"copilot", "--resume=new-one-0000"}},
		{"copilot -p is one-shot",
			session.Restore{Fg: "copilot", FgArgs: []string{"copilot", "-p", "hi"}}, nil},
		{"copilot mcp is not a conversation",
			session.Restore{Fg: "copilot", FgArgs: []string{"copilot", "mcp", "list"}}, nil},
		{"kilo by id keeps its model",
			session.Restore{Fg: "kilo", FgArgs: []string{"node", "/usr/local/bin/kilo", "-m", "standin/gpt-4.1"}, Conv: "ses_e4d1c0adbffeque2XLUh0Km8mK"},
			[]string{"kilo", "-m", "standin/gpt-4.1", "--session", "ses_e4d1c0adbffeque2XLUh0Km8mK"}},
		{"kilo without an id continues the latest",
			session.Restore{Fg: "kilo", FgArgs: []string{"kilo", "-c"}},
			[]string{"kilo", "--continue"}},
		{"kilo run is one-shot",
			session.Restore{Fg: "kilo", FgArgs: []string{"kilo", "run", "hello"}}, nil},
	} {
		if got := resumeArgv(c.r); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
	for _, c := range []struct {
		prog string
		args []string
		into bool
	}{
		{"copilot", []string{"copilot", "--continue"}, true},
		{"copilot", []string{"copilot", "--resume=abc"}, true},
		{"copilot", []string{"copilot", "--model", "gpt-5"}, false},
		{"kilo", []string{"kilo", "-s", "ses_x"}, true},
		{"kilo", []string{"kilo"}, false},
	} {
		if got := startedIntoConversation(c.prog, c.args); got != c.into {
			t.Errorf("%s %v: into a conversation %v, want %v", c.prog, c.args, got, c.into)
		}
	}
	for _, a := range []string{"copilot", "kilo"} {
		if !sleepsExactly(a) {
			t.Errorf("%s does not come back on exactly its own conversation", a)
		}
	}
}

// Copilot keeps a conversation in ~/.copilot/session-state/<id>/
// events.jsonl (its hooks' transcriptPath): there when that file is. One
// not there is not told from one out of sight: its folder is the
// conversation's own (globRoot), so gone reads as not known, which keeps a
// session from sleeping on it all the same.
func TestCopilotsConversationIsItsFile(t *testing.T) {
	home := isolateHome(t)
	conv := "0cb916db-26aa-40f2-86b5-1ba81b225fd2"
	if there, _ := convThere("copilot", conv, home); there {
		t.Fatalf("before it is written: there %v", there)
	}
	dir := filepath.Join(home, ".copilot", "session-state", conv)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if there, known := convThere("copilot", conv, home); !there || !known {
		t.Fatalf("after: there %v known %v", there, known)
	}
}

// Kilo keeps its conversations where no file names them; it is asked, as
// OpenCode is (`kilo session list --format json`, an id each). It prints
// nothing when it has none: none there, and that is known.
func TestKilosConversationIsInItsSessionList(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for kilo")
	}
	home := isolateHome(t)
	bin := t.TempDir()
	list := filepath.Join(t.TempDir(), "list.json")
	script := "#!/bin/sh\n[ \"$*\" = \"session list --format json\" ] || exit 2\ncat " + list + "\n"
	if err := os.WriteFile(filepath.Join(bin, "kilo"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, c := range []struct {
		said        string
		there, know bool
	}{
		{`[{"id":"ses_e4d1c0adbffeque2XLUh0Km8mK","title":"New session"}]`, true, true},
		{`[{"id":"ses_another0000000000000000"}]`, false, true},
		{"", false, true},
	} {
		if err := os.WriteFile(list, []byte(c.said), 0o600); err != nil {
			t.Fatal(err)
		}
		if there, known := convThere("kilo", "ses_e4d1c0adbffeque2XLUh0Km8mK", home); there != c.there || known != c.know {
			t.Errorf("%q: there %v known %v, want %v %v", c.said, there, known, c.there, c.know)
		}
	}
}
