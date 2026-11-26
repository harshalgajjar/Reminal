// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// before is spec as a reminal that did not know event had it.
func specBefore(spec hookSpec, event string) hookSpec {
	out := spec
	out.events = nil
	for _, ev := range spec.events {
		if ev.Event != event {
			out.events = append(out.events, ev)
		}
	}
	return out
}

func readSettings(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	root := map[string]any{}
	if err := json.Unmarshal(b, &root); err != nil {
		t.Fatal(err)
	}
	return root
}

// An install made before reminal knew an event gets its hook as the upgraded
// reminal starts, with no integrate run: the entry calls the reminal the
// others call, quoted as they are; the person's own hooks, an event of
// theirs alone and every other setting are as they were; and a second run
// writes nothing.
func TestAnUpgradeAddsTheHookItBrought(t *testing.T) {
	for _, c := range []struct{ bin, version, file string }{
		{"qwen", "0.26.9", ".qwen/settings.json"},
		{"claude", "2.1.304 (Claude Code)", ".claude/settings.json"},
	} {
		for _, exe := range []string{"/usr/local/bin/reminal", "/Users/some one/Applications/reminal.app/Contents/MacOS/reminal"} {
			home := t.TempDir()
			path := filepath.Join(home, c.file)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			mine := `{"theme": "dark", "hooks": {"Stop": [{"hooks": [{"type": "command", "command": "say done"}]}], "PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "/home/me/bin/guard.sh", "timeout": 5}]}]}, "permissions": {"allow": ["Bash(ls:*)"]}}`
			if err := os.WriteFile(path, []byte(mine), 0o600); err != nil {
				t.Fatal(err)
			}
			now := *targetNamed(t, c.bin).hooks
			old := specBefore(now, "StopFailure")
			old.version = c.version
			if err := applyHooks(&old, home, exe, false); err != nil {
				t.Fatal(err)
			}
			was := readSettings(t, path)
			version := func(bin string) string {
				if bin != c.bin {
					t.Errorf("asked the version of %s, which is not set up", bin)
				}
				return c.version
			}
			said := healAddedHooks(home, version)
			if len(said) != 1 || !strings.Contains(said[0], "StopFailure") {
				t.Fatalf("%s: said %q, want one line naming StopFailure", c.bin, said)
			}
			got := readSettings(t, path)
			hooks := got["hooks"].(map[string]any)
			added, _ := json.Marshal(hooks["StopFailure"])
			if !strings.Contains(string(added), quoteExe(exe)+" hook failed") || strings.Count(string(added), "hook failed") != 1 {
				t.Errorf("%s: the entry added does not call the reminal the others call, once: %s", c.bin, added)
			}
			// But for the entry added, the file says what it said.
			delete(hooks, "StopFailure")
			if !reflect.DeepEqual(got, was) {
				t.Errorf("%s: something other than the new entry changed:\nwas %v\nnow %v", c.bin, was, got)
			}
			if !hooksInstalled(&now, home) || hookVersionMismatch(&now, home, c.version) {
				t.Errorf("%s: after the heal it still reads as needing a setup", c.bin)
			}
			// Again: nothing to add, nothing written.
			raw, _ := os.ReadFile(path)
			st, _ := os.Stat(path)
			if said := healAddedHooks(home, version); len(said) != 0 {
				t.Errorf("%s: a second run said %q", c.bin, said)
			}
			again, _ := os.ReadFile(path)
			st2, _ := os.Stat(path)
			if string(again) != string(raw) || !st2.ModTime().Equal(st.ModTime()) {
				t.Errorf("%s: a second run wrote the file", c.bin)
			}
		}
	}
}

// What is left alone: an agent that was never set up (no file; a file with
// only the person's own hooks, one of them naming reminal in a command of
// their own), an agent whose version is too old for the event or was not
// read, and a file that is not valid JSON.
func TestAnUpgradeAddsNoHookWhereNoneIsWanted(t *testing.T) {
	untouched := func(name, home, path, content string, version func(string) string) {
		t.Helper()
		if said := healAddedHooks(home, version); len(said) != 0 {
			t.Errorf("%s: said %q", name, said)
		}
		got, err := os.ReadFile(path)
		if content == "" {
			if !os.IsNotExist(err) {
				t.Errorf("%s: a file was made", name)
			}
			return
		}
		if string(got) != content {
			t.Errorf("%s: the file was changed:\n%s", name, got)
		}
		if _, err := os.Stat(path + ".bak"); err == nil {
			t.Errorf("%s: a backup was made of a file not written", name)
		}
	}
	new := func(string) string { return "0.26.9" }
	home := t.TempDir()
	path := filepath.Join(home, ".qwen", "settings.json")
	untouched("no file", home, path, "", new)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"the person's own hooks only":          `{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "say done"}]}]}}`,
		"their own hook that runs reminal too": `{"hooks": {"Stop": [{"matcher": "", "hooks": [{"type": "command", "command": "reminal hook done && say done"}]}]}}`,
		"no hooks at all":                      `{"theme": "dark"}`,
		"not JSON":                             `{"hooks": `,
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		untouched(name, home, path, content, new)
	}
	// Set up, by a reminal of before; its Qwen Code too old for the event,
	// or its version not read.
	now := *targetNamed(t, "qwen").hooks
	old := specBefore(now, "StopFailure")
	for name, v := range map[string]string{"too old": "0.14.3", "version not read": ""} {
		home := t.TempDir()
		path := filepath.Join(home, ".qwen", "settings.json")
		if err := applyHooks(&old, home, "/usr/local/bin/reminal", false); err != nil {
			t.Fatal(err)
		}
		_ = os.Remove(path + ".bak")
		raw, _ := os.ReadFile(path)
		untouched(name, home, path, string(raw), func(string) string { return v })
	}
}

// Every other hook of an agent that is set up and has them all: nothing is
// written, whichever agent it is.
func TestAnUpgradeWritesNothingForAnInstallThatHasItAll(t *testing.T) {
	for _, tg := range agentTargets() {
		if tg.hooks == nil {
			continue
		}
		home := t.TempDir()
		spec := *tg.hooks
		spec.version = "999.0.0"
		if err := applyHooks(&spec, home, "/usr/local/bin/reminal", false); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(home, tg.hooks.file)
		raw, _ := os.ReadFile(path)
		if said := healAddedHooks(home, func(string) string { return "999.0.0" }); len(said) != 0 {
			t.Errorf("%s: said %q for an install with every hook", tg.Bin, said)
		}
		if got, _ := os.ReadFile(path); string(got) != string(raw) {
			t.Errorf("%s: the file of an install with every hook was written", tg.Bin)
		}
	}
}

// The start of a reminal ("integrate --heal-plugins", which the daemon and
// a session run) is where it happens.
func TestTheHealRunsAsReminalStarts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	oldV := checkVersion
	t.Cleanup(func() { checkVersion = oldV })
	checkVersion = func(bin string) string {
		if bin == "qwen" {
			return "0.26.9"
		}
		return ""
	}
	now := *targetNamed(t, "qwen").hooks
	old := specBefore(now, "StopFailure")
	if err := applyHooks(&old, home, "/usr/local/bin/reminal", false); err != nil {
		t.Fatal(err)
	}
	if err := runIntegrate([]string{"--heal-plugins"}); err != nil {
		t.Fatal(err)
	}
	if !ownHookWritten(&now, home, "StopFailure") {
		t.Error("the hook the upgrade brought was not written as reminal started")
	}
}
