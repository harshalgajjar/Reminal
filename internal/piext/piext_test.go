// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package piext

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallWritesADiscoverableExtension(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", "")

	if err := Install(home, "/opt/reminal/reminal"); err != nil {
		t.Fatalf("install: %v", err)
	}

	dir := filepath.Join(home, ".pi", "agent", "extensions", "reminal")
	// pi discovers a directory by its package.json manifest, and loads exactly
	// the entry points that names. Every one of them has to be there.
	raw, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	var manifest struct {
		Name string
		Pi   struct{ Extensions []string }
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("manifest is not valid JSON: %v", err)
	}
	if manifest.Name != marker {
		t.Errorf("manifest name = %q, want %q (Remove keys off this)", manifest.Name, marker)
	}
	if len(manifest.Pi.Extensions) == 0 {
		t.Fatal("manifest declares no entry points, so pi would load nothing")
	}
	for _, entry := range manifest.Pi.Extensions {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(entry))); err != nil {
			t.Errorf("manifest points at %s, which was not installed: %v", entry, err)
		}
	}

	// The extension shells out to reminal, so it has to know which reminal.
	pin, err := os.ReadFile(filepath.Join(dir, "bin.json"))
	if err != nil {
		t.Fatalf("bin.json: %v", err)
	}
	var pinned struct{ Bin string }
	if err := json.Unmarshal(pin, &pinned); err != nil {
		t.Fatalf("bin.json is not valid JSON: %v", err)
	}
	if pinned.Bin != "/opt/reminal/reminal" {
		t.Errorf("bin.json = %q, want the installing binary's path", pinned.Bin)
	}

	// The test harness is for this repo, not for users' machines.
	if _, err := os.Stat(filepath.Join(dir, "test")); !os.IsNotExist(err) {
		t.Error("shipped the test harness into the install")
	}
}

func TestInstallIsIdempotentAndRepointable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", "")
	dir := filepath.Join(home, ".pi", "agent", "extensions", "reminal")

	if err := Install(home, "/first/reminal"); err != nil {
		t.Fatalf("first install: %v", err)
	}
	// A file an older version shipped and this one does not: a second install
	// must not leave it behind for pi to load.
	stale := filepath.Join(dir, "src", "stale.ts")
	if err := os.WriteFile(stale, []byte("export default () => {};\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Install(home, "/second/reminal"); err != nil {
		t.Fatalf("second install: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("a stale file from an earlier version survived reinstall")
	}
	pin, _ := os.ReadFile(filepath.Join(dir, "bin.json"))
	if !strings.Contains(string(pin), "/second/reminal") {
		t.Errorf("reinstall did not repoint bin.json: %s", pin)
	}
}

func TestRemove(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", "")

	// Removing what was never installed is a success: people run --remove twice.
	if err := Remove(home); err != nil {
		t.Errorf("remove with nothing installed: %v", err)
	}
	if err := Install(home, "/opt/reminal/reminal"); err != nil {
		t.Fatalf("install: %v", err)
	}
	if err := Remove(home); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(Dir(home)); !os.IsNotExist(err) {
		t.Error("extension survived remove")
	}
}

func TestRemoveLeavesSomebodyElsesDirectoryAlone(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", "")
	dir := Dir(home)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	theirs := filepath.Join(dir, "package.json")
	if err := os.WriteFile(theirs, []byte(`{"name":"someone-elses-extension"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// A directory that happens to share the name is not ours to delete — and an
	// install must not quietly clobber it either.
	if err := Remove(home); err == nil {
		t.Error("removed a directory that is not reminal's extension")
	}
	if err := Install(home, "/opt/reminal/reminal"); err == nil {
		t.Error("overwrote a directory that is not reminal's extension")
	}
	if _, err := os.Stat(theirs); err != nil {
		t.Errorf("their package.json is gone: %v", err)
	}
}

func TestAgentDirFollowsPisOwnOverride(t *testing.T) {
	home := "/home/someone"
	t.Setenv("PI_CODING_AGENT_DIR", "")
	if got, want := AgentDir(home), filepath.Join(home, ".pi", "agent"); got != want {
		t.Errorf("AgentDir = %q, want %q", got, want)
	}
	// pi resolves this env var before its default, so an install that ignored it
	// would land somewhere pi never looks.
	t.Setenv("PI_CODING_AGENT_DIR", filepath.Join("/elsewhere", "pi"))
	if got, want := AgentDir(home), filepath.Join("/elsewhere", "pi"); got != want {
		t.Errorf("AgentDir with override = %q, want %q", got, want)
	}
	t.Setenv("PI_CODING_AGENT_DIR", "~/moved")
	if got, want := AgentDir(home), filepath.Join(home, "moved"); got != want {
		t.Errorf("AgentDir with ~ = %q, want %q", got, want)
	}
}
