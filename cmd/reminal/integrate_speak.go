// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

// Voice answers through reminal's speak tool (mcp_speak.go). An agent that
// asks the person before every call of a tool asked before every spoken
// answer too, on screen, while they were listening rather than looking:
// voice did not answer until they found the screen and allowed it. So
// `reminal integrate` allows speak, and only speak, in the agent's own
// permission settings, where the agent has them:
//
//   - Claude Code: "mcp__reminal__speak" in permissions.allow of
//     ~/.claude/settings.json.
//   - Gemini CLI: a rule of its policy engine in a file of reminal's own,
//     ~/.gemini/policies/reminal.toml, the same rule its "Allow always"
//     saves. Narrower than trusting the server (trust: true), which would
//     allow every reminal tool; and tools.allowed is deprecated, and says
//     so each time it starts.
//   - Qwen Code: the same allow entry as Claude Code's, in
//     ~/.qwen/settings.json. Qwen also defers MCP tools behind its
//     tool_search, so the model never saw speak, nor what it says about a
//     "[voice]" line, until it searched for it: reminal's entry asks for its
//     tools up front (alwaysLoadTools, mcpServerEntry below).
//
// Every write keeps what the person has: an allow list keeps all of its
// entries, and a re-run adds nothing twice. --remove takes out only the one
// entry reminal adds, and the policy file that is wholly reminal's.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// speakPermission is what the claude and qwen allow lists name speak by:
// mcp__<server>__<tool>.
const speakPermission = "mcp__" + mcpServerName + "__speak"

// speakAllow allows speak in one agent's permissions: apply writes (or, on
// remove, takes out) the allowance; allowed reads it back for --check.
type speakAllow struct {
	how     string // for the plan line
	apply   func(home string, remove bool) error
	allowed func(home string) bool
}

// allowListSpeak is the allowance kept as one entry of a JSON allow list
// (Claude Code's and Qwen Code's permissions.allow).
func allowListSpeak(file string) *speakAllow {
	key := []string{"permissions", "allow"}
	return &speakAllow{
		how: "speak allowed",
		apply: func(home string, remove bool) error {
			return editAllowList(filepath.Join(home, file), key, speakPermission, remove)
		},
		allowed: func(home string) bool {
			list, _ := jsonList(filepath.Join(home, file), key)
			return slices.Contains(list, speakPermission)
		},
	}
}

// editAllowList adds entry to the list at key in a JSON config (creating the
// file and the levels as needed), or on remove takes that one entry out.
// Every other entry and key stays as it was; a file that is not JSON is left
// alone, and the first one reminal changes is kept as a .bak.
func editAllowList(path string, key []string, entry string, remove bool) error {
	root := map[string]any{}
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		if len(strings.TrimSpace(string(raw))) > 0 {
			if err := json.Unmarshal(raw, &root); err != nil {
				return fmt.Errorf("%s is not valid JSON; leaving it alone", path)
			}
		}
	case !os.IsNotExist(err):
		return err
	case remove:
		return nil
	}
	node := root
	for _, k := range key[:len(key)-1] {
		next, ok := node[k].(map[string]any)
		if !ok {
			if _, there := node[k]; there || remove {
				// Something of the person's own, not an object: theirs
				// to keep; or nothing to remove.
				if remove {
					return nil
				}
				return fmt.Errorf("%s: %q is not an object; leaving it alone", path, k)
			}
			next = map[string]any{}
			node[k] = next
		}
		node = next
	}
	last := key[len(key)-1]
	var list []any
	if v, there := node[last]; there {
		l, ok := v.([]any)
		if !ok {
			if remove {
				return nil
			}
			return fmt.Errorf("%s: %q is not a list; leaving it alone", path, strings.Join(key, "."))
		}
		list = l
	}
	has := slices.ContainsFunc(list, func(v any) bool { s, _ := v.(string); return s == entry })
	switch {
	case !remove && has, remove && !has:
		return nil // already as wanted: the file is not rewritten
	case remove:
		list = slices.DeleteFunc(list, func(v any) bool { s, _ := v.(string); return s == entry })
	default:
		list = append(list, entry)
	}
	node[last] = list
	if err == nil {
		if err := backupOnce(path, raw); err != nil {
			return fmt.Errorf("writing backup: %w", err)
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

// jsonList reads the list of strings at key in a JSON file; nil when the
// file or the list is not there.
func jsonList(path string, key []string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var node any
	if err := json.Unmarshal(raw, &node); err != nil {
		return nil, err
	}
	for _, k := range key {
		m, _ := node.(map[string]any)
		node = m[k]
	}
	l, _ := node.([]any)
	var out []string
	for _, v := range l {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out, nil
}

// geminiSpeakPolicyFile is reminal's own file among Gemini CLI's user
// policies (~/.gemini/policies, read at its start: every .toml there).
const geminiSpeakPolicyFile = ".gemini/policies/reminal.toml"

// geminiSpeakPolicy is that file: one rule, as Gemini's own "Allow always"
// writes it for an MCP tool (its auto-saved.toml), but at priority 0, the
// lowest a user policy takes. Gemini ranks a rule by tier + priority/1000 and
// the first matching rule in that order wins, a deny no more than an allow
// (its PolicyEngine sorts by priority alone). At 4.000 this rule is below
// every rule a person sets for their user (their settings' excludeTools,
// tools.exclude and confirmationRequired are 4.35 to 4.9; their own policy
// rules 4.001 and up), and still above Gemini's defaults (tier 1), which is
// where the asking comes from. A person's rule at priority 0 too would tie,
// which is why a deny or ask of theirs is looked for first (geminiSpeakRefused)
// and this file is then not written at all.
const geminiSpeakPolicy = `# Written by reminal integrate: Gemini CLI runs reminal's speak tool (how
# it answers out loud to a person talking to it by voice in reminal) without
# asking each time. Only speak: reminal's other tools still ask. At priority
# 0, so a rule of your own about it, at any other priority, decides instead.
# reminal integrate --remove deletes this file.
[[rule]]
mcpName = "` + mcpServerName + `"
toolName = "speak"
decision = "allow"
priority = 0
`

// speakSkipped is not a failure: the person has said speak must not run
// without asking (or at all), and integrate leaves it so, and says where.
type speakSkipped struct{ why string }

func (s *speakSkipped) Error() string { return s.why }

func geminiSpeak() *speakAllow {
	return &speakAllow{
		how: "speak allowed",
		apply: func(home string, remove bool) error {
			path := filepath.Join(home, geminiSpeakPolicyFile)
			refused := ""
			if !remove {
				refused = geminiSpeakRefused(home)
			}
			if remove || refused != "" {
				// Also on a refusal: a rule an earlier run wrote would
				// tie with, or outrank, what the person set since.
				if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
					return err
				}
				if refused != "" {
					return &speakSkipped{why: "you have denied reminal's speak tool in " + refused + "; leaving it (Gemini CLI will keep asking, or refusing, before each spoken answer)"}
				}
				return nil
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			return writeAgentConfig(path, []byte(geminiSpeakPolicy))
		},
		allowed: func(home string) bool {
			if geminiSpeakRefused(home) != "" {
				return true // as the person set it: nothing for integrate to do
			}
			b, err := os.ReadFile(filepath.Join(home, geminiSpeakPolicyFile))
			return err == nil && string(b) == geminiSpeakPolicy
		},
	}
}

// geminiSpeakTool is speak as Gemini's policy engine names it.
const geminiSpeakTool = "mcp_" + mcpServerName + "_speak"

// geminiSpeakRefused names where the person told Gemini CLI that speak must
// ask or not run ("" when nowhere): a deny or ask_user rule matching it in
// their user policies (~/.gemini/policies, or the policyPaths their settings
// name instead), or, in ~/.gemini/settings.json, speak or reminal left out
// (excludeTools, includeTools without it, tools.exclude, mcp.excluded) or
// made to ask (tools.confirmationRequired).
//
// A project's own policies (<project>/.gemini/policies) are not read: they
// are many and integrate runs once, and Gemini ranks every user rule above
// every project one in any case (tier 4 over 3), its own "Allow always" too.
func geminiSpeakRefused(home string) string {
	settingsPath := filepath.Join(home, ".gemini", "settings.json")
	var settings struct {
		PolicyPaths []string `json:"policyPaths"`
		Tools       struct {
			Exclude              []string `json:"exclude"`
			ConfirmationRequired []string `json:"confirmationRequired"`
		} `json:"tools"`
		MCP struct {
			Excluded []string `json:"excluded"`
		} `json:"mcp"`
		MCPServers map[string]struct {
			ExcludeTools []string `json:"excludeTools"`
			IncludeTools []string `json:"includeTools"`
		} `json:"mcpServers"`
	}
	if raw, err := os.ReadFile(settingsPath); err == nil {
		_ = json.Unmarshal(raw, &settings)
	}
	shown := tildePath(home, settingsPath)
	if s, ok := settings.MCPServers[mcpServerName]; ok {
		if slices.Contains(s.ExcludeTools, "speak") {
			return shown + " (mcpServers.reminal.excludeTools)"
		}
		if s.IncludeTools != nil && !slices.Contains(s.IncludeTools, "speak") {
			return shown + " (mcpServers.reminal.includeTools)"
		}
	}
	if slices.Contains(settings.MCP.Excluded, mcpServerName) {
		return shown + " (mcp.excluded)"
	}
	for _, list := range []struct {
		names []string
		key   string
	}{{settings.Tools.Exclude, "tools.exclude"}, {settings.Tools.ConfirmationRequired, "tools.confirmationRequired"}} {
		for _, n := range list.names {
			if geminiNameMatches(n, "", geminiSpeakTool) {
				return shown + " (" + list.key + ")"
			}
		}
	}

	dirs := []string{filepath.Join(home, ".gemini", "policies")}
	if len(settings.PolicyPaths) > 0 {
		dirs = nil
		for _, p := range settings.PolicyPaths {
			if strings.HasPrefix(p, "~/") {
				p = filepath.Join(home, p[2:])
			}
			dirs = append(dirs, p)
		}
	}
	for _, d := range dirs {
		files := []string{d}
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			files, _ = filepath.Glob(filepath.Join(d, "*.toml"))
		}
		for _, f := range files {
			if filepath.Base(f) == filepath.Base(geminiSpeakPolicyFile) && filepath.Dir(f) == filepath.Join(home, ".gemini", "policies") {
				continue // reminal's own
			}
			raw, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			if geminiPolicyRefuses(string(raw)) {
				return tildePath(home, f)
			}
		}
	}
	return ""
}

// geminiPolicyRefuses reads Gemini policy TOML for a [[rule]] that denies
// speak or asks before it: its decision deny or ask_user, its mcpName absent,
// "*" or reminal, and one of its toolName patterns matching speak as the
// engine names it (a bare name with an mcpName is that server's tool).
// Read for the keys alone, line by line, a list running on until its "]".
func geminiPolicyRefuses(toml string) bool {
	type rule struct {
		decision, mcp string
		tools         []string
	}
	var rules []rule
	var cur *rule
	quoted := regexp.MustCompile(`"((?:[^"\\]|\\.)*)"|'([^']*)'`)
	lines := strings.Split(toml, "\n")
	for i := 0; i < len(lines); i++ {
		s := strings.TrimSpace(lines[i])
		if strings.HasPrefix(s, "[") {
			// A table header: a rule, or another table (not a rule).
			cur = nil
			if strings.HasPrefix(s, "[[rule]]") {
				rules = append(rules, rule{})
				cur = &rules[len(rules)-1]
			}
			continue
		}
		if cur == nil {
			continue
		}
		key, val, ok := strings.Cut(s, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if strings.HasPrefix(val, "[") {
			for !strings.Contains(val, "]") && i+1 < len(lines) {
				i++
				val += " " + strings.TrimSpace(lines[i])
			}
		}
		var strs []string
		for _, m := range quoted.FindAllStringSubmatch(val, -1) {
			strs = append(strs, m[1]+m[2])
		}
		switch key {
		case "decision":
			if len(strs) > 0 {
				cur.decision = strs[0]
			}
		case "mcpName":
			if len(strs) > 0 {
				cur.mcp = strs[0]
			}
		case "toolName":
			cur.tools = strs
		}
	}
	for _, r := range rules {
		if r.decision != "deny" && r.decision != "ask_user" {
			continue
		}
		if r.mcp != "" && r.mcp != "*" && r.mcp != mcpServerName {
			continue
		}
		for _, t := range r.tools {
			if geminiNameMatches(t, r.mcp, geminiSpeakTool) {
				return true
			}
		}
	}
	return false
}

// geminiNameMatches says whether a tool pattern of Gemini's matches name: a
// bare tool name under mcpName is mcp_<server>_<tool> (formatMcpToolName),
// and "*" anywhere in it stands for anything. A bare "speak" with no server
// is matched too: someone writing it means reminal's.
func geminiNameMatches(pattern, mcpName, name string) bool {
	if mcpName != "" && !strings.HasPrefix(pattern, "mcp_") {
		switch {
		case mcpName == "*" && (pattern == "" || pattern == "*"):
			pattern = "mcp_*"
		case mcpName == "*":
			pattern = "mcp_*_" + pattern
		default:
			pattern = "mcp_" + mcpName + "_" + pattern
		}
	}
	if pattern == "speak" {
		return true
	}
	re := "^" + strings.ReplaceAll(regexp.QuoteMeta(pattern), `\*`, ".*") + "$"
	ok, _ := regexp.MatchString(re, name)
	return ok
}

// qwenEntry is Qwen Code's server entry: the stdio command, with its tools
// loaded at the start (alwaysLoadTools) rather than deferred behind
// tool_search, where the model would not see speak or its description.
func qwenEntry(exe string) map[string]any {
	e := stdioEntry(exe)
	e["alwaysLoadTools"] = true
	return e
}

// qwenSpeak is Qwen's allowance: the allow entry, and the entry loading
// reminal's tools up front, which an entry an older reminal wrote lacks.
func qwenSpeak() *speakAllow {
	a := allowListSpeak(".qwen/settings.json")
	list := a.allowed
	a.allowed = func(home string) bool {
		e, _ := jsonEntry(filepath.Join(home, ".qwen/settings.json"), []string{"mcpServers"}, mcpServerName)
		return list(home) && e != nil && e["alwaysLoadTools"] == true
	}
	return a
}
