package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"reminal/internal/piext"
)

func targetNamed(t *testing.T, bin string) agentTarget {
	t.Helper()
	for _, tg := range agentTargets() {
		if tg.Bin == bin {
			return tg
		}
	}
	t.Fatalf("no target %q", bin)
	return agentTarget{}
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	raw, _ := json.Marshal(v)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestCheckReadsBackWhatIntegrateWrote round-trips through the real writers:
// whatever applyViaFile/applyHooks put down, checkIntegration must see — so a
// change to one format cannot silently make --check lie.
func TestCheckReadsBackWhatIntegrateWrote(t *testing.T) {
	home := t.TempDir()
	exe := filepath.Join(home, "reminal")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, bin := range []string{"gemini", "qwen", "opencode", "cursor-agent", "amp"} {
		tg := targetNamed(t, bin)
		before := checkIntegration(tg, home, exe)
		if before.Integrated || !before.Known || before.ConfigMtime != 0 {
			t.Fatalf("%s before: %+v", bin, before)
		}
		if err := applyViaFile(tg, home, exe, false); err != nil {
			t.Fatalf("%s apply: %v", bin, err)
		}
		if tg.hooks != nil {
			if err := applyHooks(tg.hooks, home, exe, false); err != nil {
				t.Fatalf("%s hooks: %v", bin, err)
			}
		}
		if tg.plugin != nil {
			if err := applyPlugin(tg.plugin, home, exe, false); err != nil {
				t.Fatalf("%s plugin: %v", bin, err)
			}
		}
		after := checkIntegration(tg, home, exe)
		if !after.Integrated || !after.Current || after.ConfigMtime == 0 || after.Error != "" {
			t.Fatalf("%s after apply: %+v", bin, after)
		}
		if tg.hooks != nil && !after.Hooks {
			t.Fatalf("%s: hooks written but not seen: %+v", bin, after)
		}
		if after.Restart == "" || after.Resume == "" {
			t.Fatalf("%s: no restart hint / resume command: %+v", bin, after)
		}
		// A registration left by another reminal is set up, but not for us.
		other := checkIntegration(tg, home, filepath.Join(home, "elsewhere", "reminal"))
		if !other.Integrated || other.Current {
			t.Fatalf("%s other exe: %+v", bin, other)
		}
		if err := applyViaFile(tg, home, exe, true); err != nil {
			t.Fatal(err)
		}
		if tg.hooks != nil {
			_ = applyHooks(tg.hooks, home, exe, true)
		}
		gone := checkIntegration(tg, home, exe)
		if gone.Integrated || gone.Hooks {
			t.Fatalf("%s after remove: %+v", bin, gone)
		}
	}
}

// TestCheckReadsCLIRouteConfigs covers the agents whose own `mcp add` writes
// the file: we read the formats they produce (fixtures copied from real
// installs), not something we wrote ourselves.
func TestCheckReadsCLIRouteConfigs(t *testing.T) {
	home := t.TempDir()
	exe := "/home/u/.local/bin/reminal"

	writeJSON(t, filepath.Join(home, ".claude.json"), map[string]any{
		"numStartups": 3,
		"mcpServers":  map[string]any{"reminal": map[string]any{"type": "stdio", "command": exe, "args": []string{"mcp"}}},
	})
	claude := checkIntegration(targetNamed(t, "claude"), home, exe)
	if !claude.Integrated || !claude.Current || claude.Hooks || !claude.HooksWanted {
		t.Fatalf("claude: %+v", claude)
	}
	if err := applyHooks(targetNamed(t, "claude").hooks, home, exe, false); err != nil {
		t.Fatal(err)
	}
	if c := checkIntegration(targetNamed(t, "claude"), home, exe); !c.Hooks {
		t.Fatalf("claude hooks not seen: %+v", c)
	}

	writeJSON(t, filepath.Join(home, ".gemini", "config", "mcp_config.json"), map[string]any{
		"mcpServers": map[string]any{"reminal": map[string]any{"args": []string{"mcp"}, "command": exe, "disabled": false}},
	})
	agy := checkIntegration(targetNamed(t, "agy"), home, exe)
	if !agy.Integrated || !agy.Current {
		t.Fatalf("agy: %+v", agy)
	}

	toml := "model = \"o3\"\n\n[mcp_servers.reminal]\ncommand = \"" + exe + "\"\nargs = [\"mcp\"]\n\n[mcp_servers.other]\ncommand = \"/x\"\n"
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	codex := checkIntegration(targetNamed(t, "codex"), home, exe)
	if !codex.Integrated || !codex.Current {
		t.Fatalf("codex: %+v", codex)
	}
	stale := checkIntegration(targetNamed(t, "codex"), home, "/opt/reminal")
	if !stale.Integrated || stale.Current {
		t.Fatalf("codex stale: %+v", stale)
	}

	// Broken JSON is an error, not "not set up": integrate refuses to touch
	// such a file, so the answer must not be "run integrate".
	if err := os.WriteFile(filepath.Join(home, ".qwen", "settings.json"), []byte("{oops"), 0o600); err != nil {
		os.MkdirAll(filepath.Join(home, ".qwen"), 0o755)
		os.WriteFile(filepath.Join(home, ".qwen", "settings.json"), []byte("{oops"), 0o600)
	}
	qwen := checkIntegration(targetNamed(t, "qwen"), home, exe)
	if qwen.Error == "" || qwen.Integrated {
		t.Fatalf("qwen broken json: %+v", qwen)
	}
}

// TestIntegrationReportJSONShape pins the wire shape the Machines view reads.
func TestIntegrationReportJSONShape(t *testing.T) {
	home := t.TempDir()
	rep := integrationReport(home, "/x/reminal", []string{"claude"})
	if len(rep) != 1 || rep[0].Bin != "claude" {
		t.Fatalf("report: %+v", rep)
	}
	raw, _ := json.Marshal(rep)
	var back []map[string]any
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"bin", "name", "installed", "integrated", "current", "hooks", "hooks_wanted", "known", "restart"} {
		if _, ok := back[0][k]; !ok {
			t.Fatalf("missing %q in %s", k, raw)
		}
	}
}

// TestSetUpTimeAndBackupOnce: integrate notes when it set an agent up (the
// config's own mtime says nothing — Claude rewrites its file as it runs),
// forgets it on remove, and keeps the FIRST backup of a config rather than
// overwriting it with a file it had already changed.
func TestSetUpTimeAndBackupOnce(t *testing.T) {
	home := t.TempDir()
	exe := filepath.Join(home, "reminal")
	tg := targetNamed(t, "gemini")
	writeJSON(t, filepath.Join(home, tg.file), map[string]any{"theme": "dark"})
	if before := checkIntegration(tg, home, exe); before.Since != 0 {
		t.Fatalf("since before any setup: %+v", before)
	}
	if err := applyViaFile(tg, home, exe, false); err != nil {
		t.Fatal(err)
	}
	noteSetUp(home, tg.Bin, true)
	after := checkIntegration(tg, home, exe)
	if after.Since == 0 || time.Since(time.Unix(after.Since, 0)) > time.Minute {
		t.Fatalf("since after setup: %+v", after)
	}
	bak, _ := os.ReadFile(filepath.Join(home, tg.file+".bak"))
	if !strings.Contains(string(bak), "dark") || strings.Contains(string(bak), "reminal") {
		t.Fatalf("the backup should be the file before reminal touched it: %s", bak)
	}
	// A second run must not replace that backup with the changed file.
	if err := applyViaFile(tg, home, exe, false); err != nil {
		t.Fatal(err)
	}
	bak2, _ := os.ReadFile(filepath.Join(home, tg.file+".bak"))
	if string(bak2) != string(bak) {
		t.Fatalf("the backup was overwritten on a re-run")
	}
	noteSetUp(home, tg.Bin, false)
	if gone := checkIntegration(tg, home, exe); gone.Since != 0 {
		t.Fatalf("since after remove: %+v", gone)
	}
}

// The pi target's registration is a package in pi's extensions directory, not a
// JSON entry, so --check reads it back differently — and the part that is easy
// to lose is "current". `reminal upgrade` replaces the binary where it already
// was and leaves the extension alone, so the path it records still matches while
// pi loads older code. This goes through checkIntegration rather than the helper
// it calls, because it is the wiring that decides whether --check tells the truth.
func TestCheckNoticesAPiExtensionFromAnOlderReminal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", "")
	exe := filepath.Join(home, "reminal")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	tg := targetNamed(t, "pi")

	if st := checkIntegration(tg, home, exe); st.Integrated || st.Current {
		t.Fatalf("nothing installed, yet integrated=%v current=%v", st.Integrated, st.Current)
	}

	if err := piext.Install(home, exe); err != nil {
		t.Fatalf("install: %v", err)
	}
	st := checkIntegration(tg, home, exe)
	if !st.Integrated || !st.Current {
		t.Fatalf("a fresh install reads integrated=%v current=%v", st.Integrated, st.Current)
	}

	// What an older reminal left behind: the same path it records, different
	// sources. Still integrated — pi will load it — but not what this binary
	// would write, so not current.
	stale := filepath.Join(piext.Dir(home), "index.ts")
	if err := os.WriteFile(stale, []byte("// an older build\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if st := checkIntegration(tg, home, exe); !st.Integrated || st.Current {
		t.Errorf("an extension from an older reminal reads integrated=%v current=%v, want true/false",
			st.Integrated, st.Current)
	}

	// Re-running integrate is the remedy --check points at, so it has to work.
	if err := piext.Install(home, exe); err != nil {
		t.Fatalf("reinstall: %v", err)
	}
	if st := checkIntegration(tg, home, exe); !st.Current {
		t.Error("re-installing left --check still reporting it as not current")
	}

	// A file missing entirely is a half-install, and equally not current.
	if err := os.Remove(filepath.Join(piext.Dir(home), "mcp.ts")); err != nil {
		t.Fatal(err)
	}
	if st := checkIntegration(tg, home, exe); st.Current {
		t.Error("an install missing a file reads as current")
	}
}
