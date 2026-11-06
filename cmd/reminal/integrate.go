// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

// `reminal integrate` — register reminal's MCP server with every coding agent
// installed on this machine, in each one's own native format.
//
// Why this exists: an MCP server nobody registers is an MCP server that does not
// exist. The registration itself is a one-time chore that differs per agent —
// some have an `mcp add` subcommand, some want JSON edited in a config file —
// and asking a user to look up the right incantation for each of their agents is
// how a feature goes undiscovered. This collapses it to one command.
//
// It is deliberately cautious about other people's config: it prints the plan
// and asks before touching anything, backs up each JSON file it edits, and
// preserves unknown keys rather than rewriting the file wholesale.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"reminal/internal/atomicfile"
	"reminal/internal/client"
	"reminal/internal/piext"
	"reminal/internal/protocol"
	"reminal/internal/session"
)

// mcpServerName is the key reminal registers itself under, in every agent.
const mcpServerName = "reminal"

// agentTarget is one coding agent we know how to register with. Exactly one of
// cliAdd or file is set: prefer the agent's own CLI where it has one, since that
// keeps us out of the business of guessing its config schema.
type agentTarget struct {
	Name string // human label, e.g. "Claude Code"
	Bin  string // binary to look for on PATH

	// CLI route: args appended to Bin. %CMD% expands to the reminal path.
	cliAdd    []string
	cliRemove []string

	// File route: JSON config we merge into.
	file    string   // path relative to $HOME
	keyPath []string // where the server map lives, e.g. ["mcpServers"]
	entry   func(exe string) map[string]any

	// hooks, when set, installs reminal's attention lifecycle hooks into this
	// agent's config (separate from MCP registration). nil for agents whose hook
	// support we haven't wired yet — those still get MCP + the screen fallback.
	hooks *hookSpec

	// plugin, for an agent whose hooks are a plugin file it loads (OpenCode),
	// rather than entries in its config.
	plugin *pluginFile

	// Extension route: an agent with its own extension system takes a package
	// rather than a config entry, and one package does both jobs — it hands the
	// agent reminal's tools AND reports the session's attention state — so these
	// targets carry no cliAdd, file, or hooks of their own.
	install    func(home, exe string, remove bool) error
	installHow string // what the plan line says, relative to $HOME

	// Read-back (see integrate_check.go). A CLI-route agent keeps its
	// registration somewhere its `mcp add` decides; checkFile/checkKey (JSON)
	// or checkTOML name that place. File-route agents are read from file/keyPath.
	checkFile string
	checkKey  []string
	checkTOML string
	// aroundAdd, when set, runs the `mcp add` (add) and finishes what it
	// cannot fully make: a setting it has no flag for, which it also drops
	// when it writes the entry over. Run on every add, so an entry an older
	// reminal registered is brought up to date too.
	aroundAdd func(home string, add func() error) error
}

func stdioEntry(exe string) map[string]any {
	return map[string]any{"command": exe, "args": []string{"mcp"}}
}

// opencodeEntry is the server entry OpenCode, and Kilo Code CLI after it,
// read: a local command and its arguments as one list.
func opencodeEntry(exe string) map[string]any {
	return map[string]any{"type": "local", "command": []string{exe, "mcp"}, "enabled": true}
}

// ---- Attention hooks -------------------------------------------------------
//
// Alongside the MCP server, `reminal integrate` installs lifecycle hooks so an
// agent reports its attention state (working / input / done) precisely, via
// `reminal hook <state>`, instead of us inferring it from the screen. Each agent
// exposes different events and a different config shape; hookSpec captures both.

const hookMarker = "reminalHook" // tags OUR hook entries so we can find/remove them

type hookShape int

const (
	// shapeMatcher: Claude Code / Gemini / Qwen — an event maps to a list of
	// {matcher?, hooks:[{type:"command", command}]} groups.
	shapeMatcher hookShape = iota
	// shapeFlat: Cursor / Crush — an event maps to a list of {command,...} entries.
	shapeFlat
)

type hookEvent struct {
	Event string // the agent's own event name, e.g. "Notification"
	State string // our state it maps to: "working" | "input" | "done"
	// Matcher narrows a tool event to the tools named (shapeMatcher only);
	// empty, the entry carries no matcher at all.
	Matcher string
	// MinVersion is the agent's own version the event first exists in. An
	// older agent may reject its whole hooks block for an event it does not
	// know — Claude Code 2.1.77 ran none of its hooks with "StopFailure" in
	// them (tried in Docker) — so below it, or with no version read, the
	// event is not written, and one written before is taken out.
	MinVersion string
}

// hookSpec is how to install reminal's attention hooks into one agent's config.
type hookSpec struct {
	file   string         // config file, relative to $HOME
	key    []string       // path to the hooks object within it, e.g. ["hooks"]
	extra  map[string]any // top-level keys to ensure (e.g. Cursor's {"version":1})
	events []hookEvent    // event → state mappings (an agent may lack some states)
	shape  hookShape
	// cleanTop: also take our entries out of event lists at the top of the
	// file, where an earlier reminal put them (Antigravity's hooks live under
	// a name; left at the top, they make agy reject the whole file).
	cleanTop bool
	// version is the agent's own, as it says (agentVersion), for the events
	// that need one (hookEvent.MinVersion); "" when not read.
	version string
}

// hookEventFits says whether ev can be written for an agent of version
// have: it needs none, or have is at least what it needs.
func hookEventFits(ev hookEvent, have string) bool {
	return ev.MinVersion == "" || versionAtLeast(have, ev.MinVersion)
}

var versionRe = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

// versionAtLeast compares the first x.y.z in have with min's; false when
// have holds none.
func versionAtLeast(have, min string) bool {
	h, m := versionRe.FindStringSubmatch(have), versionRe.FindStringSubmatch(min)
	if h == nil || m == nil {
		return false
	}
	for i := 1; i <= 3; i++ {
		a, _ := strconv.Atoi(h[i])
		b, _ := strconv.Atoi(m[i])
		if a != b {
			return a > b
		}
	}
	return true
}

// agentVersionWait caps asking an agent its version: an abort cap, for a
// CLI that hangs; it answers at once.
const agentVersionWait = 15 * time.Second

// agentVersion is what `<bin> --version` says; "" when it cannot be run.
func agentVersion(bin string) string {
	ctx, cancel := context.WithTimeout(context.Background(), agentVersionWait)
	defer cancel()
	// Apart from any session it is run in (session.ProbeCommand).
	out, err := session.RunProbe(session.ProbeCommand(ctx, bin, "--version"), false)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// hookCommand is the shell command an agent runs on an event. The reminal path
// is quoted so a path with spaces (an app bundle, a username with a space,
// Program Files) still works when the harness runs it through a shell. Quoting
// is OS-specific: cmd.exe treats only double quotes as quoting — a single-quoted
// path is passed through literally and the command fails — so on Windows we
// double-quote, and on POSIX shells we single-quote (with escaping).
func hookCommand(exe, state string) string {
	return quoteExe(exe) + " hook " + state
}

func quoteExe(exe string) string { return quoteExeFor(exe, runtime.GOOS) }

func quoteExeFor(exe, goos string) string {
	if goos == "windows" {
		// Claude Code runs Windows hooks through bash (the hook error shows
		// /usr/bin/bash), where an unquoted native path's backslashes are eaten as
		// escape sequences — C:\Users\harsh -> C:Usersharsh, "command not found".
		// Forward slashes are accepted by the Windows loader and mean nothing
		// special to bash, so the path survives with no escaping. Spaces still need
		// a quote, and the shell is bash, so single-quote (not cmd's double).
		exe = strings.ReplaceAll(exe, `\`, `/`)
	}
	if strings.ContainsAny(exe, " \t") {
		return "'" + strings.ReplaceAll(exe, "'", `'\''`) + "'"
	}
	return exe
}

// applyHooks merges reminal's attention hooks into an agent's JSON config —
// preserving every other hook and key, backing up first, and (on remove, or
// re-run) dropping only the entries tagged with hookMarker so it's idempotent.
func applyHooks(spec *hookSpec, home, exe string, remove bool) error {
	path := filepath.Join(home, spec.file)
	root := map[string]any{}
	if raw, err := os.ReadFile(path); err == nil {
		if len(strings.TrimSpace(string(raw))) > 0 {
			if err := json.Unmarshal(raw, &root); err != nil {
				return fmt.Errorf("%s is not valid JSON; leaving it alone", path)
			}
		}
		if err := backupOnce(path, raw); err != nil {
			return fmt.Errorf("writing backup: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	} else if remove {
		return nil // nothing installed
	}

	if spec.cleanTop {
		for ev, v := range root {
			list, ok := v.([]any)
			if !ok {
				continue
			}
			kept := list[:0:0]
			for _, item := range list {
				if m, ok := item.(map[string]any); ok && ourHookEntry(m) {
					continue
				}
				kept = append(kept, item)
			}
			if len(kept) == 0 {
				delete(root, ev)
			} else {
				root[ev] = kept
			}
		}
	}

	// Walk to the hooks object, creating levels only when installing.
	node := root
	for _, k := range spec.key {
		next, ok := node[k].(map[string]any)
		if !ok {
			if remove {
				return nil
			}
			next = map[string]any{}
			node[k] = next
		}
		node = next
	}

	for _, ev := range spec.events {
		list, _ := node[ev.Event].([]any)
		// Drop any prior reminal entry for this event (idempotent + removal).
		kept := list[:0:0]
		for _, item := range list {
			if m, ok := item.(map[string]any); ok && ourHookEntry(m) {
				continue
			}
			kept = append(kept, item)
		}
		if !remove && hookEventFits(ev, spec.version) {
			kept = append(kept, hookEntry(spec.shape, exe, ev))
		}
		if len(kept) == 0 {
			delete(node, ev.Event)
		} else {
			node[ev.Event] = kept
		}
	}

	if !remove {
		for k, v := range spec.extra {
			if _, ok := root[k]; !ok {
				root[k] = v
			}
		}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	buf, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	return writeAgentConfig(path, append(buf, '\n'))
}

// writeAgentConfig writes an agent's config file, private to its owner. A
// link to it (settings kept in a dotfiles repo) stays a link: what it names
// is written — written to the link's own path, the link was replaced by a
// file, and the repo's copy went stale.
func writeAgentConfig(path string, b []byte) error {
	mode := os.FileMode(0o600)
	if real, err := filepath.EvalSymlinks(path); err == nil && real != path {
		// A link's target keeps its own mode: it is the person's file, in
		// their repo (review).
		path = real
		if st, err := os.Stat(real); err == nil {
			mode = st.Mode().Perm()
		}
	}
	return atomicfile.Write(path, b, mode)
}

// backupOnce keeps the file as it was BEFORE reminal first touched it. A
// backup rewritten on every run would, by the second run, hold a file
// reminal had already changed — no backup at all.
func backupOnce(path string, raw []byte) error {
	if _, err := os.Stat(path + ".bak"); err == nil {
		return nil
	}
	return atomicfile.Write(path+".bak", raw, 0o600)
}

// reminalHookCmdRe is the whole command of one of our hook entries and nothing
// else: reminal's path (bare, or quoted when it has spaces; .exe on Windows),
// then `hook <state>`. A person's hook that merely mentions reminal, or runs
// our command and then something more, is not ours.
var reminalHookCmdRe = regexp.MustCompile("^\\s*(?:'(?:[^']*/)?reminal(?:\\.exe)?'|\"(?:[^\"$`]*[/\\\\])?reminal(?:\\.exe)?\"|(?:[^\\s'\"$`;&|<>()]*[/\\\\])?reminal(?:\\.exe)?) hook (?:working|input|done|failed|notify|subagent|ready)\\s*$")

// ourHookEntry says whether a config entry is one of ours: tagged with
// hookMarker, or, since an agent may rewrite its settings and drop keys it
// does not know (Claude Code does, on sign-in), running only reminal's hook
// commands. Untagged ones were added again on every integrate, and every hook
// then fired twice.
func ourHookEntry(m map[string]any) bool {
	if _, mine := m[hookMarker]; mine {
		return true
	}
	// An untagged entry is ours only if it is exactly what we write: a flat
	// {command}, or a group of {type, command} hooks with at most an empty
	// matcher. Anything a person added to it (a timeout, a matcher) makes it
	// theirs.
	if c, ok := m["command"].(string); ok {
		return len(m) == 1 && reminalHookCmdRe.MatchString(c)
	}
	hs, ok := m["hooks"].([]any)
	if !ok || len(hs) == 0 {
		return false
	}
	for k, v := range m {
		if k == "hooks" {
			continue
		}
		if k == "matcher" {
			if s, _ := v.(string); s == "" {
				continue
			}
		}
		return false
	}
	for _, h := range hs {
		hm, ok := h.(map[string]any)
		if !ok || len(hm) > 2 {
			return false
		}
		if t, has := hm["type"]; has && t != "command" {
			return false
		}
		c, _ := hm["command"].(string)
		if !reminalHookCmdRe.MatchString(c) {
			return false
		}
	}
	return true
}

// hookEntry builds one config entry in the agent's expected shape, tagged with
// hookMarker so we can find and remove exactly our own on a re-run or --remove.
func hookEntry(shape hookShape, exe string, ev hookEvent) map[string]any {
	cmd := hookCommand(exe, ev.State)
	switch shape {
	case shapeFlat:
		return map[string]any{"command": cmd, hookMarker: ev.State}
	default: // shapeMatcher
		e := map[string]any{
			"hooks":    []any{map[string]any{"type": "command", "command": cmd}},
			hookMarker: ev.State,
		}
		if ev.Matcher != "" {
			e["matcher"] = ev.Matcher
		}
		return e
	}
}

// agentTargets is the registration matrix. Kept explicit rather than clever:
// every one of these was verified against the tool's actual CLI or docs, and a
// wrong guess here silently corrupts somebody's config.
func agentTargets() []agentTarget {
	return []agentTarget{
		{
			// --scope user is load-bearing: `claude mcp add` defaults to "local",
			// which registers only for the directory it was run in. Without it
			// this command would appear to work and then do nothing everywhere
			// else. Same for the removal, or it looks for a local entry that was
			// never written.
			Name: "Claude Code", Bin: "claude",
			cliAdd:    []string{"mcp", "add", "--scope", "user", mcpServerName, "--", "%CMD%", "mcp"},
			cliRemove: []string{"mcp", "remove", "--scope", "user", mcpServerName},
			checkFile: ".claude.json", checkKey: []string{"mcpServers"},
			hooks: &hookSpec{
				file: ".claude/settings.json", key: []string{"hooks"}, shape: shapeMatcher,
				events: []hookEvent{
					{Event: "UserPromptSubmit", State: "working"}, // a turn begins
					{Event: "PermissionRequest", State: "input"},  // blocked on your approval — the precise event
					{Event: "Notification", State: "notify"},      // idle (or, on older builds, permission) — split by payload
					{Event: "Stop", State: "done"},                // turn finished, with what it left running
					// Its turn ended on an API error instead — a usage limit's
					// refusal, a login gone: fires in Stop's place.
					{Event: "StopFailure", State: "failed", MinVersion: "2.1.78"},
					// One of its own subagents finished: what is still running.
					{Event: "SubagentStop", State: "subagent"},
					// Up and reading its input: started, or resumed (a long
					// conversation loads for a while first).
					{Event: "SessionStart", State: "ready"},
				},
			},
		},
		{
			Name: "Codex CLI", Bin: "codex",
			cliAdd:    []string{"mcp", "add", mcpServerName, "--", "%CMD%", "mcp"},
			cliRemove: []string{"mcp", "remove", mcpServerName},
			checkTOML: ".codex/config.toml",
			aroundAdd: codexAddWithToolTimeout,
			// No hooks: Codex runs them from its app-server, shared by every
			// Codex on the machine and started inside the first one's
			// session, so each would report as that session.
		},
		{
			Name: "Antigravity CLI", Bin: "agy",
			cliAdd:    []string{"mcp", "add", mcpServerName, "--", "%CMD%", "mcp"},
			cliRemove: []string{"mcp", "remove", mcpServerName},
			checkFile: ".gemini/config/mcp_config.json", checkKey: []string{"mcpServers"},
			// Its hooks (~/.gemini/config/hooks.json): each set under a name of
			// its own, as flat {command} entries — agy rejects the whole file if
			// events sit at its top. It has no event for waiting on the person.
			hooks: &hookSpec{
				file: ".gemini/config/hooks.json", key: []string{mcpServerName}, shape: shapeFlat, cleanTop: true,
				events: []hookEvent{
					{Event: "PreInvocation", State: "working"}, // a turn begins
					{Event: "Stop", State: "done"},             // turn finished
				},
			},
		},
		{
			// opencode's `mcp add` is interactive, so drive its config instead.
			Name: "OpenCode", Bin: "opencode",
			file: ".config/opencode/opencode.json", keyPath: []string{"mcp"}, entry: opencodeEntry,
			plugin: &pluginFile{path: ".config/opencode/plugin/reminal.js", body: opencodePlugin},
		},
		{
			// Kilo Code CLI is OpenCode's fork: the same server entry and the
			// same plugin, in its own config dir (it reads nothing of
			// OpenCode's). Its own config is kilo.jsonc, comments and all,
			// which a JSON merge would lose; it also reads a kilo.json beside
			// it and merges the two, so ours goes there. Its `mcp add` has no
			// remove to go with it.
			Name: "Kilo Code CLI", Bin: "kilo",
			file: ".config/kilo/kilo.json", keyPath: []string{"mcp"}, entry: opencodeEntry,
			plugin: &pluginFile{path: ".config/kilo/plugin/reminal.js", body: opencodePlugin},
		},
		{
			Name: "Cursor CLI", Bin: "cursor-agent",
			file: ".cursor/mcp.json", keyPath: []string{"mcpServers"}, entry: stdioEntry,
		},
		{
			Name: "Gemini CLI", Bin: "gemini",
			file: ".gemini/settings.json", keyPath: []string{"mcpServers"}, entry: stdioEntry,
			hooks: &hookSpec{
				file: ".gemini/settings.json", key: []string{"hooks"}, shape: shapeMatcher,
				events: []hookEvent{
					{Event: "BeforeAgent", State: "working"},
					{Event: "Notification", State: "input"}, // observability-only; fires on tool-permission
					{Event: "AfterAgent", State: "done"},
				},
			},
		},
		{
			Name: "Qwen Code", Bin: "qwen",
			file: ".qwen/settings.json", keyPath: []string{"mcpServers"}, entry: stdioEntry,
			hooks: &hookSpec{
				file: ".qwen/settings.json", key: []string{"hooks"}, shape: shapeMatcher,
				events: []hookEvent{
					{Event: "UserPromptSubmit", State: "working"},
					{Event: "Notification", State: "notify"}, // idle_prompt + permission_prompt — split by payload
					{Event: "Stop", State: "done"},
					{Event: "SessionStart", State: "ready"},
				},
			},
		},
		{
			Name: "Amp", Bin: "amp",
			file: ".config/amp/settings.json", keyPath: []string{"amp.mcpServers"}, entry: stdioEntry,
		},
		{
			// pi has no MCP client to register with — it takes an extension, which
			// covers both jobs at once: it discovers reminal's tools at runtime and
			// reports pi's lifecycle as attention state. See internal/piext.
			Name: "pi", Bin: "pi",
			install: func(home, exe string, remove bool) error {
				if remove {
					return piext.Remove(home)
				}
				return piext.Install(home, exe)
			},
			installHow: "extension in ~/.pi/agent/extensions + attention",
		},
	}
}

type planStep struct {
	target  agentTarget
	binPath string
	how     string // human description of what will change
}

func runIntegrate(args []string) error {
	remove, assumeYes, dryRun, check, asJSON, heal := false, false, false, false, false, false
	agentVersions := map[string]string{}
	var only []string
	for _, a := range args {
		switch a {
		case "--remove", "--uninstall":
			remove = true
		case "--check", "--status":
			check = true
		case "--json":
			asJSON = true
		case "--heal-hooks":
			// Run by a session as its claude leaves for its shell
			// (hooks_heal.go): an entry the agent is too old for taken
			// out, and said.
			heal = true
		case "-y", "--yes":
			assumeYes = true
		case "--dry-run", "-n":
			dryRun = true
		case "-h", "--help":
			printIntegrateHelp()
			return nil
		default:
			if v, ok := strings.CutPrefix(a, "--agent-version="); ok {
				// What the session's own agent binary said it is
				// (--heal-hooks): bin=version.
				if bin, ver, ok := strings.Cut(v, "="); ok && bin != "" {
					agentVersions[bin] = ver
				}
				continue
			}
			if strings.HasPrefix(a, "-") {
				return fmt.Errorf("unknown flag %q (try --help)", a)
			}
			only = append(only, strings.ToLower(a))
		}
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locating reminal: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	home, _ := os.UserHomeDir()
	if home == "" {
		return fmt.Errorf("no home directory: the agents' configs live there")
	}
	widenPATH(home)

	if check {
		return printIntegrationReport(integrationReport(home, exe, only), asJSON)
	}
	if heal {
		for _, line := range healHooks(home, sessionVersions(agentVersions)) {
			fmt.Println(line)
		}
		return nil
	}

	var plan []planStep
	var skipped []string
	for _, t := range agentTargets() {
		if len(only) > 0 && !matchesAny(t, only) {
			continue
		}
		path, err := exec.LookPath(t.Bin)
		if err != nil {
			skipped = append(skipped, t.Name)
			continue
		}
		how := ""
		switch {
		case t.install != nil:
			how = t.installHow
		case t.cliAdd != nil:
			how = "via " + t.Bin + " mcp add"
			if remove {
				how = "via " + t.Bin + " mcp remove"
			}
		default:
			how = filepath.Join("~", t.file)
		}
		if t.hooks != nil {
			how += " + attention hooks"
		} else if t.plugin != nil {
			how += " + attention plugin"
		}
		plan = append(plan, planStep{target: t, binPath: path, how: how})
	}

	if len(plan) == 0 {
		fmt.Println("No supported agents found on PATH.")
		if len(skipped) > 0 {
			fmt.Printf("Looked for: %s\n", strings.Join(agentNames(), ", "))
		}
		return nil
	}

	action := "Register"
	if remove {
		action = "Remove"
	}
	fmt.Printf("%s reminal's MCP server (%s mcp) with:\n\n", action, exe)
	for _, p := range plan {
		fmt.Printf("  %-18s %s\n", p.target.Name, cDim(p.how))
	}
	if len(skipped) > 0 {
		sort.Strings(skipped)
		fmt.Printf("\n  not installed: %s\n", cDim(strings.Join(skipped, ", ")))
	}

	if dryRun {
		fmt.Println("\nDry run — nothing changed.")
		return nil
	}
	if !assumeYes {
		fmt.Print("\nProceed? [y/N] ")
		reader := bufio.NewReader(os.Stdin)
		line, _ := reader.ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			fmt.Println("Cancelled.")
			return nil
		}
	}
	fmt.Println()

	var failures int
	for _, p := range plan {
		var mcpErr error
		switch {
		case p.target.install != nil:
			mcpErr = p.target.install(home, exe, remove)
		case p.target.cliAdd != nil:
			add := func() error { return applyViaCLI(p, exe, remove) }
			if !remove && p.target.aroundAdd != nil {
				mcpErr = p.target.aroundAdd(home, add)
			} else {
				mcpErr = add()
			}
		default:
			mcpErr = applyViaFile(p.target, home, exe, remove)
		}
		// The MCP server and the attention hooks are separate installs; do both
		// INDEPENDENTLY. A failure of one must not skip the other: a re-run hits
		// "MCP already exists" (see applyViaCLI), and gating hooks on MCP success
		// used to mean the hooks silently never installed on any machine whose
		// MCP server was registered by an earlier version.
		var hookErr error
		if p.target.hooks != nil {
			spec := *p.target.hooks
			if !remove {
				spec.version = agentVersion(p.target.Bin)
			}
			hookErr = applyHooks(&spec, home, exe, remove)
		}
		if p.target.plugin != nil {
			hookErr = applyPlugin(p.target.plugin, home, exe, remove)
		}
		if err := errors.Join(mcpErr, hookErr); err != nil {
			failures++
			fmt.Printf("  ✗ %-18s %v\n", p.target.Name, err)
			continue
		}
		noteSetUp(home, p.target.Bin, !remove)
		fmt.Printf("  ✓ %-18s %s\n", p.target.Name, cDim(p.how))
	}

	if failures > 0 {
		return fmt.Errorf("%d of %d failed", failures, len(plan))
	}
	if !remove {
		fmt.Printf("\nDone. Restart any running agent to pick it up. It can now leave\n" +
			"notes on your windows (MCP), and its live state — working / needs you /\n" +
			"done — shows in `reminal list` and the Machines view (attention hooks).\n" +
			"\nAn agent loads its tools only when it starts: quit a running one from its\n" +
			"own prompt and start it again with its resume option, so the same\n" +
			"conversation continues:\n")
		for _, p := range plan {
			if p.target.resumeHint() != "" {
				fmt.Printf("  %-18s %s\n", p.target.Name, cDim(p.target.resumeHint()))
			}
		}
	} else {
		fmt.Printf("\nRemoved.\n")
	}
	return nil
}

func applyViaCLI(p planStep, exe string, remove bool) error {
	tmpl := p.target.cliAdd
	if remove {
		tmpl = p.target.cliRemove
	}
	args := make([]string, 0, len(tmpl))
	for _, a := range tmpl {
		args = append(args, strings.ReplaceAll(a, "%CMD%", exe))
	}
	// The agent's own CLI, asked apart from any session integrate is run in
	// (session.ProbeCommand): one that does not know the subcommand starts
	// up instead, and its hooks are no session's.
	out, err := session.RunProbe(session.ProbeCommand(context.Background(), p.binPath, args...), true)
	if err != nil {
		msg := strings.TrimSpace(string(out))
		low := strings.ToLower(msg)
		// integrate must be idempotent: re-running it (e.g. to pick up newly
		// added attention hooks) must not error on state that's already how we
		// want it. Removing something never registered, or adding something
		// already registered, is a success, not a failure.
		if remove && (strings.Contains(low, "not found") || strings.Contains(low, "no such")) {
			return nil
		}
		if !remove && strings.Contains(low, "already exists") {
			return nil
		}
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("%s", firstLine(msg))
	}
	return nil
}

// applyViaFile merges into an agent's JSON config, preserving every key it does
// not own and leaving a .bak of whatever was there before.
func applyViaFile(t agentTarget, home, exe string, remove bool) error {
	path := filepath.Join(home, t.file)
	root := map[string]any{}
	if raw, err := os.ReadFile(path); err == nil {
		if len(strings.TrimSpace(string(raw))) > 0 {
			if err := json.Unmarshal(raw, &root); err != nil {
				return fmt.Errorf("%s is not valid JSON; leaving it alone", path)
			}
		}
		if err := backupOnce(path, raw); err != nil {
			return fmt.Errorf("writing backup: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	} else if remove {
		return nil // nothing registered
	}

	// Walk to the map that holds server entries, creating levels as needed.
	node := root
	for _, key := range t.keyPath {
		next, ok := node[key].(map[string]any)
		if !ok {
			if remove {
				return nil
			}
			next = map[string]any{}
			node[key] = next
		}
		node = next
	}
	if remove {
		delete(node, mcpServerName)
	} else {
		node[mcpServerName] = t.entry(exe)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	buf, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	return writeAgentConfig(path, append(buf, '\n'))
}

func matchesAny(t agentTarget, names []string) bool {
	for _, n := range names {
		if n == strings.ToLower(t.Bin) || n == strings.ToLower(t.Name) {
			return true
		}
	}
	return false
}

func agentNames() []string {
	var out []string
	for _, t := range agentTargets() {
		out = append(out, t.Bin)
	}
	return out
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func printIntegrateHelp() {
	fmt.Print(`reminal integrate — wire reminal into your coding agents

  reminal integrate                 set up every agent found on PATH
  reminal integrate claude codex    only these
  reminal integrate --dry-run       show the plan, change nothing
  reminal integrate --remove        undo
  reminal integrate -y              skip the confirmation
  reminal integrate --check         what is set up, without changing anything
                                    (--json for machines)

Two things get installed, in each agent's own native format (an ` + "`mcp add`" + `
subcommand where one exists, a native extension for an agent that prefers one,
otherwise a merge into its JSON config, backed up first):

  • the reminal MCP server — so the agent can leave notes on your windows;
  • attention hooks — so the agent reports its live state (working / needs you /
    done) to ` + "`reminal list`" + ` and the Machines view. Agents without hook support
    fall back to reminal's screen-based detection automatically.
`)
}

// printIntegrationReport renders `--check`: one line per agent for a person,
// or the JSON the Machines view consumes.
func printIntegrationReport(report []protocol.IntegrationStatus, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}
	for _, st := range report {
		mark, what := "·", "not set up"
		switch {
		case !st.Known:
			what = "cannot tell (unknown config)"
		case st.Error != "":
			mark, what = "✗", st.Error
		case st.Integrated && st.Current:
			mark, what = "✓", "set up"
			if st.HooksWanted && !st.Hooks {
				what += ", attention hooks missing"
			}
		case st.Integrated:
			mark, what = "!", "set up for a different reminal (re-run integrate)"
		}
		if !st.Installed {
			what += cDim(" — not on PATH")
		}
		fmt.Printf("  %s %-18s %s\n", mark, st.Name, what)
	}
	return nil
}

// resumeHint is the command that starts this agent again on its latest
// conversation, as a restore would (client.ResumeLatest): the restart hint
// shown after a setup.
func (t agentTarget) resumeHint() string { return client.ResumeLatest(t.Bin) }
