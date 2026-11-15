// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package pty

import (
	"os"
	"strings"
)

// hookProbeEnv is session.HookProbeEnv: the mark of a program reminal only
// asks something, under which hooks write nothing. A session started from
// such a program (or from anything that inherited the mark) is a real one,
// and its shell must not carry it, or its agent's hooks would say nothing.
const hookProbeEnv = "REMINAL_HOOK_PROBE"

// shellEnv builds the inner shell's environment. It forces TERM and DISABLES
// macOS Terminal's shell session save/restore: a reminal shell isn't a
// Terminal.app window, but it inherits Terminal's SHELL_SESSION_FILE, so every
// spawned shell would source (and re-save) the same session file — which
// corrupts it and surfaces as "…/.zsh_sessions/….session: command not found:
// Saving" at the top of a new session. SHELL_SESSIONS_DISABLE=1 is Apple's
// documented off-switch; it's a harmless no-op on Linux and Windows.
func shellEnv(extra []string) []string {
	base := os.Environ()
	out := make([]string, 0, len(base)+len(extra)+2)
	for _, kv := range base {
		if strings.HasPrefix(kv, "SHELL_SESSION_FILE=") || strings.HasPrefix(kv, "SHELL_SESSIONS_DISABLE=") {
			continue // drop the inherited pointer + any prior flag; set a clean one below
		}
		if strings.HasPrefix(kv, hookProbeEnv+"=") {
			continue // a probe's mark (session.HookProbeEnv) never enters a session
		}
		if strings.HasPrefix(kv, ShellCgroupEnv+"=") {
			continue // the shell's cgroup is where it runs, not something for it to read
		}
		out = append(out, kv)
	}
	out = append(out, "TERM=xterm-256color", "SHELL_SESSIONS_DISABLE=1")
	// Refresh from the persistent environment before the session-specific
	// extras go on, so REMINAL_* additions are never overwritten by it.
	return append(freshenEnv(out), extra...)
}

// mergeEnv folds a persistent environment (Windows' registry keys) into an
// inherited one, in the order a logon builds it: the machine's, the user's,
// then the logon's own (volatile: USERNAME, USERPROFILE, APPDATA and the
// like). Split from the registry read so the merge — the part with the sharp
// edges — is testable on any platform.
func mergeEnv(env []string, machine, user, volatile map[string]string) []string {
	// Case-insensitive: Windows environment names are, so a "Path" written by
	// one tool must still override a "PATH" inherited from another.
	replace := map[string]string{}
	for k, v := range machine {
		// The machine key's USERNAME is a placeholder, SYSTEM, for processes
		// with no logon; a logon replaces it with its own. Taken from here it
		// made every session's shell SYSTEM's.
		if strings.EqualFold(k, "USERNAME") {
			continue
		}
		replace[strings.ToUpper(k)] = v
	}
	for k, v := range user {
		replace[strings.ToUpper(k)] = v
	}
	for k, v := range volatile {
		replace[strings.ToUpper(k)] = v
	}
	if p := pathFor(machine, user); p != "" {
		replace["PATH"] = p
	}

	out := make([]string, 0, len(env)+len(replace))
	seen := map[string]bool{}
	for _, kv := range env {
		name, _, ok := strings.Cut(kv, "=")
		if !ok {
			out = append(out, kv)
			continue
		}
		up := strings.ToUpper(name)
		v, found := replace[up]
		if !found {
			out = append(out, kv)
			continue
		}
		if seen[up] {
			continue // a duplicate of one already refreshed
		}
		seen[up] = true
		out = append(out, name+"="+v)
	}
	// Anything the registry defines that was never inherited at all.
	for up, v := range replace {
		if !seen[up] {
			out = append(out, up+"="+v)
		}
	}
	return out
}

// pathFor builds PATH the way a logon does: the machine's entries first, then
// the user's appended. Concatenated rather than overridden — a user PATH does
// not replace the system one, and treating it as a plain variable would drop
// System32 out from under the shell.
func pathFor(machine, user map[string]string) string {
	m := strings.Trim(lookupFold(machine, "Path"), "; ")
	u := strings.Trim(lookupFold(user, "Path"), "; ")
	switch {
	case m == "" && u == "":
		return ""
	case m == "":
		return u
	case u == "":
		return m
	}
	return m + ";" + u
}

func lookupFold(m map[string]string, name string) string {
	for k, v := range m {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}
