// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package cloudflare

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
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
// two languages; this is what keeps them one.
func TestTheWorkersViewerIsTheBinarys(t *testing.T) {
	node := nodeOrSkip(t)
	web := filepath.Join("..", "internal", "client", "web")
	worker, binary := t.TempDir(), t.TempDir()
	cmd := exec.Command(node, filepath.Join("scripts", "build-viewer.mjs"), "--out", worker)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build-viewer.mjs: %v\n%s", err, out)
	}
	if err := viewerbuild.Write(web, binary); err != nil {
		t.Fatal(err)
	}
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
		t.Fatalf("only %d files built", len(files))
	}
	for rel := range files {
		a, errA := os.ReadFile(filepath.Join(worker, rel))
		b, errB := os.ReadFile(filepath.Join(binary, rel))
		switch {
		case errA != nil:
			t.Errorf("%s: only the binary has it", rel)
		case errB != nil:
			t.Errorf("%s: only the Worker has it", rel)
		case !bytes.Equal(a, b):
			t.Errorf("%s: the Worker's and the binary's differ (%d vs %d bytes)", rel, len(a), len(b))
		}
	}
}

// The builder's own rules: naming, SRI, stamping, keeping a live Worker's
// builds — at most build.json's keep, each checked against its name, each
// file and the list capped — and failing a deploy that cannot read them.
func TestTheWorkersViewerBuilder(t *testing.T) {
	node := nodeOrSkip(t)
	cmd := exec.Command(node, "--test", filepath.Join("scripts", "build-viewer.test.mjs"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}
