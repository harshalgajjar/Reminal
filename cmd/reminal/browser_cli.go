// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

// `reminal browser <tool> [name=value ...]`: the browser_* tools from a
// shell, for an agent that works by commands and not by MCP. The same call
// as the MCP tool makes (mcpBrowser): only inside a reminal session on this
// computer, through that session, held to the same rules.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func runBrowserCLI(args []string) error {
	if !browserToolsHere() {
		return errors.New("the browser tools are not set up here: run `reminal integrate`, and load reminal's extension in your browser")
	}
	obj := func(props map[string]any, required ...string) map[string]any {
		m := map[string]any{"type": "object", "properties": props}
		if len(required) > 0 {
			m["required"] = required
		}
		return m
	}
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	tools := browserTools(obj, str)
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Println("reminal browser <tool> [name=value ...]   use the browser from a reminal session's shell")
		fmt.Println("reminal browser <tool> --help             a tool's arguments")
		fmt.Println()
		for _, t := range tools {
			name, _ := t["name"].(string)
			desc, _ := t["description"].(string)
			fmt.Printf("  %-18s %s\n", strings.TrimPrefix(name, "browser_"), firstSentence(desc))
		}
		return nil
	}
	name := "browser_" + strings.TrimPrefix(args[0], "browser_")
	var tool map[string]any
	for _, t := range tools {
		if t["name"] == name {
			tool = t
		}
	}
	if tool == nil {
		return fmt.Errorf("no browser tool %q: `reminal browser` lists them", args[0])
	}
	props, _ := tool["inputSchema"].(map[string]any)["properties"].(map[string]any)
	out := ""
	call := map[string]any{}
	for _, a := range args[1:] {
		switch {
		case a == "--help" || a == "-h":
			desc, _ := tool["description"].(string)
			fmt.Println(firstSentence(desc))
			keys := make([]string, 0, len(props))
			for k := range props {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				d, _ := props[k].(map[string]any)["description"].(string)
				fmt.Printf("  %-12s %s\n", k+"=", d)
			}
			return nil
		case strings.HasPrefix(a, "--out="):
			out = strings.TrimPrefix(a, "--out=")
		case strings.HasPrefix(a, "{"):
			// The arguments as one JSON object.
			if err := json.Unmarshal([]byte(a), &call); err != nil {
				return fmt.Errorf("the JSON arguments: %w", err)
			}
		default:
			k, v, ok := strings.Cut(a, "=")
			if !ok || props[k] == nil {
				return fmt.Errorf("%q: give arguments as name=value (`reminal browser %s --help` lists them)", a, args[0])
			}
			call[k] = cliValue(props[k].(map[string]any), v)
		}
	}
	blocks, err := mcpBrowser(name, call)
	if err != nil {
		return err
	}
	for _, b := range blocks {
		m, _ := b.(map[string]any)
		switch m["type"] {
		case "text":
			fmt.Println(m["text"])
		case "image":
			data, _ := m["data"].(string)
			raw, err := base64.StdEncoding.DecodeString(data)
			if err != nil {
				return err
			}
			p := out
			if p == "" {
				p = "screenshot-" + time.Now().Format("20060102-150405.000") + ".png"
			}
			if err := os.WriteFile(p, raw, 0o644); err != nil {
				if out != "" {
					return err
				}
				// No place was asked for and the shell's folder cannot be
				// written (C:\ on Windows, a read-only one): the temp folder.
				p = filepath.Join(os.TempDir(), p)
				if err := os.WriteFile(p, raw, 0o644); err != nil {
					return err
				}
			}
			out = "" // a second image gets a name of its own
			fmt.Println("Saved the image: " + p)
		}
	}
	return nil
}

// cliValue reads a name=value's value as the tool's schema types it: a
// number, true or false, a list (JSON, or comma separated), else the text.
func cliValue(schema map[string]any, v string) any {
	switch schema["type"] {
	case "number":
		var n float64
		if json.Unmarshal([]byte(v), &n) == nil {
			return n
		}
	case "boolean":
		return v == "true" || v == "1" || v == "yes"
	case "array":
		var l []any
		if json.Unmarshal([]byte(v), &l) == nil {
			return l
		}
		l = nil
		for _, s := range strings.Split(v, ",") {
			l = append(l, s)
		}
		return l
	}
	return v
}

func firstSentence(s string) string {
	if i := strings.Index(s, ". "); i > 0 {
		return s[:i+1]
	}
	return s
}
