// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveSpawnDirExplicit(t *testing.T) {
	dir := t.TempDir()
	got, err := resolveSpawnDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Clean(dir) {
		t.Fatalf("got %q, want %q", got, dir)
	}
}

func TestResolveSpawnDirRejectsRelative(t *testing.T) {
	if _, err := resolveSpawnDir("relative/path"); err == nil {
		t.Fatal("expected error for relative cwd")
	}
}

func TestResolveSpawnDirRejectsMissing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-dir")
	if _, err := resolveSpawnDir(missing); err == nil {
		t.Fatal("expected error for missing cwd")
	}
}

func TestResolveSpawnDirRejectsFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "file")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := resolveSpawnDir(f)
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("got %v, want not-a-directory", err)
	}
}

func TestResolveSpawnDirEmptyInheritsOrHome(t *testing.T) {
	got, err := resolveSpawnDir("")
	if err != nil {
		t.Fatal(err)
	}
	wd, werr := os.Getwd()
	if werr == nil && wd != "/" {
		if got != "" {
			t.Fatalf("from a real directory, empty cwd should inherit (got %q)", got)
		}
		return
	}
	home, herr := os.UserHomeDir()
	if herr != nil {
		t.Skip("no home")
	}
	if got != home {
		t.Fatalf("from /, empty cwd should be home %q, got %q", home, got)
	}
}
