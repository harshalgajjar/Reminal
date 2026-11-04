// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"reminal/internal/session"
)

// hookBG is a hook record's background as written ({"shells":1,…}).
func hookBG(t *testing.T, js string) *session.Background {
	t.Helper()
	var bg session.Background
	if err := json.Unmarshal([]byte(js), &bg); err != nil {
		t.Fatal(err)
	}
	return &bg
}

// Claude Code's footer counts its monitors beside its shells, joined by a
// comma ("· 1 shell, 2 monitors"; its bundle: a.push(o===1?"1 monitor":…),
// a.join(", ")). Read there, and nowhere above its box.
func TestClaudesFooterMonitorsAreCounted(t *testing.T) {
	for _, c := range []struct {
		name, screen string
		want         map[string]int
	}{
		{"a shell and monitors", claudeBox + "\n  ⏵⏵ accept edits on · 1 shell, 2 monitors · ← 2 agents", map[string]int{"monitor": 2}},
		{"one monitor alone", claudeBox + "\n  ⏵⏵ accept edits on · 1 monitor", map[string]int{"monitor": 1}},
		{"a summary above the box is not the footer", "✻ Baked for 15s · 1 monitor still running\n" + claudeBox + "\n  ⏵⏵ accept edits on", nil},
		{"no monitor", claudeBox + "\n  ⏵⏵ accept edits on · 1 shell", nil},
	} {
		if got := attnOtherOnScreen("claude", true, c.screen); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	if got := attnOtherOnScreen("claude", false, claudeBox+"\n  · 1 monitor"); got != nil {
		t.Errorf("no agent in front: %v", got)
	}
}

// geminiScreenWith is a recorded Gemini CLI screen (harness-screens) with
// its status row (StatusRow: the approval mode, then its context summary)
// saying status: its input box under it, its footer under that.
func geminiScreenWith(t *testing.T, status string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "harness-screens", "gemini-expired-start.txt"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	const row = " Shift+Tab to accept edits"
	if !strings.Contains(s, row) {
		t.Fatal("the recorded screen has no status row")
	}
	return strings.Replace(s, row, row+status, 1)
}

// Gemini CLI says its background processes in its context summary
// ("1 Background process", "N Background processes"), in the status row
// over its input box (Composer.tsx: StatusRow, InputPrompt, Footer): where
// attnFooter, under the box, never reads. Read there for gemini only; the
// same words in what it wrote above are not it.
func TestGeminisBackgroundProcessesAreReadOverItsBox(t *testing.T) {
	two := geminiScreenWith(t, "          1 GEMINI.md file · 2 Background processes")
	if got, want := attnOtherOnScreen("gemini", true, two), map[string]int{"background process": 2}; !reflect.DeepEqual(got, want) {
		t.Errorf("gemini's status row: %v, want %v", got, want)
	}
	if got, want := attnOtherOnScreen("gemini", true, geminiScreenWith(t, "          1 Background process")), map[string]int{"background process": 1}; !reflect.DeepEqual(got, want) {
		t.Errorf("first in the summary: %v, want %v", got, want)
	}
	// Where it sits: not in the footer attnFooter reads.
	if f := attnFooter(attentionProbeTail(two, attnTailRows)); strings.Contains(f, "Background") {
		t.Errorf("the count read as the footer's:\n%s", f)
	}
	// Words of its own above its status row's rule are not its status.
	said := strings.Replace(geminiScreenWith(t, ""), "Gemini CLI update available!", "2 Background processes are fine", 1)
	if got := attnOtherOnScreen("gemini", true, said); got != nil {
		t.Errorf("its output above read as its status: %v", got)
	}
	if got := attnOtherOnScreen("claude", true, two); got != nil {
		t.Errorf("read for another program: %v", got)
	}
}

// What is running is the larger of the screen's count and the hook's, kind
// by kind: a hook that lists work it counts as none, or fewer of it, never
// lowers what the screen shows, nor the screen the hook. Antigravity's
// "background work" (no count) stands only while nothing else counts.
func TestAHookAndTheScreenMakeOneCount(t *testing.T) {
	for _, c := range []struct {
		name         string
		screenShells int
		screenOther  map[string]int
		hook         string
		shells       int
		agents       int
		other        map[string]int
	}{
		{"a hook counting none of what it lists", 2, nil, `{}`, 2, 0, nil},
		{"the hook's more", 1, nil, `{"shells":3,"agents":1}`, 3, 1, nil},
		{"the screen's monitors and the hook's", 0, map[string]int{"monitor": 2}, `{"other":{"monitor":1,"workflow":1}}`, 0, 0, map[string]int{"monitor": 2, "workflow": 1}},
		{"background work, nothing else", 0, nil, `{"other":{"background work":1}}`, 0, 0, map[string]int{"background work": 1}},
		{"background work, and a count on screen", 0, map[string]int{"background process": 2}, `{"other":{"background work":1}}`, 0, 0, map[string]int{"background process": 2}},
		{"no hook", 1, map[string]int{"monitor": 1}, ``, 1, 0, map[string]int{"monitor": 1}},
	} {
		var bg *session.Background
		if c.hook != "" {
			bg = hookBG(t, c.hook)
		}
		shells, agents, other := attnBackground(c.screenShells, c.screenOther, bg)
		if shells != c.shells || agents != c.agents || !reflect.DeepEqual(other, c.other) {
			t.Errorf("%s: %d shells, %d agents, %v; want %d, %d, %v", c.name, shells, agents, other, c.shells, c.agents, c.other)
		}
	}
}
