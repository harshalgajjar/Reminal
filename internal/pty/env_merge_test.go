// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package pty

import (
	"strings"
	"testing"
)

func envMap(env []string) map[string]string {
	out := map[string]string{}
	for _, kv := range env {
		if name, v, ok := strings.Cut(kv, "="); ok {
			out[strings.ToUpper(name)] = v
		}
	}
	return out
}

// TestMergeEnvRefreshesStalePath is the regression test for `claude` being "not
// recognized" in session after session while the registry plainly contained the
// entry. A terminal open since before an installer ran holds a frozen
// environment block, and every session started from it inherited the stale PATH.
func TestMergeEnvRefreshesStalePath(t *testing.T) {
	inherited := []string{
		`Path=C:\Windows\system32;C:\Windows`, // as it was when the terminal opened
		`USERPROFILE=C:\Users\harsh`,          // logon-only: not in the registry
		`REMINAL_SESSION=ABC123`,              // the session's own
	}
	machine := map[string]string{"Path": `C:\Windows\system32;C:\Windows`}
	user := map[string]string{"Path": `C:\Users\harsh\.local\bin`}

	got := envMap(mergeEnv(inherited, machine, user, nil))

	want := `C:\Windows\system32;C:\Windows;C:\Users\harsh\.local\bin`
	if got["PATH"] != want {
		t.Errorf("PATH = %q, want %q", got["PATH"], want)
	}
	if got["USERPROFILE"] != `C:\Users\harsh` {
		t.Error("dropped a logon-only variable the registry never defines")
	}
	if got["REMINAL_SESSION"] != "ABC123" {
		t.Error("clobbered the session's own variable")
	}
}

// The user PATH is APPENDED to the machine one, never substituted for it —
// treating it as an ordinary variable would take System32 out from under the
// shell, which is the difference between a stale PATH and an unusable one.
func TestMergeEnvPathIsMachineThenUser(t *testing.T) {
	cases := []struct {
		name          string
		machine, user string
		want          string
	}{
		{"both", `C:\Windows\system32`, `C:\tools`, `C:\Windows\system32;C:\tools`},
		{"machine only", `C:\Windows\system32`, "", `C:\Windows\system32`},
		{"user only", "", `C:\tools`, `C:\tools`},
		{"trailing separators", `C:\Windows\system32;`, `;C:\tools`, `C:\Windows\system32;C:\tools`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, u := map[string]string{}, map[string]string{}
			if tc.machine != "" {
				m["Path"] = tc.machine
			}
			if tc.user != "" {
				u["Path"] = tc.user
			}
			if got := pathFor(m, u); got != tc.want {
				t.Errorf("pathFor = %q, want %q", got, tc.want)
			}
		})
	}
}

// Windows environment names are case-insensitive, so a registry "Path" has to
// override an inherited "PATH" — and must not leave both behind, which would
// make the shell's PATH depend on which copy it read first.
func TestMergeEnvIsCaseInsensitive(t *testing.T) {
	merged := mergeEnv(
		[]string{`PATH=C:\old`, `TeMp=C:\old\temp`},
		map[string]string{"Path": `C:\new`},
		map[string]string{"TMP": `C:\new\tmp`, "Temp": `C:\new\temp`},
		nil,
	)
	var paths, temps int
	for _, kv := range merged {
		switch {
		case strings.HasPrefix(strings.ToUpper(kv), "PATH="):
			paths++
		case strings.HasPrefix(strings.ToUpper(kv), "TEMP="):
			temps++
		}
	}
	if paths != 1 {
		t.Errorf("PATH appears %d times; the shell's would depend on read order", paths)
	}
	if temps != 1 {
		t.Errorf("TEMP appears %d times", temps)
	}
	got := envMap(merged)
	if got["PATH"] != `C:\new` {
		t.Errorf("PATH = %q, want the registry's value", got["PATH"])
	}
	if got["TEMP"] != `C:\new\temp` {
		t.Errorf("TEMP = %q, want the registry's value", got["TEMP"])
	}
}

// A variable that exists only in the registry has to arrive, or a freshly-set
// one (an API key, a proxy) would never reach the session.
func TestMergeEnvAddsNewVariables(t *testing.T) {
	got := envMap(mergeEnv(
		[]string{`USERPROFILE=C:\Users\harsh`},
		nil,
		map[string]string{"ANTHROPIC_BASE_URL": "https://example.test"},
		nil,
	))
	if got["ANTHROPIC_BASE_URL"] != "https://example.test" {
		t.Error("a variable defined only in the registry never reached the shell")
	}
}

// The machine key's USERNAME is SYSTEM, a placeholder for processes with no
// logon. Folded in like any other variable it made every session's shell
// SYSTEM's; the inherited one is the person's.
func TestMergeEnvKeepsThePersonsUsername(t *testing.T) {
	machine := map[string]string{"USERNAME": "SYSTEM", "OS": "Windows_NT"}
	got := envMap(mergeEnv([]string{"USERNAME=alice"}, machine, nil, nil))
	if got["USERNAME"] != "alice" {
		t.Errorf("USERNAME = %q, want the inherited alice", got["USERNAME"])
	}
	if got["OS"] != "Windows_NT" {
		t.Errorf("OS = %q: the rest of the machine key must still apply", got["OS"])
	}
}

// The logon's own values (Volatile Environment) fill in what was not
// inherited, and nothing that was: a reminal started with a profile of its
// own (an isolated home, a service account, runas) keeps handing it on.
func TestMergeEnvLogonValuesOnlyFillGaps(t *testing.T) {
	machine := map[string]string{"USERNAME": "SYSTEM"}
	volatile := map[string]string{"USERNAME": "bob", "USERPROFILE": `C:\Users\bob`, "APPDATA": `C:\Users\bob\AppData\Roaming`}
	got := envMap(mergeEnv([]string{`USERPROFILE=C:\iso`}, machine, nil, volatile))
	if got["USERPROFILE"] != `C:\iso` {
		t.Errorf("USERPROFILE = %q: the profile this reminal was started with was replaced", got["USERPROFILE"])
	}
	if got["USERNAME"] != "bob" || got["APPDATA"] != `C:\Users\bob\AppData\Roaming` {
		t.Errorf("USERNAME, APPDATA = %q, %q: the logon's values should fill what was not inherited", got["USERNAME"], got["APPDATA"])
	}
}
