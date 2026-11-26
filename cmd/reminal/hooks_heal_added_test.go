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

const healExe = "/usr/local/bin/reminal"

// specBefore is spec as a reminal that did not know event had it.
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

// atStart is the heal as reminal's start makes it; bySession, as a session
// makes it when its agent, at version, leaves for its shell (the entries
// too new for it taken out first, as there).
func atStart(home string) []string { return healAddedHooks(home, healExe, nil) }

func bySession(home, bin, version string) []string {
	said := func(b string) string {
		if b == bin {
			return version
		}
		return ""
	}
	return append(healHooks(home, said), healAddedHooks(home, healExe, said)...)
}

func events(spec hookSpec) []string {
	var out []string
	for _, ev := range spec.events {
		out = append(out, ev.Event)
	}
	return out
}

// dropOwn takes reminal's own entry for event out of the agent's file, as a
// person editing it would.
func dropOwn(t *testing.T, spec *hookSpec, home, event string) {
	t.Helper()
	removed, err := removeOwnHookFile(filepath.Join(home, spec.file), spec.key, event)
	if err != nil || !removed {
		t.Fatalf("could not take the %s entry out: %v", event, err)
	}
}

// An install made before reminal knew an event gets its hook with no
// integrate run: one every version of the agent has, as the upgraded
// reminal starts; one only a new enough agent has, when a session says its
// agent is that new. The entry calls the reminal the others call, quoted as
// they are; the person's own hooks, an event of theirs alone and every other
// setting are as they were; and a second run writes nothing.
func TestAnUpgradeAddsTheHookItBrought(t *testing.T) {
	for _, c := range []struct {
		bin, version, file, event, state string
		gated                            bool
	}{
		{"qwen", "0.26.9", ".qwen/settings.json", "StopFailure", "failed", true},
		{"claude", "2.1.304 (Claude Code)", ".claude/settings.json", "StopFailure", "failed", true},
		{"qwen", "0.26.9", ".qwen/settings.json", "SessionStart", "ready", false},
		{"claude", "2.1.304 (Claude Code)", ".claude/settings.json", "SubagentStop", "subagent", false},
	} {
		for _, exe := range []string{healExe, "/Users/some one/Applications/reminal.app/Contents/MacOS/reminal"} {
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
			old := specBefore(now, c.event)
			old.version = c.version
			if err := applyHooks(&old, home, exe, false); err != nil {
				t.Fatal(err)
			}
			was := readSettings(t, path)
			raw, _ := os.ReadFile(path)
			said := atStart(home)
			if c.gated {
				// Not on the word of whatever agent is on PATH.
				if got, _ := os.ReadFile(path); len(said) != 0 || string(got) != string(raw) {
					t.Fatalf("%s %s: written at reminal's start, with no session's word on the agent's version: %q", c.bin, c.event, said)
				}
				said = bySession(home, c.bin, c.version)
			}
			if len(said) != 1 || !strings.Contains(said[0], c.event) || !strings.Contains(said[0], "takes effect the next time") {
				t.Fatalf("%s %s: said %q, want one line naming it and when it takes effect", c.bin, c.event, said)
			}
			got := readSettings(t, path)
			hooks := got["hooks"].(map[string]any)
			added, _ := json.Marshal(hooks[c.event])
			if !strings.Contains(string(added), quoteExe(exe)+" hook "+c.state) || strings.Count(string(added), "hook "+c.state) != 1 {
				t.Errorf("%s %s: the entry added does not call the reminal the others call, once: %s", c.bin, c.event, added)
			}
			// But for the entry added, the file says what it said.
			delete(hooks, c.event)
			if !reflect.DeepEqual(got, was) {
				t.Errorf("%s %s: something other than the new entry changed:\nwas %v\nnow %v", c.bin, c.event, was, got)
			}
			if !hooksInstalled(&now, home) || hookVersionMismatch(&now, home, c.version) {
				t.Errorf("%s %s: after the heal it still reads as needing a setup", c.bin, c.event)
			}
			// Again, either way: nothing to add, nothing written.
			raw, _ = os.ReadFile(path)
			st, _ := os.Stat(path)
			if said := append(atStart(home), bySession(home, c.bin, c.version)...); len(said) != 0 {
				t.Errorf("%s %s: a second run said %q", c.bin, c.event, said)
			}
			again, _ := os.ReadFile(path)
			st2, _ := os.Stat(path)
			if string(again) != string(raw) || !st2.ModTime().Equal(st.ModTime()) {
				t.Errorf("%s %s: a second run wrote the file", c.bin, c.event)
			}
		}
	}
}

// What is left alone: an agent that was never set up (no file; a file with
// only the person's own hooks, one of them running reminal in a command of
// their own), a file that is not valid JSON, and an agent a session says is
// too old for the event.
func TestAnUpgradeAddsNoHookWhereNoneIsWanted(t *testing.T) {
	untouched := func(name, home, path, content string) {
		t.Helper()
		if said := append(atStart(home), bySession(home, "qwen", "0.26.9")...); len(said) != 0 {
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
	home := t.TempDir()
	path := filepath.Join(home, ".qwen", "settings.json")
	untouched("no file", home, path, "")
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
		untouched(name, home, path, content)
	}
	// Set up by a reminal of before; the session's Qwen Code too old.
	now := *targetNamed(t, "qwen").hooks
	old := specBefore(now, "StopFailure")
	home = t.TempDir()
	path = filepath.Join(home, ".qwen", "settings.json")
	if err := applyHooks(&old, home, healExe, false); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if said := append(atStart(home), bySession(home, "qwen", "0.14.3")...); len(said) != 0 {
		t.Errorf("a Qwen Code too old for the event: said %q", said)
	}
	if got, _ := os.ReadFile(path); string(got) != string(raw) {
		t.Error("a Qwen Code too old for the event: its file was written")
	}
}

// An agent that is set up and has every hook: nothing is written,
// whichever agent it is.
func TestAnUpgradeWritesNothingForAnInstallThatHasItAll(t *testing.T) {
	for _, tg := range agentTargets() {
		if tg.hooks == nil {
			continue
		}
		home := t.TempDir()
		spec := *tg.hooks
		spec.version = "999.0.0"
		if err := applyHooks(&spec, home, healExe, false); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(home, tg.hooks.file)
		raw, _ := os.ReadFile(path)
		if said := append(atStart(home), bySession(home, tg.Bin, "999.0.0")...); len(said) != 0 {
			t.Errorf("%s: said %q for an install with every hook", tg.Bin, said)
		}
		if got, _ := os.ReadFile(path); string(got) != string(raw) {
			t.Errorf("%s: the file of an install with every hook was written", tg.Bin)
		}
	}
}

// Where it happens: reminal's start ("integrate --heal-plugins", which the
// daemon and a session run), and a session's word on its agent's version
// ("integrate --heal-hooks --agent-version=…").
func TestTheHealRunsAsReminalStartsAndOnASessionsWord(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	oldExe := integrateExe
	t.Cleanup(func() { integrateExe = oldExe })
	integrateExe = func() (string, error) { return healExe, nil }
	now := *targetNamed(t, "qwen").hooks
	old := specBefore(specBefore(now, "StopFailure"), "SessionStart")
	if err := applyHooks(&old, home, healExe, false); err != nil {
		t.Fatal(err)
	}
	if err := runIntegrate([]string{"--heal-plugins"}); err != nil {
		t.Fatal(err)
	}
	if !ownHookWritten(&now, home, "SessionStart") || ownHookWritten(&now, home, "StopFailure") {
		t.Errorf("at reminal's start: SessionStart written %v (want true), StopFailure written %v (want false)", ownHookWritten(&now, home, "SessionStart"), ownHookWritten(&now, home, "StopFailure"))
	}
	if err := runIntegrate([]string{"--heal-hooks", "--agent-version=qwen=0.26.9"}); err != nil {
		t.Fatal(err)
	}
	if !ownHookWritten(&now, home, "StopFailure") {
		t.Error("on a session's word that its Qwen Code is new enough, StopFailure was not written")
	}
}

// An entry the person took out stays out: across a restart of reminal, and
// across an upgrade that brings another hook, which is added without the
// one they removed coming back. A run of integrate is what writes it again.
func TestAHookThePersonRemovedStaysRemoved(t *testing.T) {
	now := *targetNamed(t, "qwen").hooks
	old := specBefore(now, "StopFailure")
	home := t.TempDir()
	old.version = "0.26.9"
	if err := applyHooks(&old, home, healExe, false); err != nil {
		t.Fatal(err)
	}
	setHooksWritten(home, "qwen", events(old))
	dropOwn(t, &now, home, "Notification")
	// reminal starts again: nothing comes back.
	if said := atStart(home); len(said) != 0 || ownHookWritten(&now, home, "Notification") {
		t.Fatalf("a restart wrote the removed entry again: %q", said)
	}
	// The upgrade's hook is new to this install and is added; the entry the
	// person removed is not.
	said := bySession(home, "qwen", "0.26.9")
	if len(said) != 1 || !strings.Contains(said[0], "StopFailure") || strings.Contains(said[0], "Notification") {
		t.Fatalf("after the upgrade: %q, want StopFailure alone", said)
	}
	if !ownHookWritten(&now, home, "StopFailure") {
		t.Error("the hook the upgrade brought was not added")
	}
	if ownHookWritten(&now, home, "Notification") {
		t.Error("the entry the person removed came back with the upgrade")
	}
	// And again, and again: still out.
	for i := 0; i < 2; i++ {
		if said := append(atStart(home), bySession(home, "qwen", "0.26.9")...); len(said) != 0 || ownHookWritten(&now, home, "Notification") {
			t.Errorf("a later start: said %q, the removed entry back: %v", said, ownHookWritten(&now, home, "Notification"))
		}
	}
	// The new one, removed by the person in turn: stays out too.
	dropOwn(t, &now, home, "StopFailure")
	if said := append(atStart(home), bySession(home, "qwen", "0.26.9")...); len(said) != 0 || ownHookWritten(&now, home, "StopFailure") {
		t.Errorf("the new hook, removed by the person, came back: %q", said)
	}
}

// An install from before the note was kept: the events in its file are
// taken as the ones written and the others added, once; from then on an
// entry the person takes out stays out.
func TestAnInstallFromBeforeTheNoteIsNotedOnce(t *testing.T) {
	now := *targetNamed(t, "qwen").hooks
	old := specBefore(now, "SessionStart")
	old.version = "0.26.9"
	home := t.TempDir()
	if err := applyHooks(&old, home, healExe, false); err != nil {
		t.Fatal(err)
	}
	if notes, _ := readHooksNotes(home); len(notes) != 0 {
		t.Fatal("a note before any was kept")
	}
	if said := atStart(home); len(said) != 1 {
		t.Fatalf("the first start: %q", said)
	}
	notes, err := readHooksNotes(home)
	if err != nil || len(notes["qwen"].Wrote) != len(now.events) {
		t.Fatalf("after the first start the note holds %v (%v), want every event", notes["qwen"].Wrote, err)
	}
	dropOwn(t, &now, home, "Stop")
	if said := atStart(home); len(said) != 0 || ownHookWritten(&now, home, "Stop") {
		t.Errorf("an entry removed after the note was kept came back: %q", said)
	}
}

// The agent on PATH and the one a session runs may differ. An entry is
// written only on a session's word that its agent is new enough, and one
// taken out for a session's old agent is held, not forgotten: reminal's
// start does not write it again, so the two never take turns.
func TestAnOldAgentInASessionAndANewOneOnPathDoNotTakeTurns(t *testing.T) {
	now := *targetNamed(t, "claude").hooks
	home := t.TempDir()
	path := filepath.Join(home, now.file)
	full := now
	full.version = "2.1.90 (Claude Code)"
	if err := applyHooks(&full, home, healExe, false); err != nil {
		t.Fatal(err)
	}
	setHooksWritten(home, "claude", events(now))
	// A session running 2.1.77 leaves for its shell: the entry is taken out.
	said := bySession(home, "claude", "2.1.77 (Claude Code)")
	if len(said) != 1 || !strings.Contains(said[0], "removed its StopFailure hook") || ownHookWritten(&now, home, "StopFailure") {
		t.Fatalf("a session's 2.1.77: %q", said)
	}
	notes, _ := readHooksNotes(home)
	if notes["claude"].Held["StopFailure"] != "2.1.77" {
		t.Errorf("the note does not say what it was taken out for: %+v", notes["claude"])
	}
	// reminal starts again and again, the session's 2.1.77 leaves again:
	// nothing is written, nothing more is said.
	raw, _ := os.ReadFile(path)
	for i := 0; i < 3; i++ {
		if said := append(atStart(home), bySession(home, "claude", "2.1.77 (Claude Code)")...); len(said) != 0 {
			t.Fatalf("round %d: %q", i, said)
		}
		if got, _ := os.ReadFile(path); string(got) != string(raw) {
			t.Fatalf("round %d: the agent's file was written", i)
		}
	}
	// A session whose agent has the event says so: written again, once.
	said = bySession(home, "claude", "2.1.90 (Claude Code)")
	if len(said) != 1 || !ownHookWritten(&now, home, "StopFailure") {
		t.Fatalf("a session's 2.1.90: %q", said)
	}
	if notes, _ := readHooksNotes(home); len(notes["claude"].Held) != 0 {
		t.Errorf("still held after it was written again: %+v", notes["claude"])
	}
	if said := append(atStart(home), bySession(home, "claude", "2.1.90 (Claude Code)")...); len(said) != 0 {
		t.Errorf("after that: %q", said)
	}
}

// The reminal an added entry calls: the one the entries there name that is
// this reminal; else one named by a whole path with nothing in it a shell
// would read as more than a name; else none, and nothing is added.
func TestTheEntryAddedCallsAReminalThatCanBeTrusted(t *testing.T) {
	now := *targetNamed(t, "qwen").hooks
	entry := func(cmd, state string) string {
		return `{"hooks": [{"type": "command", "command": "` + cmd + `"}], "reminalHook": "` + state + `"}`
	}
	write := func(t *testing.T, cmds ...string) string {
		t.Helper()
		home := t.TempDir()
		path := filepath.Join(home, now.file)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		states := []struct{ ev, st string }{{"UserPromptSubmit", "working"}, {"Notification", "notify"}, {"Stop", "done"}}
		var parts []string
		for i, c := range cmds {
			parts = append(parts, `"`+states[i].ev+`": [`+entry(c+" hook "+states[i].st, states[i].st)+`]`)
		}
		if err := os.WriteFile(path, []byte(`{"hooks": {`+strings.Join(parts, ", ")+`}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		return home
	}
	odd := "/opt/x{a,b}/reminal"
	// The one that is this reminal is preferred, wherever it comes.
	home := write(t, odd, "/opt/other/reminal", healExe)
	if got := ownHookCalls(&now, home, healExe); got != healExe {
		t.Errorf("with an entry naming this reminal: calls %q", got)
	}
	// None is this reminal: the first named by a plain whole path.
	if got := ownHookCalls(&now, home, "/somewhere/else/reminal"); got != "/opt/other/reminal" {
		t.Errorf("with none naming this reminal: calls %q, want the plain whole path", got)
	}
	// Only odd ones, or ones by name alone: none, and nothing is added.
	for _, cmds := range [][]string{{odd}, {"reminal"}, {"~/bin/reminal"}, {"bin/reminal"}} {
		home := write(t, cmds...)
		raw, _ := os.ReadFile(filepath.Join(home, now.file))
		if got := ownHookCalls(&now, home, "/somewhere/else/reminal"); got != "" {
			t.Errorf("%v: calls %q, want none", cmds, got)
		}
		if said := healAddedHooks(home, "/somewhere/else/reminal", nil); len(said) != 0 {
			t.Errorf("%v: said %q", cmds, said)
		}
		if got, _ := os.ReadFile(filepath.Join(home, now.file)); string(got) != string(raw) {
			t.Errorf("%v: the file was written", cmds)
		}
	}
}

// A note that cannot be read as reminal's decides nothing: the heal is not
// made, the note is left as it is, and so is the agent's file.
func TestANoteThatCannotBeReadStopsTheHeal(t *testing.T) {
	now := *targetNamed(t, "qwen").hooks
	old := specBefore(now, "SessionStart")
	for name, note := range map[string]string{
		"not JSON":   `{"qwen": {"wrote": ["Stop"`,
		"too large":  `{"qwen": {"wrote": ["` + strings.Repeat("x", hooksNoteMax) + `"]}}`,
		"wrong kind": `["qwen"]`,
	} {
		home := t.TempDir()
		if err := applyHooks(&old, home, healExe, false); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(home, ".reminal"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(hooksWrittenPath(home), []byte(note), 0o600); err != nil {
			t.Fatal(err)
		}
		raw, _ := os.ReadFile(filepath.Join(home, now.file))
		if said := append(atStart(home), bySession(home, "qwen", "0.26.9")...); len(said) != 0 {
			t.Errorf("%s: said %q", name, said)
		}
		if got, _ := os.ReadFile(filepath.Join(home, now.file)); string(got) != string(raw) {
			t.Errorf("%s: the agent's file was written", name)
		}
		if got, _ := os.ReadFile(hooksWrittenPath(home)); string(got) != note {
			t.Errorf("%s: the note was written over", name)
		}
	}
}

// Both heals run under one lock: the daemon and a session start together
// after an upgrade.
func TestTheHealsRunUnderTheirLock(t *testing.T) {
	old := withHooksHealLock
	t.Cleanup(func() { withHooksHealLock = old })
	held, ran := 0, 0
	withHooksHealLock = func(fn func()) { held++; fn(); ran++ }
	home := t.TempDir()
	atStart(home)
	bySession(home, "qwen", "0.26.9")
	if held != 3 || ran != 3 {
		t.Errorf("the lock was taken %d time(s) for the start's heal and a session's two, want 3", held)
	}
	// Another reminal holds it: nothing is done meanwhile.
	now := *targetNamed(t, "qwen").hooks
	before := specBefore(now, "SessionStart")
	if err := applyHooks(&before, home, healExe, false); err != nil {
		t.Fatal(err)
	}
	withHooksHealLock = func(fn func()) {}
	if said := atStart(home); len(said) != 0 || ownHookWritten(&now, home, "SessionStart") {
		t.Errorf("done without the lock: %q", said)
	}
}

// integrate notes what it writes, and a remove drops the note.
func TestIntegrateNotesTheHooksItWrites(t *testing.T) {
	home := integrateRig(t, 200)
	runIntegrateRig(t, "--yes")
	notes, err := readHooksNotes(home)
	if err != nil || len(notes["gemini"].Wrote) == 0 {
		t.Fatalf("after integrate the note for an agent set up holds %+v (%v)", notes["gemini"], err)
	}
	for _, ev := range targetNamed(t, "gemini").hooks.events {
		if ev.MinVersion == "" && !notes["gemini"].wrote(ev.Event) {
			t.Errorf("%s was written and is not in the note", ev.Event)
		}
	}
	runIntegrateRig(t, "--remove", "--yes")
	if notes, _ := readHooksNotes(home); len(notes["gemini"].Wrote) != 0 {
		t.Error("the note is kept after a remove")
	}
}

// A note that cannot be read does not stop the heal for good: integrate,
// which writes every event, begins it again, and the heal works after.
// And a note a build of before wrote (the events alone, as a list) is read.
func TestIntegrateBeginsAnUnreadableNoteAgain(t *testing.T) {
	now := *targetNamed(t, "qwen").hooks
	old := specBefore(now, "SessionStart")
	home := t.TempDir()
	if err := applyHooks(&old, home, healExe, false); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".reminal"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hooksWrittenPath(home), []byte(`{"qwen": {"wrote": ["Stop"`), 0o600); err != nil {
		t.Fatal(err)
	}
	if said := atStart(home); len(said) != 0 {
		t.Fatalf("with a note that cannot be read: %q", said)
	}
	// integrate for another agent: the note is begun again, with that one.
	setHooksWritten(home, "claude", []string{"Stop", "UserPromptSubmit"})
	notes, err := readHooksNotes(home)
	if err != nil || len(notes["claude"].Wrote) != 2 {
		t.Fatalf("after integrate the note reads %+v (%v)", notes, err)
	}
	// And the heal works again: Qwen Code, with no note of its own now, has
	// what its file lacks added.
	if said := atStart(home); len(said) != 1 || !ownHookWritten(&now, home, "SessionStart") {
		t.Errorf("after integrate began the note again, the heal: %q", said)
	}

	// The form of a build of before.
	home = t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".reminal"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hooksWrittenPath(home), []byte(`{"qwen": ["Notification", "Stop", "UserPromptSubmit"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	notes, err = readHooksNotes(home)
	if err != nil || !notes["qwen"].wrote("Notification") || len(notes["qwen"].Wrote) != 3 {
		t.Fatalf("a note of a build of before reads %+v (%v)", notes, err)
	}
	// Read as written, so an entry of it gone from the file stays out.
	if err := applyHooks(&old, home, healExe, false); err != nil {
		t.Fatal(err)
	}
	dropOwn(t, &now, home, "Notification")
	if said := atStart(home); len(said) != 1 || strings.Contains(said[0], "Notification") || ownHookWritten(&now, home, "Notification") {
		t.Errorf("with a note of a build of before: %q", said)
	}
}

// integrate writes its note under the heal's lock.
func TestIntegrateNotesUnderTheHealsLock(t *testing.T) {
	integrateRig(t, 200)
	old := withHooksHealLock
	t.Cleanup(func() { withHooksHealLock = old })
	held := 0
	withHooksHealLock = func(fn func()) { held++; fn() }
	runIntegrateRig(t, "--yes")
	if held == 0 {
		t.Error("integrate wrote its note without the heal's lock")
	}
}
