// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"reminal/internal/session"
)

// What may sleep, what never may, and what a sleep would end — said, never
// lost silently.
func TestSleepVerdict(t *testing.T) {
	idle := sleepFacts{Headless: true, Saved: true, Fg: "claude", Conv: "conv-0123456789ab", Attn: "done", BgSaid: true, ConvThere: true, Turned: true}
	with := func(f func(*sleepFacts)) sleepFacts { c := idle; f(&c); return c }
	cases := []struct {
		name   string
		f      sleepFacts
		block  string
		losses []string
	}{
		{"an idle claude sleeps", idle, "", nil},
		{"claude never sent a message sleeps (nothing to resume)", with(func(f *sleepFacts) { f.Conv, f.NoTurn = "", true }), "", nil},
		{"kept awake on purpose", with(func(f *sleepFacts) { f.Pinned = true }), "you asked", nil},
		{"a terminal tab is the session", with(func(f *sleepFacts) { f.Headless = false }), "open in a terminal", nil},
		{"a save that failed", with(func(f *sleepFacts) { f.Saved = false }), "couldn't save", nil},
		{"an agent resumed only by 'latest'", with(func(f *sleepFacts) { f.Fg = "gemini" }), "can't be resumed exactly", nil},
		{"claude whose conversation is not known", with(func(f *sleepFacts) { f.Conv = "" }), "which conversation", nil},
		{"a turn under way", with(func(f *sleepFacts) { f.Attn = "working" }), "", []string{"the agent's unfinished turn"}},
		{"a question to the person", with(func(f *sleepFacts) { f.Attn = "input" }), "", []string{"the question it was waiting on you for"}},
		{"background shells", with(func(f *sleepFacts) { f.Shells = 1 }), "", []string{"its agent's background work"}},
		{"other background work (a monitor)", with(func(f *sleepFacts) { f.Other = 1 }), "", []string{"its agent's background work"}},
		{"a job in its shell", with(func(f *sleepFacts) { f.Jobs = []string{"npm"} }), "", []string{"npm, running in it"}},
		{"a terminal attached", with(func(f *sleepFacts) { f.Attached = 1 }), "", []string{"the terminal attached to it"}},
		{"a plain shell", with(func(f *sleepFacts) { f.Fg, f.Conv = "", "" }), "", []string{"what was typed into its shell by hand (variables, aliases)"}},
		{"an agent that never said what it left running", with(func(f *sleepFacts) { f.BgSaid = false }), "", []string{"any background work its agent left (it didn't say)"}},
		{"never said, but never sent a message either", with(func(f *sleepFacts) { f.BgSaid, f.Conv, f.NoTurn, f.Turned = false, "", true, false }), "", nil},
		{"never said, with no turn since it woke", with(func(f *sleepFacts) { f.BgSaid, f.Turned = false, false }), "", nil},
		{"never said, with shells seen", with(func(f *sleepFacts) { f.BgSaid, f.Shells = false, 1 }), "", []string{"its agent's background work"}},
		{"typed and not sent", with(func(f *sleepFacts) { f.Draft = true }), "", []string{"what was typed and not sent"}},
	}
	for _, c := range cases {
		block, losses := sleepVerdict(c.f)
		if c.block == "" && block != "" || c.block != "" && !strings.Contains(block, c.block) {
			t.Errorf("%s: block %q, want one with %q", c.name, block, c.block)
		}
		if !reflect.DeepEqual(losses, c.losses) {
			t.Errorf("%s: losses %q, want %q", c.name, losses, c.losses)
		}
	}
}

func TestLastLines(t *testing.T) {
	if got := lastLines("a\nb\nc\nd\n", 2); got != "c\nd" {
		t.Fatalf("got %q", got)
	}
	if got := lastLines("a\nb", 5); got != "a\nb" {
		t.Fatalf("got %q", got)
	}
}

// A session asleep is left alone at start, unless its wake was cut short
// just now (then the wake is finished); an older cut-short wake sleeps on.
func TestSleepsOn(t *testing.T) {
	const id = "SLEEPC01"
	now := time.Now()
	if sleepsOn(id, now) {
		t.Fatal("a session not asleep sleeps on")
	}
	_ = session.MarkAsleep(id, session.Asleep{SleptAt: now, Why: "asked"})
	defer session.ClearAsleep(id)
	if !sleepsOn(id, now) {
		t.Fatal("a sleeper is restored at start")
	}
	_ = session.MarkWaking(id, now.Add(-time.Minute))
	if sleepsOn(id, now) {
		t.Fatal("a wake cut short a minute ago is not tried again")
	}
	_ = session.MarkWaking(id, now.Add(-time.Hour))
	if !sleepsOn(id, now) {
		t.Fatal("a wake cut short an hour ago is tried again")
	}
}

// What keys leave on the input line: typed is a draft until sent or
// cleared; a paste's line ends do not send it; arrows and the terminal's
// own replies change nothing.
func TestInputLine(t *testing.T) {
	cases := []struct {
		name  string
		keys  []string
		draft bool
	}{
		{"typed", []string{"fix the ", "build"}, true},
		{"typed and sent", []string{"fix it", "\r"}, false},
		{"sent in one go", []string{"fix it\r"}, false},
		{"cleared with Ctrl-C", []string{"fix it", "\x03"}, false},
		{"cleared with Ctrl-U", []string{"fix it\x15"}, false},
		{"arrows only", []string{"\x1b[A\x1b[B\x1bOC"}, false},
		{"arrows after typing", []string{"fix", "\x1b[D"}, true},
		{"a paste's line ends", []string{string(pasteStart) + "one\ntwo\n" + string(pasteEnd)}, true},
		{"a paste then sent", []string{string(pasteStart) + "one\n" + string(pasteEnd), "\r"}, false},
		{"a paste split across reads", []string{string(pasteStart) + "one", "\n", string(pasteEnd)}, true},
		{"the terminal's replies", []string{string(personKeys([]byte("\x1b]11;rgb:0000/0000/0000\x07\x1b[12;40R")))}, false},
	}
	for _, c := range cases {
		var l inputLine
		for _, k := range c.keys {
			l.feed([]byte(k))
		}
		if l.draft != c.draft {
			t.Errorf("%s: draft %v, want %v", c.name, l.draft, c.draft)
		}
	}
}

// A restored session clears its own sleep marks once it is up: a waker that
// ended before it could leaves no running session marked asleep. One that
// was not restored (started anew) leaves them be.
func TestARestoredSessionMarksItselfAwake(t *testing.T) {
	const id = "WAKEME01"
	for _, restoring := range []bool{false, true} {
		_ = session.MarkAsleep(id, session.Asleep{SleptAt: time.Now(), Why: "asked"})
		_ = session.MarkWaking(id, time.Now())
		(&Agent{sessionID: id, restoring: restoring}).markAwake()
		_, waking := session.WakingSince(id)
		if asleep := session.IsAsleep(id); asleep == restoring || waking == restoring {
			t.Errorf("restoring=%v: asleep %v, waking %v after markAwake", restoring, asleep, waking)
		}
	}
	session.ClearAsleep(id)
}

// Claude's files are looked for where it keeps them: CLAUDE_CONFIG_DIR when
// set. A sleep that could not be woken on its conversation is refused.
func TestClaudeConversationFileFollowsItsConfigDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	const conv = "conv-0123456789ab"
	if convSize("claude", conv) != 0 {
		t.Fatal("found before written")
	}
	p := dir + "/projects/-work/" + conv + ".jsonl"
	if err := os.MkdirAll(dir+"/projects/-work", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if convSize("claude", conv) != 3 {
		t.Fatalf("size %d, want 3", convSize("claude", conv))
	}
	if convSize("claude", "../x") != 0 {
		t.Fatal("a pattern read as an id")
	}
	block, _ := sleepVerdict(sleepFacts{Headless: true, Saved: true, Fg: "claude", Conv: conv, BgSaid: true})
	if !strings.Contains(block, "can't find claude's conversation") {
		t.Fatalf("no file: block %q", block)
	}
}

// Keys typed after a sleep was asked for are someone using the session: it
// stays awake, and takes keys. With none, it goes, and keys after that are
// not typed into a shell about to be ended.
func TestGoToSleep(t *testing.T) {
	a := &Agent{}
	asked := time.Now()
	time.Sleep(time.Millisecond)
	a.feedTyping([]byte("x"))
	if a.goToSleep(asked) || a.asleep() {
		t.Fatal("went to sleep with keys typed after the ask")
	}
	if !a.goToSleep(time.Now()) || !a.asleep() {
		t.Fatal("did not go to sleep with no keys since")
	}
	if n, err := a.typeIn([]byte("rm -rf x\r")); err != nil || n != 9 {
		t.Fatalf("typeIn once asleep: %d %v", n, err) // a nil terminal: written, it would panic
	}
}

// A draft goes in with a turn its hook said began after it was typed; keys
// typed during a turn are a new draft; a turn told twice is one.
func TestTurnBegan(t *testing.T) {
	var l inputLine
	l.feed([]byte("fix it"))
	l.at = time.Now()
	l.turnBegan(l.at.Add(-time.Second))
	if !l.draft {
		t.Fatal("a turn from before the keys took them")
	}
	l.turnBegan(l.at.Add(time.Second))
	if l.draft {
		t.Fatal("a turn after the keys did not take them")
	}
	l.feed([]byte("and this"))
	l.at = l.turn.Add(time.Second)
	l.turnBegan(l.turn)
	if !l.draft {
		t.Fatal("the same turn told again took keys typed since")
	}
}

// Keep awake from another device names a session there is, never any id.
func TestKeepAwakeNeedsASession(t *testing.T) {
	if _, err := sleepAct("NOSUCH01", "keep_awake"); err == nil || session.AwakePinned("NOSUCH01") {
		t.Fatalf("pinned a session that is not there: %v", err)
	}
}

// The run's first turn is kept: a later turn does not move it, and a turn
// from before this agent run (a hook left by the last) is not its.
func TestFirstTurnOfTheRun(t *testing.T) {
	run := time.Now()
	a := &Agent{attnFGAt: run}
	a.noteFirstTurnLocked(run.Add(-time.Minute))
	if !a.firstTurn.at.IsZero() {
		t.Fatal("a turn from before the run taken as its first")
	}
	a.noteFirstTurnLocked(run.Add(2 * time.Second))
	a.noteFirstTurnLocked(run.Add(9 * time.Second))
	if !a.firstTurn.of(run).Equal(run.Add(2 * time.Second)) {
		t.Fatalf("first turn %v", a.firstTurn)
	}
	a.attnFGAt = run.Add(time.Hour) // another run in front
	a.noteFirstTurnLocked(run.Add(time.Hour + time.Second))
	if !a.firstTurn.of(a.attnFGAt).Equal(run.Add(time.Hour+time.Second)) || !a.firstTurn.of(run).IsZero() {
		t.Fatalf("the next run's first turn %v", a.firstTurn)
	}
}
