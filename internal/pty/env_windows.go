// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build windows

package pty

// A session's shell should have the environment a terminal opened right now
// would have — not the one its launcher happened to inherit.
//
// On Unix reminal spawns a LOGIN shell for exactly this reason: it re-sources
// the profile chain, so PATH matches a freshly-opened terminal even when the
// agent was started by launchd with a bare environment. Windows has no such
// mechanism. A process inherits its parent's environment block, frozen at the
// moment that parent started, and nothing re-reads it.
//
// So a terminal that has been open since before an installer ran hands every
// reminal session it starts a stale PATH — and the session keeps handing it to
// every shell after that. Installing Claude Code and finding `claude` "not
// recognized" in session after session, while the registry plainly contains the
// entry, is that bug: the fix looked applied everywhere except where it counted.
//
// Explorer avoids this by rebuilding its environment on WM_SETTINGCHANGE, which
// is why a terminal opened fresh from the Start menu is fine. We rebuild ours
// from the same source Explorer reads: the persistent environment in the
// registry.

import "golang.org/x/sys/windows/registry"

const (
	machineEnvKey = `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`
	userEnvKey    = `Environment`
	// volatileEnvKey is what this logon set: USERNAME, USERPROFILE, APPDATA,
	// LOCALAPPDATA, HOMEDRIVE/HOMEPATH. Explorer folds it in last.
	volatileEnvKey = `Volatile Environment`
)

// freshenEnv returns env with the persistent (registry) environment folded in.
// Values there win over the inherited copy — they are the current truth, and the
// inherited one is only as new as whatever started us.
//
// Variables the registry says nothing about are kept untouched: the volatile
// ones a process cannot do without (SystemRoot, TEMP, USERPROFILE, the
// session's own REMINAL_* additions) live only in the inherited block.
func freshenEnv(env []string) []string {
	machine := readEnvKey(registry.LOCAL_MACHINE, machineEnvKey)
	user := readEnvKey(registry.CURRENT_USER, userEnvKey)
	if len(machine) == 0 && len(user) == 0 {
		return env // registry unreadable: inherited is all we have, and it works
	}
	return mergeEnv(env, machine, user, readEnvKey(registry.CURRENT_USER, volatileEnvKey))
}

// readEnvKey reads one environment key, expanding REG_EXPAND_SZ values the way
// the loader would. Best-effort: an unreadable key yields nothing and leaves the
// inherited environment in place.
func readEnvKey(root registry.Key, path string) map[string]string {
	k, err := registry.OpenKey(root, path, registry.QUERY_VALUE)
	if err != nil {
		return nil
	}
	defer k.Close()
	names, err := k.ReadValueNames(0)
	if err != nil {
		return nil
	}
	out := make(map[string]string, len(names))
	for _, n := range names {
		v, kind, err := k.GetStringValue(n)
		if err != nil {
			continue // non-string values are not environment variables
		}
		if kind == registry.EXPAND_SZ {
			if e, err := registry.ExpandString(v); err == nil {
				v = e
			}
		}
		out[n] = v
	}
	return out
}
