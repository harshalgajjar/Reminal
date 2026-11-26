// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"reminal/internal/atomicfile"
	"reminal/internal/client"
)

// A reminal that learns of a new event (Qwen Code's StopFailure, Claude
// Code's before it) wrote its hook only when `reminal integrate` was run,
// and a person who set an agent up once does not run it again: the upgrade
// reached them without it. So what an upgrade added is written without
// integrate, for each agent whose hooks file already holds reminal's own
// entries (one with none was not set up, or had them taken out, and is left
// alone), calling the reminal the entries there call:
//
//   - an event every version of the agent has: as the upgraded reminal
//     starts (`integrate --heal-plugins`, which the daemon and a session run
//     as they start);
//   - an event only a new enough agent has (hookEvent.MinVersion): never on
//     the word of the agent found on PATH, which may not be the one a
//     session runs. Only when a session says what version the agent in it
//     is, as that agent leaves for its shell (`integrate --heal-hooks
//     --agent-version=…`, where an entry too new for it is taken out too).
//
// Nothing but the missing entries is added, and with none missing the
// agent's file is not written at all. The hook takes effect the next time
// the agent starts.
//
// An entry the person took out stays out. reminal keeps, in its own folder
// (~/.reminal/hooks-written.json, never the agent's file), which events it
// has written for each agent: integrate and this heal both note them. Only
// an event not in that note is added, so one that was written and is gone
// from the file was taken out by the person; `reminal integrate` writes them
// all again. An install from before the note was kept has none: the events
// its file holds are taken as the ones written, the others as new to it. An
// entry taken out because its agent was too old is noted as held for that
// version, not forgotten: it is written again only when a session's agent
// is new enough, so the two never take turns.

// hooksNote is what reminal has written for one agent: the events, and the
// ones it took out again for an agent too old, by the version that was.
type hooksNote struct {
	Wrote []string          `json:"wrote"`
	Held  map[string]string `json:"held,omitempty"`
}

// UnmarshalJSON reads a note as reminal writes it, and as a build of
// before wrote it: the events alone, as a list.
func (n *hooksNote) UnmarshalJSON(b []byte) error {
	var list []string
	if json.Unmarshal(b, &list) == nil {
		*n = hooksNote{Wrote: list}
		return nil
	}
	type plain hooksNote
	var v plain
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*n = hooksNote(v)
	return nil
}

func (n hooksNote) wrote(event string) bool {
	for _, ev := range n.Wrote {
		if ev == event {
			return true
		}
	}
	return false
}

func hooksWrittenPath(home string) string {
	return filepath.Join(home, ".reminal", "hooks-written.json")
}

// hooksNoteMax bounds the note read back.
const hooksNoteMax = 64 << 10

// readHooksNotes is the note, by agent. No file is no note. One that cannot
// be read as what reminal writes is an error: it is left as it is, and
// nothing is decided on it.
func readHooksNotes(home string) (map[string]hooksNote, error) {
	all := map[string]hooksNote{}
	b, err := os.ReadFile(hooksWrittenPath(home))
	if os.IsNotExist(err) {
		return all, nil
	}
	if err != nil {
		return nil, err
	}
	if len(b) > hooksNoteMax {
		return nil, errors.New("hooks-written.json is too large")
	}
	if err := json.Unmarshal(b, &all); err != nil {
		return nil, err
	}
	return all, nil
}

func writeHooksNotes(home string, all map[string]hooksNote) {
	b, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(hooksWrittenPath(home)), 0o700) == nil {
		_ = atomicfile.Write(hooksWrittenPath(home), append(b, '\n'), 0o600)
	}
}

// changeHooksNote reads the note, lets change alter bin's part, and writes
// it back when it differs. A note that cannot be read is left alone, unless
// anew: then it is begun again from nothing (integrate, which writes every
// event, so what the old note said of this agent is not needed; without
// that an unreadable note would stop the heal for good).
func changeHooksNote(home, bin string, anew bool, change func(n *hooksNote) (keep bool)) {
	all, err := readHooksNotes(home)
	if err != nil {
		if !anew {
			return
		}
		all = map[string]hooksNote{}
	}
	was, _ := json.Marshal(all)
	if err != nil {
		was = nil // whatever is there is written over
	}
	n := all[bin]
	if change(&n) {
		sort.Strings(n.Wrote)
		if len(n.Held) == 0 {
			n.Held = nil
		}
		all[bin] = n
	} else {
		delete(all, bin)
	}
	if now, _ := json.Marshal(all); string(now) != string(was) {
		writeHooksNotes(home, all)
	}
}

// setHooksWritten notes events as the ones written for bin (integrate, which
// writes them all, whatever was held or taken out); with none, the note for
// bin is dropped (a remove).
func setHooksWritten(home, bin string, events []string) {
	changeHooksNote(home, bin, true, func(n *hooksNote) bool {
		*n = hooksNote{Wrote: append([]string(nil), events...)}
		return len(events) > 0
	})
}

// addHooksWritten notes events as written for bin, beside those noted; one
// that was held is held no longer.
func addHooksWritten(home, bin string, events []string) {
	if len(events) == 0 {
		return
	}
	changeHooksNote(home, bin, false, func(n *hooksNote) bool {
		for _, ev := range events {
			if !n.wrote(ev) {
				n.Wrote = append(n.Wrote, ev)
			}
			delete(n.Held, ev)
		}
		return true
	})
}

// holdHookWritten notes that event was taken out for bin because its agent,
// at version, is too old for it: not the person's doing, and not to be
// written again until a session's agent is new enough.
func holdHookWritten(home, bin, event, version string) {
	changeHooksNote(home, bin, false, func(n *hooksNote) bool {
		kept := n.Wrote[:0:0]
		for _, ev := range n.Wrote {
			if ev != event {
				kept = append(kept, ev)
			}
		}
		n.Wrote = kept
		if n.Held == nil {
			n.Held = map[string]string{}
		}
		n.Held[event] = version
		return true
	})
}

// hookPathNever are characters a path taken from a file is not trusted
// with: what a shell would expand or read as more than a name.
const hookPathNever = "{}*?~!#$`;&|<>()\"'\\\n"

// unquoteHookExe is the path a hook command's reminal names, its quotes
// off.
func unquoteHookExe(q string) string {
	if len(q) >= 2 && q[0] == '\'' && q[len(q)-1] == '\'' {
		return strings.ReplaceAll(q[1:len(q)-1], `'\''`, `'`)
	}
	if len(q) >= 2 && q[0] == '"' && q[len(q)-1] == '"' {
		return q[1 : len(q)-1]
	}
	return q
}

var windowsDrivePath = regexp.MustCompile(`^[A-Za-z]:[/\\]`)

// ownHookCalls is reminal as its own entries in spec's file name it, quoted
// as they are, for an entry added beside them: the one that names self (this
// reminal) when one does; else one given by a whole path with nothing in it
// a shell would read as more than a name; else "" (nothing is added).
func ownHookCalls(spec *hookSpec, home, self string) string {
	raw, err := os.ReadFile(filepath.Join(home, spec.file))
	if err != nil {
		return ""
	}
	root := map[string]any{}
	if json.Unmarshal(raw, &root) != nil {
		return ""
	}
	node := root
	for _, k := range spec.key {
		next, ok := node[k].(map[string]any)
		if !ok {
			return ""
		}
		node = next
	}
	same := func(a, b string) bool {
		if a == "" || b == "" {
			return false
		}
		if ra, err := filepath.EvalSymlinks(a); err == nil {
			a = ra
		}
		if rb, err := filepath.EvalSymlinks(b); err == nil {
			b = rb
		}
		return filepath.Clean(filepath.FromSlash(a)) == filepath.Clean(filepath.FromSlash(b))
	}
	plain := ""
	for _, ev := range spec.events {
		list, _ := node[ev.Event].([]any)
		for _, item := range list {
			m, ok := item.(map[string]any)
			if !ok || !ourHookEntry(m) {
				continue
			}
			cmd, _ := m["command"].(string)
			if cmd == "" {
				cmd, _ = m["bash"].(string)
			}
			if cmd == "" {
				if hs, _ := m["hooks"].([]any); len(hs) > 0 {
					if h, ok := hs[0].(map[string]any); ok {
						cmd, _ = h["command"].(string)
					}
				}
			}
			if !reminalHookCmdRe.MatchString(cmd) {
				continue
			}
			cmd = strings.TrimSpace(cmd)
			at := strings.LastIndex(cmd, " hook ")
			if at <= 0 {
				continue
			}
			quoted := cmd[:at]
			path := unquoteHookExe(quoted)
			if same(path, self) {
				return quoted
			}
			whole := filepath.IsAbs(path) || windowsDrivePath.MatchString(path)
			if plain == "" && whole && !strings.ContainsAny(path, hookPathNever) {
				plain = quoted
			}
		}
	}
	return plain
}

// hooksHealLock is the lock the heal runs under: the daemon and a session
// start at once after an upgrade, and each reads and writes the same files.
const hooksHealLock = "hooks-heal.lock"

// withHooksHealLock runs fn holding that lock (a var so a test can stand in
// for another reminal holding it).
var withHooksHealLock = func(fn func()) { client.WithFileLock(hooksHealLock, fn) }

// healAddedHooks writes reminal's hooks that are missing for the agents
// already set up (see above), and says which it wrote, one line an agent.
// said: what a session said the version of its agent is, by agent; nil at
// reminal's start, when only events every version has are written. With
// said, only the agents it names are looked at.
func healAddedHooks(home, self string, said func(bin string) string) []string {
	var lines []string
	withHooksHealLock(func() { lines = healAddedHooksLocked(home, self, said) })
	return lines
}

func healAddedHooksLocked(home, self string, said func(bin string) string) []string {
	notes, err := readHooksNotes(home)
	if err != nil {
		return nil // a note that cannot be read decides nothing
	}
	var lines []string
	for _, t := range agentTargets() {
		if t.hooks == nil {
			continue
		}
		have := ""
		if said != nil {
			if have = said(t.Bin); have == "" {
				continue
			}
		}
		calls := ownHookCalls(t.hooks, home, self)
		if calls == "" {
			continue // none of reminal's entries: not set up, or taken out
		}
		note, noted := notes[t.Bin]
		var missing []hookEvent
		var names, there []string
		for _, ev := range t.hooks.events {
			if ownHookWritten(t.hooks, home, ev.Event) {
				there = append(there, ev.Event)
				continue
			}
			if noted && note.wrote(ev.Event) {
				continue // written before and gone: the person took it out
			}
			if ev.MinVersion != "" && (said == nil || !hookEventFits(ev, have)) {
				continue // only on a session's word, for an agent new enough
			}
			missing = append(missing, ev)
			names = append(names, ev.Event)
		}
		if len(missing) == 0 {
			// The entries there are noted all the same (an install from
			// before the note was kept).
			addHooksWritten(home, t.Bin, there)
			continue
		}
		add := *t.hooks
		add.events, add.version, add.calls = missing, have, calls
		// Nothing at the file's top is cleaned up here: only entries added.
		add.cleanTop = false
		if err := applyHooks(&add, home, "", false); err != nil {
			lines = append(lines, fmt.Sprintf("reminal could not add its %s hook for %s: %v", strings.Join(names, ", "), t.Name, err))
			continue
		}
		addHooksWritten(home, t.Bin, append(there, names...))
		lines = append(lines, fmt.Sprintf("reminal added its %s hook for %s: it takes effect the next time %s starts", strings.Join(names, ", "), t.Name, t.Name))
	}
	return lines
}
