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
// Returns "" when this process is genuinely not inside a session — and when
// it runs under a probe's mark (HookProbeEnv), its own or any process's it
// meets on the way up before an agent, or one it meets is in a running
// probe's session (RunProbe): a program reminal only asks something is in no
// session, though a session's reminal started it.
func Enclosing() string {
	if InProbe() {
		return ""
	}
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
	// A session's shell stands for the session where the process serving it
	// is not above the shell (Active.ShellPID). Never over a session's own
	// process: a pid is one or the other.
	for _, a := range all {
		if _, taken := agents[a.ShellPID]; a.ShellPID > 0 && a.PID > 0 && !taken {
			agents[a.ShellPID] = a.ID
		}
	}
	probes, probing := probeSessions()
	pid := os.Getpid()
	for i := 0; i < enclosingMaxDepth && pid > 1; i++ {
		if id, ok := agents[pid]; ok {
			return id
		}
		if probeMarked(pid) {
			return "" // started under a probe's mark, or by one
		}
		if sid := processSession(pid); probing[sid] && probeRecordLive(probes, sid) {
			return "" // in a running probe's session, its mark scrubbed or not
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

// NearestSessionPID is the pid of the reminal session nearest above pid (pid
// itself, if it is one): the session it runs in, and not one further out
// that a session it runs in was itself started in. 0 when none.
func NearestSessionPID(pid int) int {
	all, err := ReadAllActive()
	if err != nil {
		return 0
	}
	return nearestOf(pid, sessionPIDs(all))
}

// sessionPIDs maps each pid that marks a session in the process tree to the
// pid of the process serving that session: that process itself, and the
// session's shell where it is recorded (Active.ShellPID). On Windows the
// shell hangs under a holder started by the session's first process; after a
// hot restart the process serving the session is a new one, above nothing
// that runs in it, and only the shell says whose they are.
func sessionPIDs(all []Active) map[int]int {
	agents := map[int]int{}
	for _, a := range all {
		if a.PID > 0 {
			agents[a.PID] = a.PID
		}
	}
	for _, a := range all {
		if _, taken := agents[a.ShellPID]; a.ShellPID > 0 && a.PID > 0 && !taken {
			agents[a.ShellPID] = a.PID
		}
	}
	return agents
}

func nearestOf(pid int, agents map[int]int) int {
	for i := 0; i < enclosingMaxDepth && pid > 1; i++ {
		if at, ok := agents[pid]; ok {
			return at
		}
		next := parentPID(pid)
		if next == pid || next <= 0 {
			return 0
		}
		pid = next
	}
	return 0
}

// ParentPID is pid's parent's pid; 0 when not read.
func ParentPID(pid int) int { return parentPID(pid) }
