// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"

	"reminal/internal/atomicfile"
)

// An agent's version can change after integrate wrote its hooks: a Claude
// Code upgraded past 2.1.78 has no StopFailure entry yet (degraded: a refused
// turn is read from the screen), and one taken back below it holds an entry
// for an event it does not know — and Claude Code 2.1.77 then runs none of
// its hooks at all. The first is said (integrate --check: not set up, so the
// Machines view offers to set it up again); the second is healed, at the
// session's claude leaving its foreground (`integrate --heal-hooks`, which
// the session runs; it reads its settings as it starts, so the next start
// has its hooks again):
// reminal's own entry for the event taken out, nothing else in the file
// touched, and said once.

// hookVersionMismatch says whether spec's events that need an agent version
// are written as they should be for an agent of version have: one written
// for an agent too old for it, or one not written for an agent new enough.
// An agent whose version was not read says nothing.
func hookVersionMismatch(spec *hookSpec, home, have string) bool {
	if have == "" {
		return false
	}
	for _, ev := range spec.events {
		if ev.MinVersion == "" {
			continue
		}
		if ownHookWritten(spec, home, ev.Event) != hookEventFits(ev, have) {
			return true
		}
	}
	return false
}

// ownHookWritten says whether the agent's hooks file holds one of reminal's
// entries for event.
func ownHookWritten(spec *hookSpec, home, event string) bool {
	raw, err := os.ReadFile(filepath.Join(home, spec.file))
	if err != nil {
		return false
	}
	root := map[string]any{}
	if json.Unmarshal(raw, &root) != nil {
		return false
	}
	node := root
	for _, k := range spec.key {
		next, ok := node[k].(map[string]any)
		if !ok {
			return false
		}
		node = next
	}
	list, _ := node[event].([]any)
	for _, item := range list {
		if m, ok := item.(map[string]any); ok && ourHookEntry(m) {
			return true
		}
	}
	return false
}

// eventKeyWritten says whether the agent's hooks file names event at all.
func eventKeyWritten(spec *hookSpec, home, event string) bool {
	raw, err := os.ReadFile(filepath.Join(home, spec.file))
	if err != nil {
		return false
	}
	root := map[string]any{}
	if json.Unmarshal(raw, &root) != nil {
		return false
	}
	node := root
	for _, k := range spec.key {
		next, ok := node[k].(map[string]any)
		if !ok {
			return false
		}
		node = next
	}
	_, ok := node[event]
	return ok
}

// healHooks takes out reminal's own entries for events the installed agent
// is too old for (its version read by version; one not read is left as it
// is), each agent's file otherwise byte for byte as it was. It says what it
// took out, one line each, for the person.
func healHooks(home string, version func(bin string) string) []string {
	var said []string
	withHooksHealLock(func() { said = healHooksLocked(home, version) })
	return said
}

func healHooksLocked(home string, version func(bin string) string) []string {
	var said []string
	for _, t := range agentTargets() {
		if t.hooks == nil {
			continue
		}
		var old []hookEvent
		for _, ev := range t.hooks.events {
			if ev.MinVersion != "" && ownHookWritten(t.hooks, home, ev.Event) {
				old = append(old, ev)
			}
		}
		if len(old) == 0 {
			continue
		}
		have := version(t.Bin)
		if have == "" {
			continue // not known to be too old: left as it is
		}
		for _, ev := range old {
			if hookEventFits(ev, have) {
				continue
			}
			path := filepath.Join(home, t.hooks.file)
			removed, err := removeOwnHookFile(path, t.hooks.key, ev.Event)
			if err == nil && removed {
				// Taken out for its agent's version, not by the person:
				// held, and written again only when a session's agent is
				// new enough (hooks_heal_added.go).
				holdHookWritten(home, t.Bin, ev.Event, versionRe.FindString(have))
			}
			if err != nil {
				said = append(said, fmt.Sprintf("reminal could not remove its %s hook from %s: %v", ev.Event, tildePath(home, path), err))
			} else if removed {
				line := fmt.Sprintf("reminal removed its %s hook: %s %s is older than %s. Start %s again to use reminal's hooks", ev.Event, t.Bin, versionRe.FindString(have), ev.MinVersion, t.Bin)
				if ev.OldRunsNone && eventKeyWritten(t.hooks, home, ev.Event) {
					// A hook of the person's own for it is left, as it is
					// theirs — and while it is there this agent runs none.
					line += fmt.Sprintf(". A %s hook of your own is still in %s: %s %s runs none of its hooks while it is there", ev.Event, tildePath(home, path), t.Bin, versionRe.FindString(have))
				}
				said = append(said, line)
			}
		}
	}
	return said
}

// installedVersion is an agent's version as it says, when it is on PATH —
// for --check, which has no session's own agent to ask: kept by the
// binary's path, size and time (~/.reminal/agent-versions.json), so a check
// asks a CLI only once it changed.
func installedVersion(bin string) string {
	path, err := exec.LookPath(bin)
	if err != nil {
		return ""
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	st, err := os.Stat(path)
	if err != nil {
		return ""
	}
	home, _ := os.UserHomeDir()
	cache := filepath.Join(home, ".reminal", "agent-versions.json")
	key := fmt.Sprintf("%s|%d|%d", path, st.Size(), st.ModTime().UnixNano())
	kept := map[string]string{}
	if b, err := os.ReadFile(cache); err == nil && len(b) < 64<<10 {
		_ = json.Unmarshal(b, &kept)
	}
	if v, ok := kept[key]; ok {
		return v
	}
	v := agentVersion(path)
	if v == "" {
		return ""
	}
	// Only the binaries there are now: one replaced leaves its old key.
	fresh := map[string]string{key: v}
	for k, old := range kept {
		if p, _, _ := strings.Cut(k, "|"); p != path && len(fresh) < 32 {
			fresh[k] = old
		}
	}
	if b, err := json.Marshal(fresh); err == nil && home != "" {
		_ = os.MkdirAll(filepath.Dir(cache), 0o700)
		_ = atomicfile.Write(cache, b, 0o600)
	}
	return v
}

// sessionVersions is the --heal-hooks version provider: what the session
// said its agent's own binary answered (--agent-version claude=<v>), and
// nothing for an agent it did not say — never asked of reminal's own PATH,
// whose claude may not be the one the session runs.
func sessionVersions(said map[string]string) func(string) string {
	return func(bin string) string { return said[bin] }
}

// removeOwnHookFile takes reminal's own entries for event out of the JSON
// file at path, under keyPath, and writes it back with its mode kept: every
// other byte as it was.
func removeOwnHookFile(path string, keyPath []string, event string) (bool, error) {
	// A link (settings kept in a dotfiles repo) stays one: what it names is
	// written, in place of replacing the link with a file.
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false, err
	}
	st, err := os.Stat(target)
	if err != nil {
		return false, err
	}
	if !st.Mode().IsRegular() {
		return false, errors.New("its settings are not a regular file, so nothing was changed")
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		return false, err
	}
	out, removed, err := removeOwnHookEntries(raw, keyPath, event)
	if err != nil || !removed {
		return false, err
	}
	// Read again just before it is written: changed meanwhile (the agent,
	// or a person, writing it), nothing is written over theirs.
	healBeforeWrite(target)
	if now, err := os.ReadFile(target); err != nil || !bytes.Equal(now, raw) {
		return false, errHealRaced
	}
	return true, atomicfile.Write(target, out, st.Mode().Perm())
}

// healBeforeWrite runs between the cut and its re-read (a test writes the
// file there, as another writer would).
var healBeforeWrite = func(string) {}

// errHealRaced: the file changed while the heal read it.
var errHealRaced = errors.New("its settings changed while reminal read them, so nothing was changed")

// removeOwnHookEntries is raw with reminal's own entries (ourHookEntry) of
// the event list at keyPath+event cut out of the text: every other byte as it
// was — the person's formatting, their keys' order, their own entries for the
// event. A list left empty goes with its key. Every cut is found on raw's own
// offsets and the kept ranges copied in one pass; the result is then checked
// to parse as raw does with exactly reminal's entries gone, or nothing is cut
// (errHealUnsure).
func removeOwnHookEntries(raw []byte, keyPath []string, event string) ([]byte, bool, error) {
	p := &jsonSpan{b: raw}
	p.ws()
	obj, err := p.value()
	if err != nil {
		return raw, false, err
	}
	if p.ws(); p.i != len(raw) {
		return raw, false, errJSON // more after the value: not a file we read rightly
	}
	for _, k := range keyPath {
		m := obj.member(k)
		if m == nil {
			return raw, false, nil
		}
		obj = m.val
	}
	mem := obj.member(event)
	if mem == nil || mem.val.kind != '[' {
		return raw, false, nil
	}
	els := mem.val.elems
	ours := make([]bool, len(els))
	n := 0
	for i, el := range els {
		var m map[string]any
		ours[i] = json.Unmarshal(raw[el.start:el.end], &m) == nil && ourHookEntry(m)
		if ours[i] {
			n++
		}
	}
	if n == 0 {
		return raw, false, nil
	}
	var cuts []span
	if n == len(els) {
		// The whole member goes: to the next one's start, or, the last, from
		// the end of the one before.
		cuts = []span{itemCut(obj.membersAsItems(), mem.index)}
	} else {
		lastKept := -1
		for i := range els {
			if !ours[i] {
				lastKept = i
			}
		}
		for i := range els {
			if !ours[i] {
				continue
			}
			if i < lastKept {
				cuts = append(cuts, span{els[i].start, els[i+1].start}) // with the comma after it
			} else {
				cuts = append(cuts, span{els[i-1].end, els[i].end}) // with the comma before it
			}
		}
	}
	out := make([]byte, 0, len(raw))
	at := 0
	for _, c := range cuts { // in order, never overlapping: each from raw
		out = append(out, raw[at:c.start]...)
		at = c.end
	}
	out = append(out, raw[at:]...)
	if !healedAsExpected(raw, out, keyPath, event) {
		return raw, false, errHealUnsure
	}
	return out, true, nil
}

// errHealUnsure: the cut would not leave the file as it was but for
// reminal's own entries — nothing is written.
var errHealUnsure = errors.New("its settings would not read as they did with only reminal's entries gone, so nothing was changed")

// healedAsExpected says whether out parses to what raw does with exactly
// reminal's own entries for event gone (and the event, if none is left):
// the structure, not only the text, is what is checked.
func healedAsExpected(raw, out []byte, keyPath []string, event string) bool {
	if !json.Valid(out) {
		return false
	}
	var want, got any
	if json.Unmarshal(raw, &want) != nil || json.Unmarshal(out, &got) != nil {
		return false
	}
	node, ok := want.(map[string]any)
	for _, k := range keyPath {
		if !ok {
			return false
		}
		node, ok = node[k].(map[string]any)
	}
	if !ok {
		return false
	}
	list, _ := node[event].([]any)
	kept := []any{}
	for _, it := range list {
		if m, isMap := it.(map[string]any); isMap && ourHookEntry(m) {
			continue
		}
		kept = append(kept, it)
	}
	if len(kept) == 0 {
		delete(node, event)
	} else {
		node[event] = kept
	}
	return reflect.DeepEqual(want, got)
}

// itemCut is what of a list's text goes with its item i (array elements, or
// object members): up to the next item's start, or, the last of several,
// from the end of the one before; the only item, just its own span.
func itemCut(items []span, i int) span {
	switch {
	case i+1 < len(items):
		return span{items[i].start, items[i+1].start}
	case i > 0:
		return span{items[i-1].end, items[i].end}
	}
	return items[i]
}

// span is where a JSON value (or an object's member, key to value) lies in
// its text.
type span struct{ start, end int }

// jsonValue is a parsed value's place, and an object's members or an array's
// elements by their places.
type jsonValue struct {
	span
	kind    byte // '{', '[', or 0 for anything else
	members []jsonMember
	elems   []span
}

type jsonMember struct {
	key   string
	whole span // the key to the end of its value
	val   *jsonValue
	index int
}

func (v *jsonValue) member(k string) *jsonMember {
	if v == nil || v.kind != '{' {
		return nil
	}
	for i := len(v.members) - 1; i >= 0; i-- { // the last of a repeated key is the one read
		if v.members[i].key == k {
			return &v.members[i]
		}
	}
	return nil
}

func (v *jsonValue) membersAsItems() []span {
	out := make([]span, len(v.members))
	for i, m := range v.members {
		out[i] = m.whole
	}
	return out
}

// jsonSpan reads JSON for where its values lie; what it accepts is JSON.
type jsonSpan struct {
	b []byte
	i int
}

var errJSON = errors.New("not JSON")

func (p *jsonSpan) ws() {
	for p.i < len(p.b) && (p.b[p.i] == ' ' || p.b[p.i] == '\t' || p.b[p.i] == '\n' || p.b[p.i] == '\r') {
		p.i++
	}
}

func (p *jsonSpan) value() (*jsonValue, error) {
	if p.i >= len(p.b) {
		return nil, errJSON
	}
	v := &jsonValue{span: span{start: p.i}}
	switch c := p.b[p.i]; c {
	case '{':
		v.kind = '{'
		p.i++
		p.ws()
		if p.i < len(p.b) && p.b[p.i] == '}' {
			p.i++
			break
		}
		for {
			p.ws()
			ks := p.i
			key, err := p.str()
			if err != nil {
				return nil, err
			}
			p.ws()
			if p.i >= len(p.b) || p.b[p.i] != ':' {
				return nil, errJSON
			}
			p.i++
			p.ws()
			val, err := p.value()
			if err != nil {
				return nil, err
			}
			v.members = append(v.members, jsonMember{key: key, whole: span{ks, val.end}, val: val, index: len(v.members)})
			p.ws()
			if p.i >= len(p.b) {
				return nil, errJSON
			}
			if p.b[p.i] == ',' {
				p.i++
				continue
			}
			if p.b[p.i] != '}' {
				return nil, errJSON
			}
			p.i++
			break
		}
	case '[':
		v.kind = '['
		p.i++
		p.ws()
		if p.i < len(p.b) && p.b[p.i] == ']' {
			p.i++
			break
		}
		for {
			p.ws()
			el, err := p.value()
			if err != nil {
				return nil, err
			}
			v.elems = append(v.elems, el.span)
			p.ws()
			if p.i >= len(p.b) {
				return nil, errJSON
			}
			if p.b[p.i] == ',' {
				p.i++
				continue
			}
			if p.b[p.i] != ']' {
				return nil, errJSON
			}
			p.i++
			break
		}
	case '"':
		if _, err := p.str(); err != nil {
			return nil, err
		}
	default:
		// A number, true, false or null: to the next delimiter, and checked.
		s := p.i
		for p.i < len(p.b) && !isJSONDelim(p.b[p.i]) {
			p.i++
		}
		var x any
		if p.i == s || json.Unmarshal(p.b[s:p.i], &x) != nil {
			return nil, errJSON
		}
	}
	v.end = p.i
	return v, nil
}

func isJSONDelim(c byte) bool {
	switch c {
	case ',', '}', ']', ' ', '\t', '\n', '\r':
		return true
	}
	return false
}

// str reads a JSON string, and is what it says.
func (p *jsonSpan) str() (string, error) {
	if p.i >= len(p.b) || p.b[p.i] != '"' {
		return "", errJSON
	}
	s := p.i
	p.i++
	for p.i < len(p.b) {
		switch p.b[p.i] {
		case '\\':
			p.i += 2
			continue
		case '"':
			p.i++
			var out string
			if err := json.Unmarshal(p.b[s:p.i], &out); err != nil {
				return "", errJSON
			}
			return out, nil
		}
		p.i++
	}
	return "", errJSON
}
