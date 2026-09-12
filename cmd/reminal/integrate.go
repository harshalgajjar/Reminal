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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
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
}

func stdioEntry(exe string) map[string]any {
	return map[string]any{"command": exe, "args": []string{"mcp"}}
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
}

// hookSpec is how to install reminal's attention hooks into one agent's config.
type hookSpec struct {
	file   string         // config file, relative to $HOME
	key    []string       // path to the hooks object within it, e.g. ["hooks"]
	extra  map[string]any // top-level keys to ensure (e.g. Cursor's {"version":1})
	events []hookEvent    // event → state mappings (an agent may lack some states)
	shape  hookShape
}

// hookCommand is the shell command an agent runs on an event. The reminal path is
// quoted so an app-bundle path with spaces still works when run via a shell.
func hookCommand(exe, state string) string {
	q := exe
	if strings.ContainsAny(exe, " \t") {
		q = "'" + strings.ReplaceAll(exe, "'", `'\''`) + "'"
	}
	return q + " hook " + state
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
		if err := os.WriteFile(path+".bak", raw, 0o600); err != nil {
			return fmt.Errorf("writing backup: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	} else if remove {
		return nil // nothing installed
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
			if m, ok := item.(map[string]any); ok {
				if _, mine := m[hookMarker]; mine {
					continue
				}
			}
			kept = append(kept, item)
		}
		if !remove {
			kept = append(kept, hookEntry(spec.shape, exe, ev.State))
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
	return os.WriteFile(path, append(buf, '\n'), 0o600)
}

// hookEntry builds one config entry in the agent's expected shape, tagged with
// hookMarker so we can find and remove exactly our own on a re-run or --remove.
func hookEntry(shape hookShape, exe, state string) map[string]any {
	cmd := hookCommand(exe, state)
	switch shape {
	case shapeFlat:
		return map[string]any{"command": cmd, hookMarker: state}
	default: // shapeMatcher
		return map[string]any{
			"hooks":    []any{map[string]any{"type": "command", "command": cmd}},
			hookMarker: state,
		}
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
			hooks: &hookSpec{
				file: ".claude/settings.json", key: []string{"hooks"}, shape: shapeMatcher,
				events: []hookEvent{
					{"UserPromptSubmit", "working"}, // a turn begins
					{"Notification", "notify"},      // permission OR idle — split by payload
					{"Stop", "done"},                // turn finished
				},
			},
		},
		{
			Name: "Codex CLI", Bin: "codex",
			cliAdd:    []string{"mcp", "add", mcpServerName, "--", "%CMD%", "mcp"},
			cliRemove: []string{"mcp", "remove", mcpServerName},
		},
		{
			Name: "Antigravity CLI", Bin: "agy",
			cliAdd:    []string{"mcp", "add", mcpServerName, "--", "%CMD%", "mcp"},
			cliRemove: []string{"mcp", "remove", mcpServerName},
		},
		{
			// opencode's `mcp add` is interactive, so drive its config instead.
			Name: "OpenCode", Bin: "opencode",
			file: ".config/opencode/opencode.json", keyPath: []string{"mcp"},
			entry: func(exe string) map[string]any {
				return map[string]any{"type": "local", "command": []string{exe, "mcp"}, "enabled": true}
			},
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
					{"BeforeAgent", "working"},
					{"Notification", "input"}, // observability-only; fires on tool-permission
					{"AfterAgent", "done"},
				},
			},
		},
		{
			Name: "Qwen Code", Bin: "qwen",
			file: ".qwen/settings.json", keyPath: []string{"mcpServers"}, entry: stdioEntry,
			hooks: &hookSpec{
				file: ".qwen/settings.json", key: []string{"hooks"}, shape: shapeMatcher,
				events: []hookEvent{
					{"UserPromptSubmit", "working"},
					{"Notification", "notify"}, // idle_prompt + permission_prompt — split by payload
					{"Stop", "done"},
				},
			},
		},
		{
			Name: "Amp", Bin: "amp",
			file: ".config/amp/settings.json", keyPath: []string{"amp.mcpServers"}, entry: stdioEntry,
		},
	}
}

type planStep struct {
	target  agentTarget
	binPath string
	how     string // human description of what will change
}

func runIntegrate(args []string) error {
	remove, assumeYes, dryRun := false, false, false
	var only []string
	for _, a := range args {
		switch a {
		case "--remove", "--uninstall":
			remove = true
		case "-y", "--yes":
			assumeYes = true
		case "--dry-run", "-n":
			dryRun = true
		case "-h", "--help":
			printIntegrateHelp()
			return nil
		default:
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
		if t.cliAdd != nil {
			verb := "via " + t.Bin + " mcp add"
			if remove {
				verb = "via " + t.Bin + " mcp remove"
			}
			how = verb
		} else {
			how = filepath.Join("~", t.file)
		}
		if t.hooks != nil {
			how += " + attention hooks"
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
		if p.target.cliAdd != nil {
			mcpErr = applyViaCLI(p, exe, remove)
		} else {
			mcpErr = applyViaFile(p.target, home, exe, remove)
		}
		// The MCP server and the attention hooks are separate installs; do both
		// INDEPENDENTLY. A failure of one must not skip the other: a re-run hits
		// "MCP already exists" (see applyViaCLI), and gating hooks on MCP success
		// used to mean the hooks silently never installed on any machine whose
		// MCP server was registered by an earlier version.
		var hookErr error
		if p.target.hooks != nil {
			hookErr = applyHooks(p.target.hooks, home, exe, remove)
		}
		if err := errors.Join(mcpErr, hookErr); err != nil {
			failures++
			fmt.Printf("  ✗ %-18s %v\n", p.target.Name, err)
			continue
		}
		fmt.Printf("  ✓ %-18s %s\n", p.target.Name, cDim(p.how))
	}

	if failures > 0 {
		return fmt.Errorf("%d of %d failed", failures, len(plan))
	}
	if !remove {
		fmt.Printf("\nDone. Restart any running agent to pick it up. It can now leave\n" +
			"notes on your windows (MCP), and its live state — working / needs you /\n" +
			"done — shows in `reminal list` and the Machines view (attention hooks).\n")
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
	cmd := exec.Command(p.binPath, args...)
	out, err := cmd.CombinedOutput()
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
		if err := os.WriteFile(path+".bak", raw, 0o600); err != nil {
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
	return os.WriteFile(path, append(buf, '\n'), 0o600)
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

Two things get installed, in each agent's own native format (an ` + "`mcp add`" + `
subcommand where one exists, otherwise a merge into its JSON config, backed up
first):

  • the reminal MCP server — so the agent can leave notes on your windows;
  • attention hooks — so the agent reports its live state (working / needs you /
    done) to ` + "`reminal list`" + ` and the Machines view. Agents without hook support
    fall back to reminal's screen-based detection automatically.
`)
}
