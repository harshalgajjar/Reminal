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
	"time"

	"reminal/internal/session"
)

// The payloads below are shaped as Claude Code's hooks reference documents
// them: Stop and SubagentStop carry background_tasks, each an id, a type
// ("shell" or "subagent"), a status, and more besides.
const stopWithBackground = `{"session_id":"3f2c9a10-7d4e-4b8a-9c11-0e5d6f7a8b9c","hook_event_name":"Stop","stop_hook_active":false,
"background_tasks":[
 {"id":"b1","type":"shell","status":"running","description":"run the tests","command":"go test ./..."},
 {"id":"a1","type":"subagent","status":"running","description":"review","agent_type":"general-purpose"},
 {"id":"a2","type":"subagent","status":"running","description":"search","agent_type":"Explore"},
 {"id":"a3","type":"subagent","status":"completed","description":"done already"},
 {"id":"y","type":"subagent","status":"pending"},
 {"id":"x","type":"monitor","status":"running"},
 "not an entry",
 {"id":7,"type":"Shell","status":"RUNNING","extra":{"n":1}}
],"session_crons":[]}`

// What a harness still has running is read off its hook's payload: only an
// entry that says it is running counts, a shell or a subagent, whatever else
// the entry carries; the subagent a SubagentStop is about is finishing; and a
// payload that says nothing of it is told apart from one that says none.
func TestAHookReadsWhatIsStillRunningInTheBackground(t *testing.T) {
	for _, c := range []struct {
		name, payload, except string
		want                  *session.Background
	}{
		// The monitor is running work too, under its own name.
		{"stop", stopWithBackground, "", &session.Background{Shells: 2, Agents: 2, Other: map[string]int{"monitor": 1}}},
		{"subagent stop", stopWithBackground, "a1", &session.Background{Shells: 2, Agents: 1, Other: map[string]int{"monitor": 1}}},
		{"none running", `{"hook_event_name":"Stop","background_tasks":[]}`, "", &session.Background{}},
		{"not said", `{"hook_event_name":"Stop","stop_hook_active":false}`, "", nil},
		{"not json", `not json`, "", nil},
		{"not a list", `{"background_tasks":"many"}`, "", nil},
	} {
		got := hookBackground([]byte(c.payload), c.except)
		if (got == nil) != (c.want == nil) || (got != nil && !reflect.DeepEqual(*got, *c.want)) {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
	if id := hookAgentID([]byte(`{"hook_event_name":"SubagentStop","agent_id":"a1","agent_type":"general-purpose"}`)); id != "a1" {
		t.Errorf("agent_id %q", id)
	}
}

func hookWithPayload(t *testing.T, arg, payload string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString(payload); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old }()
	if err := runHook([]string{arg}); err != nil {
		t.Fatal(err)
	}
}

// A turn that ends with work left running keeps the count with its "done";
// a subagent finishing changes the count and nothing else — not the turn's
// state, nor when that was said — and the next turn starts without one. A
// SubagentStop before any turn has said anything writes nothing.
func TestTheBackgroundCountStaysWithTheTurnThatLeftIt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("REMINAL_SESSION", "BGBG2345")

	hookWithPayload(t, "subagent", `{"hook_event_name":"SubagentStop","agent_id":"a1","background_tasks":[]}`)
	if hs := session.ReadHookStateAnyAge("BGBG2345"); hs != nil {
		t.Fatalf("a SubagentStop with no turn behind it wrote %+v", hs)
	}

	hookWithPayload(t, "done", stopWithBackground)
	hs := session.ReadHookStateAnyAge("BGBG2345")
	if hs == nil || hs.State != "done" || hs.Background == nil || !reflect.DeepEqual(*hs.Background, session.Background{Shells: 2, Agents: 2, Other: map[string]int{"monitor": 1}}) {
		t.Fatalf("after Stop: %+v", hs)
	}
	said := hs.TS

	time.Sleep(5 * time.Millisecond) // so a rewritten TS would differ
	hookWithPayload(t, "subagent", `{"hook_event_name":"SubagentStop","agent_id":"a2","background_tasks":[
 {"id":"b1","type":"shell","status":"running"},{"id":"a2","type":"subagent","status":"running"}]}`)
	hs = session.ReadHookStateAnyAge("BGBG2345")
	if hs == nil || hs.State != "done" || !hs.TS.Equal(said) || hs.Background == nil || !reflect.DeepEqual(*hs.Background, session.Background{Shells: 1}) {
		t.Fatalf("after SubagentStop: %+v (said at %v)", hs, said)
	}

	// One that says nothing of the rest leaves the count as it was.
	hookWithPayload(t, "subagent", `{"hook_event_name":"SubagentStop","agent_id":"a9"}`)
	if hs2 := session.ReadHookStateAnyAge("BGBG2345"); hs2 == nil || hs2.Background == nil || !reflect.DeepEqual(*hs2.Background, session.Background{Shells: 1}) {
		t.Fatalf("a SubagentStop without the list changed the count: %+v", hs2)
	}

	hookWithPayload(t, "working", `{"hook_event_name":"UserPromptSubmit"}`)
	if hs := session.ReadHookStateAnyAge("BGBG2345"); hs == nil || hs.State != "working" || hs.Background != nil {
		t.Fatalf("a new turn kept the last one's count: %+v", hs)
	}
}

// With REMINAL_HOOK_DEBUG set, a hook says what it made of the background
// work — the counts — and never what the payload said.
func TestTheHookDebugLineHasTheCountsNotThePayload(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("REMINAL_SESSION", "DBDB2345")
	t.Setenv("REMINAL_HOOK_DEBUG", "1")
	hookWithPayload(t, "done", stopWithBackground)
	hookWithPayload(t, "done", `{"hook_event_name":"Stop"}`)
	b, err := os.ReadFile(filepath.Join(home, ".reminal", "hook-debug.log"))
	if err != nil {
		t.Fatal(err)
	}
	log := string(b)
	for _, want := range []string{"session=DBDB2345 event=done shells=2 agents=2", "session=DBDB2345 event=done no background_tasks"} {
		if !strings.Contains(log, want) {
			t.Errorf("want %q in:\n%s", want, log)
		}
	}
	for _, never := range []string{"go test", "review", "3f2c9a10"} {
		if strings.Contains(log, never) {
			t.Errorf("the payload's %q is in the debug log:\n%s", never, log)
		}
	}
}

// Claude Code's hooks include SubagentStop, so a subagent finishing between
// turns is heard.
func TestClaudeCodesHooksHearASubagentFinish(t *testing.T) {
	for _, tg := range agentTargets() {
		if tg.Bin != "claude" {
			continue
		}
		for _, e := range tg.hooks.events {
			if e.Event == "SubagentStop" && e.State == "subagent" {
				return
			}
		}
		t.Fatalf("Claude Code's hooks: %+v", tg.hooks.events)
	}
	t.Fatal("no Claude Code target")
}

// hookBackgroundJSON is what hookBackground made of payload, as it is kept
// in the hook record ("null" for nothing said).
func hookBackgroundJSON(t *testing.T, payload string) string {
	t.Helper()
	b, err := json.Marshal(hookBackground([]byte(payload), ""))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Qwen Code's Stop lists its background agents as its bundle builds them
// (getBackgroundTaskSnapshot: id, status, agent_type, started_at,
// description), with no type: each one running is a subagent. One with
// neither a type nor an agent_type is still running work.
func TestQwensStopCountsItsBackgroundAgents(t *testing.T) {
	const qwenStop = `{"session_id":"s1","hook_event_name":"Stop","stop_hook_active":false,"last_assistant_message":"ok",
"background_tasks":[
 {"id":"t1","status":"running","agent_type":"general-purpose","started_at":"2026-11-03T10:00:00.000Z","description":"run the benchmark"},
 {"id":"t2","status":"running","agent_type":"unknown","started_at":"2026-11-03T10:01:00.000Z","description":"review"},
 {"id":"t3","status":"completed","agent_type":"Explore","started_at":"2026-11-03T09:00:00.000Z","description":"done already"},
 {"id":"t4","status":"cancelled","agent_type":"Explore"},
 {"id":"t5","status":"running","description":"no kind said"}
],"crons":[]}`
	if got, want := hookBackgroundJSON(t, qwenStop), `{"agents":2,"other":{"background task":1}}`; got != want {
		t.Errorf("qwen Stop: %s, want %s", got, want)
	}
}

// Every kind Claude Code's Stop names (its type map: subagent, workflow,
// shell, monitor, MCP task, teammate, …) is running work: one reminal has no
// name for is counted under its own, never as none.
func TestEveryRunningKindAHookNamesIsCounted(t *testing.T) {
	const stop = `{"hook_event_name":"Stop","background_tasks":[
 {"id":"m1","type":"monitor","status":"running"},
 {"id":"m2","type":"monitor","status":"running"},
 {"id":"w1","type":"workflow","status":"running"},
 {"id":"p1","type":"MCP task","status":"running"},
 {"id":"x1","type":"teammate","status":"completed"},
 {"id":"z1","type":"something new","status":"running"}
]}`
	if got, want := hookBackgroundJSON(t, stop), `{"other":{"MCP task":1,"monitor":2,"something new":1,"workflow":1}}`; got != want {
		t.Errorf("claude Stop: %s, want %s", got, want)
	}
}

// Antigravity's Stop says only whether all its background work is done
// (fullyIdle): not done is one of it, said as background work.
func TestAntigravitysStopSaysOnlyWhetherItIsIdle(t *testing.T) {
	for _, c := range []struct{ name, payload, want string }{
		{"busy", `{"executionNum":1,"terminationReason":"NO_TOOL_CALL","error":"","fullyIdle":false}`, `{"other":{"background work":1}}`},
		{"idle", `{"executionNum":2,"terminationReason":"NO_TOOL_CALL","fullyIdle":true}`, `{}`},
		{"not said", `{"executionNum":3,"terminationReason":"ERROR"}`, `null`},
	} {
		if got := hookBackgroundJSON(t, c.payload); got != c.want {
			t.Errorf("agy %s: %s, want %s", c.name, got, c.want)
		}
	}
}
