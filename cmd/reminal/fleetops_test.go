// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"errors"
	"testing"
)

func TestParseMachineScope(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		selector string
		allOwned bool
		remote   bool
		wantErr  bool
	}{
		{name: "no flags is local", args: []string{}},
		{name: "local restart flags stay local", args: []string{"--all"}},
		{name: "machine space value", args: []string{"--machine", "box"}, selector: "box", remote: true},
		{name: "machine equals value", args: []string{"--machine=box"}, selector: "box", remote: true},
		{name: "short -m", args: []string{"-m", "box"}, selector: "box", remote: true},
		{name: "all owned", args: []string{"--all-owned-machines"}, allOwned: true, remote: true},
		// A following flag must not become the machine name — the same guard the
		// session verbs use, so `--machine --all` errors instead of targeting a
		// machine literally named "--all".
		{name: "machine swallowing a flag errors", args: []string{"--machine", "--all"}, wantErr: true},
		{name: "bare machine errors", args: []string{"--machine"}, wantErr: true},
		{name: "machine plus all-owned is refused", args: []string{"--machine", "box", "--all-owned-machines"}, wantErr: true},
		// Asking for help is never a scope: "upgrade --help" must not upgrade.
		{name: "help is not a scope", args: []string{"--help"}, wantErr: true},
		{name: "-h is not a scope", args: []string{"-h"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sc, err := parseMachineScope(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got scope %+v", sc)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if sc.selector != tc.selector {
				t.Errorf("selector = %q, want %q", sc.selector, tc.selector)
			}
			if sc.allOwned != tc.allOwned {
				t.Errorf("allOwned = %v, want %v", sc.allOwned, tc.allOwned)
			}
			if got := sc.remote(); got != tc.remote {
				t.Errorf("remote() = %v, want %v", got, tc.remote)
			}
		})
	}
}

// A verb with no flags of its own (upgrade) refuses anything it does not
// know, so a typo or a stray flag never turns into an upgrade.
func TestStrictScopeRefusesUnknownFlags(t *testing.T) {
	for _, args := range [][]string{{"--al-owned"}, {"--all"}, {"now"}} {
		if _, err := parseMachineScopeStrict(args); err == nil {
			t.Errorf("%v: want an error", args)
		}
	}
	if sc, err := parseMachineScopeStrict([]string{"--machine", "box"}); err != nil || sc.selector != "box" {
		t.Fatalf("a known flag still parses: %+v %v", sc, err)
	}
	if _, err := parseMachineScope([]string{"--all", "--help"}); !errors.Is(err, errScopeHelp) {
		t.Fatalf("asking for help is its own answer, got %v", err)
	}
}
