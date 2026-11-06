// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"reminal/internal/session"
)

// An agent whose hooks reminal writes only for versions that have the event
// (Claude Code's StopFailure, 2.1.78 on) may have been taken back below it
// since integrate wrote them: Claude Code 2.1.77 then runs none of its hooks.
// While such an agent is in the session's front, the session asks the very
// binary it runs what version it is (never reminal's own PATH, whose agent
// may be another); as it leaves for its shell, the session asks reminal to
// take its own entry out for that version (`reminal integrate --heal-hooks
// --agent-version=claude=<v>`: nothing else in the file touched), and says
// what it did, once — at the shell's prompt, never over the agent's own
// screen, which redraws over what is written into it. The agent reads its
// settings as it starts: its next start has its hooks again.

// hookHealAgents are the agents whose hooks can be written for a version
// they are not (cmd/reminal: hookEvent.MinVersion).
var hookHealAgents = map[string]bool{"claude": true}

// hookHealWait caps each ask: an abort cap — an agent says its version at
// once, and the heal reads and writes one file.
const hookHealWait = 30 * time.Second

// hookHealed is which agent versions this reminal has had healed for: once
// each — an upgrade or a downgrade is another version, asked about again.
var hookHealed = struct {
	sync.Mutex
	seen map[string]bool
}{seen: map[string]bool{}}

// hookHealVersion is, by agent, the version its binary in this session's
// front said (noteAgentVersion); guarded by Agent.metaMu.
type hookHealVersion map[string]string

// reminalSelf is this reminal's own binary, when it is one (not a test's).
func reminalSelf() (string, bool) {
	exe, err := os.Executable()
	if err != nil || !strings.HasPrefix(filepath.Base(exe), "reminal") {
		return "", false
	}
	return exe, true
}

// noteAgentVersion asks the agent that just came to the front (fg, its
// process group pgrp) its version, by the binary it runs.
func (a *Agent) noteAgentVersion(fg string, pgrp int) {
	if !hookHealAgents[fg] || pgrp <= 0 {
		return
	}
	if _, ok := reminalSelf(); !ok {
		return
	}
	cmd := agentCommandOf(pgrp, fg)
	if len(cmd) == 0 {
		return
	}
	// Asked once for each binary (its path and time; for node, its script's):
	// an agent coming back to the front often (a shell escape, a job
	// switch) is not started again to ask (review).
	key := agentBinaryKey(cmd[len(cmd)-1])
	agentVersions.Lock()
	known, ok := agentVersions.of[key]
	agentVersions.Unlock()
	if ok {
		a.metaMu.Lock()
		if a.hookVersions == nil {
			a.hookVersions = hookHealVersion{}
		}
		a.hookVersions[fg] = known
		a.metaMu.Unlock()
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), hookHealWait)
		defer cancel()
		// Run apart from this session: an agent that does not know the
		// question starts instead, and its hooks are no session's
		// (session.ProbeCommand).
		out, err := session.RunProbe(session.ProbeCommand(ctx, cmd[0], append(cmd[1:], "--version")...), false)
		v := strings.TrimSpace(string(out))
		if err != nil || v == "" || strings.ContainsAny(v, "\n=") || !strings.Contains(v, agentVersionSays[fg]) {
			return // not the agent's own answer (review)
		}
		if key != "" {
			agentVersions.Lock()
			agentVersions.of[key] = v
			agentVersions.Unlock()
		}
		a.metaMu.Lock()
		if a.hookVersions == nil {
			a.hookVersions = hookHealVersion{}
		}
		a.hookVersions[fg] = v
		a.metaMu.Unlock()
	}()
}

// agentVersions is what each agent binary said its version is, by
// agentBinaryKey: asked once while it is the same file.
var agentVersions = struct {
	sync.Mutex
	of map[string]string
}{of: map[string]string{}}

// agentBinaryKey is a file's path and when it was written: "" (never kept)
// if it cannot be read.
func agentBinaryKey(path string) string {
	st, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return path + "@" + st.ModTime().String()
}

// agentCommandOf is how to run the binary process pid runs, for prog: its
// executable when that is prog itself (a native build), or its interpreter
// with the script it was started with (node and prog's script); nil when not
// read — then nothing is asked, and nothing healed.
func agentCommandOf(pid int, prog string) []string {
	exe := processExecPath(pid)
	if exe == "" || !filepath.IsAbs(exe) {
		return nil // never run relative to this reminal's own directory
	}
	named := func(p string) bool {
		return strings.TrimSuffix(strings.ToLower(filepath.Base(p)), ".exe") == prog
	}
	if named(exe) {
		return []string{exe}
	}
	args := processArgs(pid)
	base := strings.ToLower(filepath.Base(exe))
	interpreter := false
	for _, p := range []string{"node", "bun", "deno", "python", "ruby", "perl"} {
		interpreter = interpreter || strings.HasPrefix(base, p)
	}
	// Started by its own name, its binary elsewhere: a native install's
	// ~/.local/bin/claude is a link to ~/.local/share/claude/versions/<v>,
	// the file /proc/<pid>/exe names (review). Never an interpreter: an npm
	// install's node renames its process "claude" (process.title rewrites
	// its command line), and node is not what to ask.
	if len(args) > 0 && named(args[0]) && !interpreter {
		return []string{exe}
	}
	// An interpreter with the agent's script, where its command line still
	// shows it. (On Linux an npm install's does not, so it is not asked —
	// and not healed.)
	if interpreter && len(args) > 1 && filepath.IsAbs(args[1]) && strings.Contains(args[1], prog) {
		return []string{exe, args[1]}
	}
	return nil
}

// agentVersionSays is what an agent's own --version answer names it by:
// one that does not is another program's, and decides nothing.
var agentVersionSays = map[string]string{"claude": "(Claude Code)"}

// healHooksFor asks, once for each version of the agent, when such an agent
// (prev) has just left the front for a shell (fg), and its binary said its
// version while it was there.
func (a *Agent) healHooksFor(prev, fg string) {
	if !hookHealAgents[prev] || !isLoginShell(fg) {
		return
	}
	exe, ok := reminalSelf()
	if !ok {
		return
	}
	a.metaMu.Lock()
	v := a.hookVersions[prev]
	a.metaMu.Unlock()
	if v == "" {
		return
	}
	key := prev + "@" + v
	hookHealed.Lock()
	seen := hookHealed.seen[key]
	hookHealed.seen[key] = true
	hookHealed.Unlock()
	if seen {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), hookHealWait)
		defer cancel()
		out, err := exec.CommandContext(ctx, exe, "integrate", "--heal-hooks", "--agent-version="+prev+"="+v).Output()
		if err != nil || strings.Contains(string(out), "could not remove") {
			// Not done (its file changed meanwhile, or it could not run):
			// asked again the next time it leaves (review).
			hookHealed.Lock()
			delete(hookHealed.seen, key)
			hookHealed.Unlock()
		}
		if err != nil {
			return
		}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				a.broadcastNotice(line)
			}
		}
	}()
}

// healPluginsOnce asks reminal, once in this process, to bring its own agent
// plugins up to date (`reminal integrate --heal-plugins`): a plugin an older
// reminal wrote stays where its agent loads it when reminal is upgraded. Run
// by the binary now at this one's path, which an upgrade replaced: the plugin
// it writes is the new one. In the background, never holding up the start.
var healPluginsOnce sync.Once

// HealOwnPlugins starts that, the first time it is called.
func HealOwnPlugins() {
	healPluginsOnce.Do(func() {
		exe, ok := reminalSelf()
		if !ok {
			return
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), hookHealWait)
			defer cancel()
			_ = exec.CommandContext(ctx, exe, "integrate", "--heal-plugins").Run()
		}()
	})
}
