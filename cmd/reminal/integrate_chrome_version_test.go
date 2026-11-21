// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	chromeext "reminal/extensions/chrome"
)

// The extension reminal writes out carries a version made from its files,
// in the form a browser takes (at most four numbers, each at most 65535),
// and the host says the same one: a browser running other files then knows,
// and the files changing changes it. The copy kept in the repository stays
// at its own version.
func TestChromeExtensionIsWrittenWithItsFilesVersion(t *testing.T) {
	v := chromeext.Version()
	if !regexp.MustCompile(`^0\.1\.\d{1,5}\.\d{1,5}$`).MatchString(v) {
		t.Fatalf("version %q is not one a browser takes", v)
	}
	if v != chromeext.Version() {
		t.Fatal("the version is not the same twice")
	}
	dir := filepath.Join(t.TempDir(), "ext")
	if err := writeChromeExtension(dir); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Version string `json:"version"`
		Key     string `json:"key"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("the written manifest is not JSON: %v", err)
	}
	if m.Version != v {
		t.Errorf("written manifest version %q, the host says %q", m.Version, v)
	}
	if m.Key == "" {
		t.Error("the written manifest lost its key (the extension's id)")
	}
	kept, _ := chromeext.Files.ReadFile("manifest.json")
	if regexp.MustCompile(`"version":\s*"` + regexp.QuoteMeta(v) + `"`).Match(kept) {
		t.Error("the kept manifest already has the written version: nothing to tell them apart")
	}
	// An older reminal's files there are brought up to this one's; a folder
	// no setup wrote is left alone.
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"version": "0.1.1.1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := refreshChromeExtension(dir); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(filepath.Join(dir, "manifest.json")); string(again) != string(raw) {
		t.Error("an older version's files were not brought up to this one's")
	}
	none := filepath.Join(t.TempDir(), "none")
	if err := refreshChromeExtension(none); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(none); err == nil {
		t.Error("a folder no setup wrote was created")
	}
	// Every other file is written as kept.
	for _, p := range []string{"background.js", "popup.js", "page.js"} {
		a, _ := chromeext.Files.ReadFile(p)
		b, _ := os.ReadFile(filepath.Join(dir, p))
		if string(a) != string(b) {
			t.Errorf("%s is not written as kept", p)
		}
	}
}
