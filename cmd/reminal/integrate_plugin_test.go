// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// reminal's plugin is written, rewritten and removed; a file of the same
// name that is not reminal's is never touched.
func TestApplyPlugin(t *testing.T) {
	home := t.TempDir()
	p := &pluginFile{path: ".config/opencode/plugin/reminal.js", body: opencodePlugin}
	path := filepath.Join(home, p.path)
	if err := applyPlugin(p, home, "/opt/reminal", false); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil || !strings.HasPrefix(string(b), pluginMark) || !strings.Contains(string(b), `"/opt/reminal"`) {
		t.Fatalf("written: %v\n%s", err, b)
	}
	if err := applyPlugin(p, home, "/usr/bin/reminal", false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), `"/usr/bin/reminal"`) {
		t.Fatal("not rewritten for where reminal is now")
	}
	if err := applyPlugin(p, home, "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("not removed")
	}
	if err := applyPlugin(p, home, "", true); err != nil {
		t.Fatalf("removing what is not there: %v", err)
	}
	if err := os.WriteFile(path, []byte("// someone's own\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if applyPlugin(p, home, "/opt/reminal", false) == nil || applyPlugin(p, home, "", true) == nil {
		t.Fatal("a plugin that is not reminal's was taken over")
	}
	if b, _ := os.ReadFile(path); string(b) != "// someone's own\n" {
		t.Fatal("a plugin that is not reminal's was changed")
	}
}

// A plugin an older reminal wrote is not set up as this one would: check
// says so (not current), and the Machines view offers to set it up again,
// as it does for pi's extension. A missing plugin is not current either.
func TestAnOlderPluginIsNotCurrent(t *testing.T) {
	home := t.TempDir()
	exe := "/usr/local/bin/reminal"
	oc := targetNamed(t, "opencode")
	writeJSON(t, filepath.Join(home, ".config", "opencode", "opencode.json"), map[string]any{
		"mcp": map[string]any{"reminal": map[string]any{"type": "local", "command": []string{exe, "mcp"}, "enabled": true}},
	})
	if c := checkIntegration(oc, home, exe); !c.Integrated || c.Current {
		t.Fatalf("no plugin: %+v", c)
	}
	if err := applyPlugin(oc.plugin, home, exe, false); err != nil {
		t.Fatal(err)
	}
	if c := checkIntegration(oc, home, exe); !c.Current {
		t.Fatalf("this binary's plugin: %+v", c)
	}
	path := filepath.Join(home, oc.plugin.path)
	older := strings.Replace(opencodePlugin(exe), "let writing = false;", "", 1)
	if older == opencodePlugin(exe) {
		t.Fatal("the plugin no longer queues its reports")
	}
	if err := os.WriteFile(path, []byte(older), 0o644); err != nil {
		t.Fatal(err)
	}
	if c := checkIntegration(oc, home, exe); c.Current {
		t.Fatalf("an older plugin read as current: %+v", c)
	}
}

// A plugin an older reminal wrote is written again as this binary writes
// it, for the reminal it already calls, with nobody running integrate; a
// current one, or a file that is not reminal's, is left as it is.
func TestHealPluginsRewritesOnlyReminalsOlderPlugin(t *testing.T) {
	home := t.TempDir()
	oc := targetNamed(t, "opencode")
	path := filepath.Join(home, oc.plugin.path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	bin := "/home/u/.local/bin/reminal"
	older := strings.Replace(opencodePlugin(bin), "let writing = false;", "", 1)
	if err := os.WriteFile(path, []byte(older), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := healPlugins(home); len(got) != 1 || got[0] != oc.plugin.path {
		t.Fatalf("healed %v, want %s", got, oc.plugin.path)
	}
	if b, _ := os.ReadFile(path); string(b) != opencodePlugin(bin) {
		t.Fatalf("not this binary's plugin for %s:\n%s", bin, b)
	}
	if got := healPlugins(home); len(got) != 0 {
		t.Errorf("a current plugin written again: %v", got)
	}
	theirs := "// someone's own\nconst BIN = \"/usr/bin/reminal\";\n"
	if err := os.WriteFile(path, []byte(theirs), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := healPlugins(home); len(got) != 0 {
		t.Errorf("someone's own plugin written over: %v", got)
	}
	if b, _ := os.ReadFile(path); string(b) != theirs {
		t.Errorf("someone's own plugin changed:\n%s", b)
	}
	if bin := pluginBin(opencodePlugin(`/opt/re"minal`)); bin != `/opt/re"minal` {
		t.Errorf("pluginBin: %q", bin)
	}
}

// Every agent that loads reminal's plugin gets its older copy healed: Kilo
// Code CLI's, in its own plugin dir, as well as OpenCode's.
func TestHealPluginsCoversEveryPluginAgent(t *testing.T) {
	home := t.TempDir()
	bin := "/home/u/.local/bin/reminal"
	var want []string
	for _, a := range []string{"opencode", "kilo"} {
		p := targetNamed(t, a).plugin
		if p == nil {
			t.Fatalf("%s has no plugin", a)
		}
		path := filepath.Join(home, p.path)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(strings.Replace(p.body(bin), "let writing = false;", "", 1)), 0o644); err != nil {
			t.Fatal(err)
		}
		want = append(want, p.path)
	}
	got := healPlugins(home)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("healed %v, want %v", got, want)
	}
	for _, a := range []string{"opencode", "kilo"} {
		p := targetNamed(t, a).plugin
		if b, _ := os.ReadFile(filepath.Join(home, p.path)); string(b) != p.body(bin) {
			t.Errorf("%s: not this binary's plugin", a)
		}
	}
}
