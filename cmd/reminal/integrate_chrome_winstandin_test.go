// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// asWindows stands in for Windows: its table of browsers, and a registry
// that is a map, for the length of a test.
func asWindows(t *testing.T) map[string]string {
	t.Helper()
	reg := map[string]string{}
	oldOS, oldSet, oldGet, oldDel, oldFound := chromeOS, regSet, regGet, regDel, chromeAppFound
	chromeOS = "windows"
	// No browser's application on the machine: one the test machine has
	// would count as here.
	chromeAppFound = func(string, chromeBrowser) bool { return false }
	regSet = func(key, manifest string) error { reg[key] = manifest; return nil }
	regGet = func(key string) string { return reg[key] }
	regDel = func(key string) error { delete(reg, key); return nil }
	t.Cleanup(func() { chromeOS, regSet, regGet, regDel, chromeAppFound = oldOS, oldSet, oldGet, oldDel, oldFound })
	return reg
}

// On Windows a browser finds reminal's host through the registry: setting
// up writes one manifest under ~/.reminal and points each browser that is
// here at it (and none that is not); a browser not pointed at it does not
// count as set up; removing takes the pointers and the manifest away.
func TestTheBrowserIsSetUpOnWindowsThroughTheRegistry(t *testing.T) {
	reg := asWindows(t)
	home := t.TempDir()
	table := chromeBrowserTable()
	chrome, chromium, edge, brave := table[0], table[2], table[3], table[4]
	for _, b := range []chromeBrowser{chrome, edge} {
		if err := os.MkdirAll(filepath.Join(home, b.Dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if got := chromeOurs(home); len(got) != 0 {
		t.Fatalf("control: set up before any setup: %v", got)
	}
	here := chromeBrowsers(home)
	if len(here) != 2 || here[0].Name != chrome.Name || here[1].Name != edge.Name {
		t.Fatalf("the browsers here: %v", here)
	}
	exe := filepath.Join(home, "reminal.exe")
	if n := applyChrome(home, exe, here, false); n != 0 {
		t.Fatalf("%d failures setting up", n)
	}
	manifest := filepath.Join(home, ".reminal", "browser-host", browserHostName+".json")
	raw, err := os.ReadFile(manifest)
	if err != nil || string(raw) != string(chromeHostManifest(exe)) {
		t.Fatalf("the manifest: %v\n%s", err, raw)
	}
	if reg[chrome.Reg] != manifest || reg[edge.Reg] != manifest {
		t.Fatalf("each browser here is pointed at the manifest: %v", reg)
	}
	if brave.Reg != chrome.Reg || table[1].Reg != chrome.Reg {
		t.Fatalf("Brave and Chrome Beta read Chrome's key: %q %q", brave.Reg, table[1].Reg)
	}
	if _, ok := reg[chromium.Reg]; ok || len(reg) != 2 {
		t.Fatalf("a browser that is not here was pointed at it: %v", reg)
	}
	if got := chromeOurs(home); len(got) != 2 {
		t.Fatalf("set up for both: %v", got)
	}
	// A browser pointed elsewhere (another program's host of the same name,
	// or nothing) is not one of ours.
	reg[edge.Reg] = `C:\elsewhere\host.json`
	if got := chromeOurs(home); len(got) != 1 || got[0].Name != chrome.Name {
		t.Fatalf("a browser pointed elsewhere counted as set up: %v", got)
	}
	reg[edge.Reg] = manifest
	if n := applyChrome(home, exe, chromeOurs(home), true); n != 0 {
		t.Fatalf("%d failures removing", n)
	}
	if len(reg) != 0 {
		t.Fatalf("pointers left after removing: %v", reg)
	}
	if _, err := os.Stat(manifest); !os.IsNotExist(err) {
		t.Fatalf("the manifest is left after removing: %v", err)
	}
	if got := chromeOurs(home); len(got) != 0 {
		t.Fatalf("set up after removing: %v", got)
	}
}

// On Windows the files an agent may never give a page include the stores
// Windows and its browsers keep sign-ins in, and a file named through one of
// its streams.
func TestSecretPathsOnWindows(t *testing.T) {
	plain := `c:/users/sam/documents/notes.txt`
	old := chromeOS
	chromeOS = "linux"
	control := browserSecretPath(plain + "::$data")
	chromeOS = old
	if control {
		t.Fatal("control: a colon in a name is refused where it does not name a stream")
	}
	asWindows(t)
	if browserSecretPath(plain) {
		t.Fatalf("control: %s refused", plain)
	}
	for _, p := range []string{
		`c:/users/sam/documents/notes.txt::$data`,
		`c:/users/sam/documents/notes.txt:hidden`,
		`c:/users/sam/appdata/local/google/chrome/user data/default/login data`,
		`c:/users/sam/appdata/local/microsoft/edge/user data/default/network/cookies`,
		`c:/users/sam/appdata/local/bravesoftware/brave-browser/user data/local state`,
		`c:/users/sam/appdata/roaming/microsoft/credentials/abc`,
		`c:/users/sam/appdata/roaming/microsoft/protect/s-1-5-21/key`,
		`c:/users/sam/.ssh/config`,
		`c:/users/sam/documents/server.pem`,
	} {
		if !browserSecretPath(p) {
			t.Errorf("%s may be given to a page", p)
		}
	}
}
