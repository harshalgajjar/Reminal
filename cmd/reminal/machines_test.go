// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"testing"

	"reminal/internal/protocol"
)

func TestSessionLabel(t *testing.T) {
	cases := []struct {
		in   protocol.DirSession
		want string
	}{
		{protocol.DirSession{Kind: "port", Port: 3000}, "port :3000"},
		{protocol.DirSession{Name: "work"}, "work"},
		{protocol.DirSession{Title: "npm run dev"}, "npm run dev"},
		{protocol.DirSession{Name: "work", Title: "vim"}, "work"}, // name wins over title
		{protocol.DirSession{Headless: true}, "background shell"},
		{protocol.DirSession{}, "shell"},
	}
	for _, c := range cases {
		if got := sessionLabel(c.in); got != c.want {
			t.Errorf("sessionLabel(%+v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSessionMeta(t *testing.T) {
	if got := sessionMeta(protocol.DirSession{}); got != "" {
		t.Errorf("empty session should have empty meta, got %q", got)
	}
	if got := sessionMeta(protocol.DirSession{Viewers: 1}); got != "1 viewer" {
		t.Errorf("one viewer: got %q", got)
	}
	if got := sessionMeta(protocol.DirSession{Viewers: 3}); got != "3 viewers" {
		t.Errorf("three viewers: got %q", got)
	}
	if got := sessionMeta(protocol.DirSession{IdleSecs: 120}); got != "idle 2m" {
		t.Errorf("idle: got %q", got)
	}
}
