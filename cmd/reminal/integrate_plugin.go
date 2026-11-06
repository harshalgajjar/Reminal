// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"reminal/internal/atomicfile"
)

// pluginFile is an agent's hooks when they are a plugin it loads rather than
// entries in its config (OpenCode): one file reminal writes, which reports
// the session's attention state and its conversation as hooks do, through
// `reminal hook`. Removed only while it is still reminal's (pluginMark).
type pluginFile struct {
	path string                  // relative to $HOME
	body func(exe string) string // the plugin, calling reminal at exe
}

// pluginMark opens every plugin reminal writes: a file without it is the
// person's own, never overwritten or removed.
const pluginMark = "// reminal's plugin, written by `reminal integrate`"

// healPlugins brings reminal's own agent plugins up to date: one an older
// reminal wrote stays where the agent loads it when reminal is upgraded, so
// a fix in it would reach no one. Each that is reminal's (pluginMark) and is
// not what this binary writes is written again, for the reminal it already
// calls; a file that is not reminal's, or names no reminal, is left as it
// is. It says which it wrote.
func healPlugins(home string) []string {
	var healed []string
	for _, t := range agentTargets() {
		if t.plugin == nil {
			continue
		}
		path := filepath.Join(home, t.plugin.path)
		b, err := os.ReadFile(path)
		if err != nil || !strings.HasPrefix(string(b), pluginMark) {
			continue
		}
		bin := pluginBin(string(b))
		if bin == "" || string(b) == t.plugin.body(bin) {
			continue
		}
		if atomicfile.Write(path, []byte(t.plugin.body(bin)), 0o644) == nil {
			healed = append(healed, t.plugin.path)
		}
	}
	return healed
}

// pluginBinRe is the line a plugin names reminal on (opencodePlugin's BIN).
var pluginBinRe = regexp.MustCompile(`(?m)^const BIN = ("(?:[^"\\\n]|\\.)*");$`)

// pluginBin is the reminal a plugin reminal wrote calls, or "".
func pluginBin(src string) string {
	m := pluginBinRe.FindStringSubmatch(src)
	if m == nil {
		return ""
	}
	var bin string
	if json.Unmarshal([]byte(m[1]), &bin) != nil {
		return ""
	}
	return bin
}

// pluginCurrent says whether the plugin in place is the one this binary
// writes for exe, byte for byte.
func pluginCurrent(p *pluginFile, home, exe string) bool {
	b, err := os.ReadFile(filepath.Join(home, p.path))
	return err == nil && string(b) == p.body(exe)
}

func applyPlugin(p *pluginFile, home, exe string, remove bool) error {
	path := filepath.Join(home, p.path)
	old, err := os.ReadFile(path)
	switch {
	case err == nil && !strings.HasPrefix(string(old), pluginMark):
		return errors.New(p.path + " is not reminal's: left as it is")
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return err
	case remove:
		if err != nil {
			return nil
		}
		return os.Remove(path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return atomicfile.Write(path, []byte(p.body(exe)), 0o644)
}

// opencodePlugin reports what OpenCode's own conversation is doing (not its
// subagents', which run as sessions of their own with a parent) and which
// conversation it is: busy is a turn, idle its end, a permission asked
// waits on the person. Only in a reminal session.
func opencodePlugin(exe string) string {
	bin, _ := json.Marshal(exe)
	return pluginMark + `: what this OpenCode is doing,
// and which conversation it is in, for reminal's list and to resume it.
import { spawn } from "node:child_process";

const BIN = ` + string(bin) + `;

export const Reminal = async () => {
	if (!process.env.REMINAL_SESSION) return {};
	let conv = "";
	const children = new Set();
	// One ` + "`reminal hook`" + ` at a time, and only ever the newest state: each is a
	// process of its own writing the same file, the last to write wins, and
	// a turn's end comes right behind its busy reports. A report made while
	// one is in flight waits, replacing any older one waiting. One that has
	// not finished in HOOK_WAIT (held on a lock, a hung disk) is ended, so it
	// can neither hold back every report after it nor land after them.
	const HOOK_WAIT = 5000;
	let writing = false;
	let parked = "";
	const report = (state) => {
		if (writing) {
			parked = state;
			return;
		}
		writing = true;
		let released = false;
		let timer;
		const next = () => {
			if (released) return;
			released = true;
			clearTimeout(timer);
			writing = false;
			const state = parked;
			parked = "";
			if (state) report(state);
		};
		try {
			const p = spawn(BIN, ["hook", state], { stdio: ["pipe", "ignore", "ignore"], detached: true });
			timer = setTimeout(() => {
				try {
					p.kill("SIGKILL");
				} catch {
					// already gone
				}
				next();
			}, HOOK_WAIT);
			timer.unref?.();
			p.on("error", next);
			p.on("exit", next);
			p.stdin.on("error", () => {});
			p.stdin.end(conv ? JSON.stringify({ session_id: conv }) : "");
			p.unref();
		} catch {
			// no reminal to tell: OpenCode carries on
			next();
		}
	};
	return {
		event: async ({ event }) => {
			const props = event.properties ?? {};
			if (event.type === "session.created" && props.info?.parentID) children.add(props.info.id);
			const id = props.sessionID;
			if (!id || children.has(id)) return;
			switch (event.type) {
				case "session.status":
					conv = id;
					report(props.status?.type === "idle" ? "done" : "working");
					break;
				case "session.idle":
					conv = id;
					report("done");
					break;
				case "permission.asked":
				case "permission.updated":
					report("input");
					break;
				case "permission.replied":
					report("working");
					break;
			}
		},
	};
};
`
}
