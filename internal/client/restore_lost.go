// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"reminal/internal/session"
)

// lostAgentNote is what a restore says about an agent its record no longer
// names. A record that names none is a session with nothing to start again,
// and its restore typed nothing; but when the session's own conversation file
// (.conv, which the agent's hooks write) still names a conversation, an agent
// was running in it, and the record lost it. Seen on a Mac that came back
// after a crash with 12 sessions at a bare prompt, each with its conversation
// still named and resumable by hand. That is said, with the command that
// resumes it, ready to paste: shown, never typed, since a person may have
// quit the agent on purpose. Nothing when the conversation is not known to
// an agent here, or another session holds it too ("" either way leaves the
// restore as it was).
func lostAgentNote(r session.Restore, peers []session.Restore, shell string) string {
	if r.Fg != "" {
		return ""
	}
	conv := session.ReadConv(r.ID)
	if !session.ValidConv(conv) {
		return ""
	}
	for _, p := range peers {
		if p.ID != r.ID && (p.Conv == conv || session.ReadConv(p.ID) == conv) {
			return "a coding agent was running here in a conversation another session also last had (" + conv + "); reminal did not start it again"
		}
	}
	agent := convAgent(conv)
	if agent == "" {
		return ""
	}
	cmd := shellCommand(resumers[agent].byID(agent, nil, conv), shell)
	if dir := convCwd(agent, conv); dir != "" && dir != r.Cwd {
		// Claude Code keeps a conversation by the folder it ran in: resumed
		// from another, it is "no conversation found".
		cmd = shellCommand([]string{"cd", dir}, shell) + " && " + cmd
	}
	return agent + " was running here, but this session's saved record had lost it, so reminal did not start it again; to resume it: " + cmd
}

// convAgent is the agent here that keeps a file for conversation conv, or ""
// when none does (or none can be looked in). By name, so the answer is the
// same every time.
func convAgent(conv string) string {
	names := make([]string, 0, len(resumers))
	for name, rs := range resumers {
		if rs.byID != nil && rs.store.file != nil {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		if convSize(name, conv) > 0 {
			return name
		}
	}
	return ""
}

// convCwd is the folder agent's conversation conv ran in, as its own file
// says: Claude Code writes it into each entry ("cwd"). "" when not known.
func convCwd(agent, conv string) string {
	if agent != "claude" {
		return ""
	}
	matches, _ := filepath.Glob(claudeConvFile(conv))
	if len(matches) != 1 {
		return "" // two projects with one id: no telling which
	}
	f, err := os.Open(matches[0])
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for i := 0; i < 50 && sc.Scan(); i++ {
		var e struct {
			Cwd string `json:"cwd"`
		}
		// Shown on the screen: nothing a terminal would act on.
		if json.Unmarshal(sc.Bytes(), &e) == nil && filepath.IsAbs(e.Cwd) && !hasControl(e.Cwd) {
			if fi, err := os.Stat(e.Cwd); err == nil && fi.IsDir() {
				return e.Cwd
			}
		}
	}
	return ""
}
