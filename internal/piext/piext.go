// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

// Package piext installs reminal's pi extension.
//
// pi has no `mcp add` subcommand and no MCP client to register with: it takes an
// extension instead — a small TypeScript module it discovers from its own
// extensions directory. That turns out to be the better deal for both jobs at
// once. The extension asks reminal which tools this machine offers and registers
// each one with pi directly, so pi gets exactly what reminal exposes and nothing
// it invented; and pi's lifecycle events are exact, so the session reports
// working / needs you / done instead of reminal reading it off the screen.
//
// The extension's sources are embedded in the reminal binary, so `reminal
// integrate` installs it with no network and no npm, and what lands is exactly
// what the installing reminal shipped. Note it is written at integrate time and
// not at upgrade time: a later `reminal upgrade` leaves the installed copy where
// it is, so re-running integrate is what picks up a newer one.
package piext

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// The extension itself. Listed file by file rather than with a directory
// pattern: the test harness beside it has no business in the shipped binary.
//
//go:embed extension/package.json extension/index.ts extension/mcp.ts
var files embed.FS

// dirName is the folder the extension is installed as. pi shows a discovered
// extension under its directory name, so this is what a user sees in `pi config`
// — and it is what Remove looks for, so it must not drift.
const dirName = "reminal"

// marker identifies the extension as ours. Remove refuses to delete a directory
// whose package.json does not carry this name, so a folder that happens to share
// the name is never somebody else's loss.
const marker = "reminal-pi"

// AgentDir returns pi's config directory for this user — the same one pi itself
// resolves, honouring its override so a user who moved pi's config still gets
// the extension where pi will look for it.
func AgentDir(home string) string {
	if dir := strings.TrimSpace(os.Getenv("PI_CODING_AGENT_DIR")); dir != "" {
		if strings.HasPrefix(dir, "~") {
			dir = filepath.Join(home, strings.TrimPrefix(dir, "~"))
		}
		return dir
	}
	return filepath.Join(home, ".pi", "agent")
}

// Dir is where the extension is installed.
func Dir(home string) string {
	return filepath.Join(AgentDir(home), "extensions", dirName)
}

// Install writes the extension into pi's extensions directory, replacing any
// earlier copy, and records the path of the reminal that installed it.
//
// exe is baked in deliberately. The extension runs `reminal hook` and `reminal
// mcp` as child processes, and pi's own PATH is whatever the shell that launched
// it had — which, right after an install, frequently does not include reminal
// yet. An absolute path costs nothing and removes the whole class of "it works
// in my terminal but not under pi".
func Install(home, exe string) error {
	dir := Dir(home)
	// Build it beside the real thing and move it into place at the end.
	//
	// Writing in place would mean a window — a Ctrl-C, a full disk — where the
	// directory holds half an extension. pi loads a bare index.ts with no
	// manifest, so it would try that wreckage on every start; and since the file
	// that identifies the directory as ours is one of the ones not written yet,
	// neither a re-install nor a remove could touch it afterwards. A rename is
	// one step: either the old extension is there or the new one is.
	// A name of its own per run: two integrates at once sharing one staging path
	// would delete each other's half-written files and then rename a directory
	// with pieces missing — which, carrying a valid manifest, looks legitimate to
	// everything downstream while pi finds no entry point in it.
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(filepath.Dir(dir), dirName+".installing-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(staging) }()

	err = fs.WalkDir(files, "extension", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel("extension", filepath.FromSlash(p))
		if err != nil {
			return err
		}
		dst := filepath.Join(staging, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		body, err := files.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, body, 0o644)
	})
	if err != nil {
		return err
	}

	pin, err := json.Marshal(map[string]string{"bin": exe})
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(staging, "bin.json"), append(pin, '\n'), 0o644); err != nil {
		return err
	}

	// Only now is there anything worth replacing the old extension with. A
	// leftover file from a version that no longer ships it would still be loaded
	// by pi, so the old directory goes rather than being written over.
	if err := removeIfOurs(dir); err != nil {
		return err
	}
	return os.Rename(staging, dir)
}

// Remove uninstalls the extension. Absent is success: `integrate --remove` is
// something a user may run twice, or on a machine that never had pi.
func Remove(home string) error {
	return removeIfOurs(Dir(home))
}

// removeIfOurs deletes the extension directory unless something else is plainly
// living in it.
//
// What counts as plainly is a manifest naming a different package — that is the
// case worth protecting, and the only one we can be sure about. A directory with
// no manifest at all is not evidence of somebody else: it is what a half-written
// install leaves behind, since the manifest is one of the files it had not
// reached yet. Refusing those meant reminal could neither repair nor remove its
// own wreckage, and told the user it belonged to someone else.
func removeIfOurs(dir string) error {
	raw, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if os.IsNotExist(err) {
		if _, statErr := os.Stat(dir); os.IsNotExist(statErr) {
			return nil // nothing installed
		}
		return os.RemoveAll(dir) // ours, half-written, or abandoned in our slot
	}
	if err != nil {
		return err
	}
	var manifest struct{ Name string }
	if err := json.Unmarshal(raw, &manifest); err == nil && manifest.Name != "" && manifest.Name != marker {
		return fmt.Errorf("%s belongs to %q, not reminal; leaving it alone", dir, manifest.Name)
	}
	return os.RemoveAll(dir)
}
