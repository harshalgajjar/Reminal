// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"version": "0.1.1.1", "key": "`+m.Key+`"}`), 0o644); err != nil {
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
	// Every other file is written as kept (the worker's script but for the
	// version it says: TestTheWorkerIsWrittenSayingItsFilesVersion).
	for _, p := range []string{"rules.js", "popup.js", "page.js"} {
		a, _ := chromeext.Files.ReadFile(p)
		b, _ := os.ReadFile(filepath.Join(dir, p))
		if string(a) != string(b) {
			t.Errorf("%s is not written as kept", p)
		}
	}
}

// The extension written out has its policy schema beside its manifest, and
// the manifest names it: without the file the browser gives the extension no
// policy at all, and an administrator's starting values are never seen.
func TestChromeExtensionIsWrittenWithItsPolicySchema(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ext")
	if err := writeChromeExtension(dir); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Storage struct {
			Schema string `json:"managed_schema"`
		} `json:"storage"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("the written manifest is not JSON: %v", err)
	}
	if m.Storage.Schema == "" {
		t.Fatal("the written manifest names no policy schema")
	}
	got, err := os.ReadFile(filepath.Join(dir, m.Storage.Schema))
	if err != nil {
		t.Fatalf("the schema the manifest names was not written: %v", err)
	}
	var s struct {
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(got, &s); err != nil {
		t.Fatalf("the written schema is not JSON: %v", err)
	}
	// Only the switches that are off as installed: never the main switch
	// (agents), which is the person's alone.
	want := []string{"cookies", "passwords", "scripts", "uploads"}
	if len(s.Properties) != len(want) {
		t.Errorf("the schema has %d properties, want exactly %v", len(s.Properties), want)
	}
	for _, k := range want {
		if s.Properties[k].Type != "string" {
			t.Errorf("the schema has no string property %q", k)
		}
	}
}

// The worker's script is written out saying the version of the files it
// came with, the same one as the manifest's: a browser that starts a script
// it kept from before on newer files reads the newer manifest either way,
// and only the script's own version shows that it is the older one.
func TestTheWorkerIsWrittenSayingItsFilesVersion(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ext")
	if err := writeChromeExtension(dir); err != nil {
		t.Fatal(err)
	}
	js, err := os.ReadFile(filepath.Join(dir, "background.js"))
	if err != nil {
		t.Fatal(err)
	}
	want := "\nconst CODE = '" + chromeext.Version() + "';\n"
	if n := strings.Count(string(js), want); n != 1 {
		t.Fatalf("the written worker says its version %d times, want once: %q", n, want)
	}
	mf, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mf), `"version": "`+chromeext.Version()+`"`) {
		t.Fatalf("the written manifest's version is not the worker's (%s)", chromeext.Version())
	}
}
