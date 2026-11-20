// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package cloudflare

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"reminal/internal/viewerbuild"
)

// nodeOrSkip is node, or a skip where there is none — but never in CI, where
// what these hold must be checked.
func nodeOrSkip(t *testing.T) string {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("node is needed here: the Worker's viewer build is checked with it")
		}
		t.Skip("node not installed")
	}
	return node
}

// The Worker serves the viewer page the binary serves, byte for byte: the
// page and every file built from internal/client/web by
// scripts/build-viewer.mjs are what internal/viewerbuild renders from the
// same sources. They follow one rule (internal/client/web/build.json), in
// two languages; this is what keeps them one — for the tree's sources, and
// for sources whose bytes are not plain UTF-8 text.
func TestTheWorkersViewerIsTheBinarys(t *testing.T) {
	node := nodeOrSkip(t)
	tree := filepath.Join("..", "internal", "client", "web")
	odd := t.TempDir()
	if err := os.CopyFS(odd, os.DirFS(tree)); err != nil {
		t.Fatal(err)
	}
	index, _ := os.ReadFile(filepath.Join(tree, "index.html"))
	index = append([]byte("\xef\xbb\xbf<!-- \xe9\xff -->\r\n"), bytes.ReplaceAll(index, []byte("\n"), []byte("\r\n"))...)
	if err := os.WriteFile(filepath.Join(odd, "index.html"), index, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(odd, "app.js"), []byte("a('\xe9 $& $1 $$')\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, web := range []string{tree, odd} {
		worker, binary := filepath.Join(t.TempDir(), "public"), filepath.Join(t.TempDir(), "new", "dir")
		cmd := exec.Command(node, filepath.Join("scripts", "build-viewer.mjs"), "--dev", "--web", web, "--out", worker)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build-viewer.mjs: %v\n%s", err, out)
		}
		if err := viewerbuild.Write(web, binary); err != nil {
			t.Fatal(err)
		}
		same(t, web, worker, binary)
	}
}

// same fails t for each file the two builds of web do not have alike.
func same(t *testing.T, web, worker, binary string) {
	t.Helper()
	files := map[string]bool{}
	for _, dir := range []string{worker, binary} {
		_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				rel, _ := filepath.Rel(dir, p)
				files[rel] = true
			}
			return nil
		})
	}
	if len(files) < 5 {
		t.Fatalf("%s: only %d files built", web, len(files))
	}
	for rel := range files {
		a, errA := os.ReadFile(filepath.Join(worker, rel))
		b, errB := os.ReadFile(filepath.Join(binary, rel))
		switch {
		case errA != nil:
			t.Errorf("%s: %s: only the binary has it", web, rel)
		case errB != nil:
			t.Errorf("%s: %s: only the Worker has it", web, rel)
		case !bytes.Equal(a, b):
			t.Errorf("%s: %s: the Worker's and the binary's differ (%d vs %d bytes)", web, rel, len(a), len(b))
		}
	}
}

// The builder's own rules: naming, SRI, stamping as bytes, a strict
// build.json; keeping a live Worker's builds — only from it, at most
// build.json's keep, each line its scripts in order, each file checked
// against its name, every read capped — and only for the build they were
// kept for; failing a deploy that cannot read them, or has none kept.
func TestTheWorkersViewerBuilder(t *testing.T) {
	node := nodeOrSkip(t)
	cmd := exec.Command(node, "--test", filepath.Join("scripts", "build-viewer.test.mjs"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// wrangler.toml's top-level keys stay above its first table: every key after
// a table header belongs to that table, so an account_id or routes below
// [build] would be build's, and a deploy would go to no route of its own.
func TestWranglerTomlKeepsItsKeysAboveItsTables(t *testing.T) {
	b, err := os.ReadFile("wrangler.toml")
	if err != nil {
		t.Fatal(err)
	}
	top := map[string]bool{}
	table := ""
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "["):
			table = line
		case table == "" && strings.Contains(line, " = "):
			top[strings.SplitN(line, " = ", 2)[0]] = true
		case table == "[build]" && strings.Contains(line, " = "):
			if k := strings.SplitN(line, " = ", 2)[0]; k != "command" && k != "watch_dir" {
				t.Errorf("[build] has %s", k)
			}
		}
	}
	for _, k := range []string{"name", "main", "account_id", "workers_dev", "routes"} {
		if !top[k] {
			t.Errorf("%s is not above wrangler.toml's first table", k)
		}
	}
}

// The voice frame's integrity check holds against a server that lies about
// lengths, chunks or encoding (voice/assets/integrity.test.mjs).
func TestVoiceIntegrityAgainstALyingServer(t *testing.T) {
	node := nodeOrSkip(t)
	cmd := exec.Command(node, "--test", filepath.Join("..", "voice", "assets", "integrity.test.mjs"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// The voice frame's cut-in on long talk: stopping a moment after hearing
// "Sorry," takes it, and what was said before stopping is not a turn that
// the agent answers over it (voice/assets/conversation.test.mjs).
func TestVoiceCutInIsTakenWhenThePersonStops(t *testing.T) {
	node := nodeOrSkip(t)
	cmd := exec.Command(node, "--test", "--test-timeout=60000", filepath.Join("..", "voice", "assets", "conversation.test.mjs"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}
