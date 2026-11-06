// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Kilo Code CLI gets reminal in a kilo.json of its own beside the person's
// kilo.jsonc (which keeps its comments, untouched), and OpenCode's plugin in
// its own plugin dir; --remove takes both out again. check reads it back.
func TestKiloGetsItsOwnConfigAndThePlugin(t *testing.T) {
	home := t.TempDir()
	exe := "/usr/local/bin/reminal"
	k := targetNamed(t, "kilo")
	theirs := "{\n  // the person's own\n  \"model\": \"standin/gpt-4.1\"\n}\n"
	jsonc := filepath.Join(home, ".config", "kilo", "kilo.jsonc")
	if err := os.MkdirAll(filepath.Dir(jsonc), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(jsonc, []byte(theirs), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := applyViaFile(k, home, exe, false); err != nil {
		t.Fatal(err)
	}
	if err := applyPlugin(k.plugin, home, exe, false); err != nil {
		t.Fatal(err)
	}
	var got struct {
		MCP map[string]map[string]any `json:"mcp"`
	}
	b, _ := os.ReadFile(filepath.Join(home, ".config", "kilo", "kilo.json"))
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	e := got.MCP[mcpServerName]
	if cmd, _ := json.Marshal(e["command"]); e["type"] != "local" || string(cmd) != `["`+exe+`","mcp"]` || e["enabled"] != true {
		t.Errorf("kilo.json: %s", b)
	}
	if b, _ := os.ReadFile(jsonc); string(b) != theirs {
		t.Errorf("the person's kilo.jsonc changed: %s", b)
	}
	plugin, err := os.ReadFile(filepath.Join(home, ".config", "kilo", "plugin", "reminal.js"))
	if err != nil || string(plugin) != opencodePlugin(exe) {
		t.Errorf("plugin: %v", err)
	}
	if c := checkIntegration(k, home, exe); !c.Integrated || !c.Current {
		t.Errorf("check: %+v", c)
	}
	if err := applyViaFile(k, home, exe, true); err != nil {
		t.Fatal(err)
	}
	if err := applyPlugin(k.plugin, home, exe, true); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".config", "kilo", "kilo.json")); strings.Contains(string(b), mcpServerName) {
		t.Errorf("after --remove: %s", b)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "kilo", "plugin", "reminal.js")); !os.IsNotExist(err) {
		t.Errorf("the plugin is still there: %v", err)
	}
}
