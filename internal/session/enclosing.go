// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package session

import "os"

// enclosingMaxDepth bounds the walk. The agent is a handful of hops up at
// most (agent → shell → harness → helper); anything deeper is a runaway or a
// cycle, and this must never spin.
const enclosingMaxDepth = 24

// Enclosing finds the reminal session this process is running inside by walking
// up the process tree until it meets a live agent.
//
// REMINAL_SESSION is the fast path and covers anything the session's shell
// starts. It is NOT enough on its own: a harness may scrub the environment of
// the helpers it spawns — cursor-agent launches MCP servers with no REMINAL_*
// variables at all — and such a helper would otherwise have no idea which
// session it belongs to, so every session-scoped tool it offers silently fails.
// The process tree still says so: the helper is a descendant of the agent that
// owns the PTY, and the agent's pid is in its own active record.
//
// Returns "" when this process is genuinely not inside a session.
func Enclosing() string {
	agents := map[int]string{}
	all, err := ReadAllActive()
	if err != nil {
		return ""
	}
	for _, a := range all {
		if a.PID > 0 {
			agents[a.PID] = a.ID
		}
	}
	pid := os.Getpid()
	for i := 0; i < enclosingMaxDepth && pid > 1; i++ {
		if id, ok := agents[pid]; ok {
			return id
		}
		next := parentPID(pid)
		if next == pid || next <= 0 {
			return "" // no parent, or a self-loop we must not follow
		}
		pid = next
	}
	return ""
}

// selfAncestry is exposed for tests: the pid chain Enclosing walks.
func selfAncestry(start int) []int {
	out := []int{}
	pid := start
	for i := 0; i < enclosingMaxDepth && pid > 1; i++ {
		out = append(out, pid)
		next := parentPID(pid)
		if next == pid || next <= 0 {
			break
		}
		pid = next
	}
	return out
}
