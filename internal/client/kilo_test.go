// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Kilo Code CLI 7.8.27 runs as `node …/bin/kilo` (its launcher, the
// session's foreground leader) over its own binary, .kilo: read as kilo, an
// agent whose screen is read.
func TestKiloIsAnAgent(t *testing.T) {
	if !isAgentProgram("kilo") {
		t.Fatal("kilo is not an agent")
	}
	for _, args := range [][]string{
		{"node", "/usr/local/bin/kilo", "--log-level", "DEBUG"},
		{"node", "/home/u/.npm-global/lib/node_modules/@kilocode/cli/bin/kilo"},
	} {
		if got := programFromArgs(args, "node"); got != "kilo" {
			t.Errorf("%v: %q, want kilo", args, got)
		}
	}
}

// Kilo's screens, recorded in Docker against a stand-in provider: a turn
// done reads as done, at its prompt (its box's "╹▀▀▀" row, as OpenCode's);
// its approval, drawn over that row, never as done (its plugin says it needs
// you: permission.asked); and a key the service refused ("┃
// Incorrect API key provided") as logged out, as OpenCode's does.
func TestKilosScreens(t *testing.T) {
	read := func(file string) string {
		b, err := os.ReadFile(filepath.Join("testdata", "harness-screens", file+".txt"))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if got := classifyAttnFor("kilo", true, attentionProbeTail(read("kilo-done"), attnTailRows), 5000, time.Minute, false); got != "done" {
		t.Errorf("kilo-done: %q, want done", got)
	}
	if got := classifyAttnFor("kilo", true, attentionProbeTail(read("kilo-approve"), attnTailRows), 5000, time.Minute, false); got == "done" {
		t.Error("kilo-approve: read as done")
	}
	if !attnPromptDrawn("kilo", attentionProbeTail(read("kilo-done"), attnTailRows)) {
		t.Error("kilo-done: its prompt box not seen")
	}
	if attnPromptDrawn("kilo", "$ kilo\n") {
		t.Error("a shell's command line read as kilo at its prompt")
	}
	bad := read("kilo-badkey")
	tail := harnessTail(bad, harnessRowsFor("kilo"))
	if !harnessKeyRefusedBy(tail, "kilo") {
		t.Error("kilo-badkey: the refused key not seen")
	}
	if harnessKeyRefusedBy(harnessTail(read("kilo-done"), harnessRowsFor("kilo")), "kilo") || harnessProblemIn(harnessTail(read("kilo-done"), harnessRowsFor("kilo")), "kilo") {
		t.Error("kilo-done: read as logged out")
	}
	if !harnessProblemIn("┃  Anthropic API key is missing. Pass it using the 'apiKey' parameter or the ANTHROPIC_API_KEY environment variable.\n┃", "kilo") {
		t.Error("a provider's key missing not seen")
	}
	if !hookTurns["kilo"] {
		t.Error("kilo's plugin marks its turns, as OpenCode's does")
	}
}
