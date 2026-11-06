// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"encoding/json"
	"maps"
	"os"
	"strings"
	"testing"
	"time"

	"reminal/internal/session"
)

// info --json carries the attention state, so a tool can tell which session
// needs you; a bare shell has none and the fields are left out entirely.
func TestInfoJSONAttn(t *testing.T) {
	since := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	b, err := json.Marshal(InfoJSON(&session.Active{ID: "ABCD2345", PIN: "123456", OpenURL: "http://r/?s=ABCD2345", Attn: "input", AttnSince: since}))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Attn      string    `json:"attn"`
		AttnSince time.Time `json:"attn_since"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Attn != "input" || !got.AttnSince.Equal(since) {
		t.Fatalf("attn = %q since %v, want input since %v (%s)", got.Attn, got.AttnSince, since, b)
	}

	b, _ = json.Marshal(InfoJSON(&session.Active{ID: "ABCD2345", PIN: "123456", OpenURL: "http://r/?s=ABCD2345"}))
	if strings.Contains(string(b), "attn") {
		t.Fatalf("a bare shell should carry no attn fields: %s", b)
	}
}

// A `reminal expose` forward says so, with its port; a shell's record reads as
// it always has, with neither field.
func TestInfoJSONKind(t *testing.T) {
	b, _ := json.Marshal(InfoJSON(&session.Active{ID: "PORT2345", PIN: "123456", OpenURL: "https://port-x.example/", Kind: session.KindPort, Port: 3000}))
	var got struct {
		Kind string `json:"kind"`
		Port int    `json:"port"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Kind != "port" || got.Port != 3000 {
		t.Fatalf("forward: kind %q port %d (%s)", got.Kind, got.Port, b)
	}
	for _, kind := range []string{"", "shell"} {
		b, _ = json.Marshal(InfoJSON(&session.Active{ID: "ABCD2345", PIN: "123456", OpenURL: "http://r/", Kind: kind}))
		if strings.Contains(string(b), `"kind"`) || strings.Contains(string(b), `"port"`) {
			t.Fatalf("shell (kind %q) should carry no kind or port: %s", kind, b)
		}
	}
}

// info --json carries the background work a session's harness still has
// running, as `reminal list` says it: shells, subagents and the rest by kind,
// each kind kept to what may be shown. A bare shell carries none of it.
func TestInfoJSONBackground(t *testing.T) {
	b, _ := json.Marshal(InfoJSON(&session.Active{ID: "ABCD2345", PIN: "123456", OpenURL: "http://r/", Attn: "done",
		Shells: 1, Agents: 2, Other: map[string]int{"background terminal": 1, "moni\x1b[2Jtor": 2}}))
	var got struct {
		Shells int            `json:"shells"`
		Agents int            `json:"agents"`
		Other  map[string]int `json:"other"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if want := map[string]int{"background terminal": 1, "moni[2Jtor": 2}; got.Shells != 1 || got.Agents != 2 || !maps.Equal(got.Other, want) {
		t.Fatalf("background: %+v, want 1 shell, 2 agents, %v (%s)", got, want, b)
	}
	b, _ = json.Marshal(InfoJSON(&session.Active{ID: "ABCD2345", PIN: "123456", OpenURL: "http://r/"}))
	for _, f := range []string{`"shells"`, `"agents"`, `"other"`} {
		if strings.Contains(string(b), f) {
			t.Fatalf("a bare shell should carry no %s: %s", f, b)
		}
	}
}

// The directory row this machine answers with carries the same background
// work, so another machine's agents see it too.
func TestLocalDirSessionsCarryBackground(t *testing.T) {
	isolateHome(t)
	if err := session.WriteActive(session.Active{ID: "SHEL5678", PIN: "123456", PID: os.Getpid(), Attn: "done",
		Shells: 1, Agents: 2, Other: map[string]int{"monitor": 3}}); err != nil {
		t.Fatal(err)
	}
	rows := localDirSessions()
	if len(rows) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
	if r := rows[0]; r.Shells != 1 || r.Agents != 2 || !maps.Equal(r.Other, map[string]int{"monitor": 3}) {
		t.Fatalf("row = %+v, want 1 shell, 2 agents, 3 monitors", r)
	}
}
