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
// writes it for an MCP tool (its auto-saved.toml), its priority too.
const geminiSpeakPolicy = `# Written by reminal integrate: Gemini CLI runs reminal's speak tool (how
# it answers out loud to a person talking to it by voice in reminal) without
# asking each time. Only speak: reminal's other tools still ask.
# reminal integrate --remove deletes this file.
[[rule]]
mcpName = "` + mcpServerName + `"
toolName = "speak"
decision = "allow"
priority = 950
`

func geminiSpeak() *speakAllow {
	return &speakAllow{
		how: "speak allowed",
		apply: func(home string, remove bool) error {
			path := filepath.Join(home, geminiSpeakPolicyFile)
			if remove {
				if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
					return err
				}
				return nil
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			return writeAgentConfig(path, []byte(geminiSpeakPolicy))
		},
		allowed: func(home string) bool {
			b, err := os.ReadFile(filepath.Join(home, geminiSpeakPolicyFile))
			return err == nil && string(b) == geminiSpeakPolicy
		},
	}
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
