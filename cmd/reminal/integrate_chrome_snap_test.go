// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"reminal/internal/client"
)

// snapRig is a Linux machine for chromeSnapOnly: its PATH is the map given
// (name to the program's path), its applications menu the folder returned.
func snapRig(t *testing.T, onPath map[string]string) (apps string) {
	t.Helper()
	oldOS, oldLook, oldDirs := chromeOS, chromeLookPath, chromeAppDirs
	t.Cleanup(func() { chromeOS, chromeLookPath, chromeAppDirs = oldOS, oldLook, oldDirs })
	chromeOS = "linux"
	chromeLookPath = func(name string) (string, error) {
		if p, ok := onPath[name]; ok {
			return p, nil
		}
		return "", errors.New("not found")
	}
	apps = t.TempDir()
	chromeAppDirs = func(string) []string { return []string{apps} }
	return apps
}

func chromiumRow(t *testing.T) chromeBrowser {
	t.Helper()
	for _, b := range chromeBrowserTable() {
		if b.Name == "Chromium" {
			return b
		}
	}
	t.Fatal("no Chromium in the table")
	return chromeBrowser{}
}

// A snap's program is a link to snap itself, or the script a distribution
// leaves in a package's old place to start the snap. Anything else is the
// browser's own program.
func TestASnapsProgramIsToldFromABrowsersOwn(t *testing.T) {
	if os.PathSeparator != '/' {
		t.Skip("snaps are on Linux")
	}
	dir := t.TempDir()
	snap := filepath.Join(dir, "usr-bin", "snap")
	if err := os.MkdirAll(filepath.Dir(snap), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(p, body string) string {
		t.Helper()
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write(snap, "\x7fELF snapd")
	link := filepath.Join(dir, "chromium")
	if err := os.Symlink(snap, link); err != nil {
		t.Fatal(err)
	}
	wrapper := write(filepath.Join(dir, "chromium-browser"), "#!/bin/sh\n# the deb is a snap now\nexec /snap/bin/chromium \"$@\"\n")
	own := write(filepath.Join(dir, "chromium-own"), "\x7fELF the browser, which mentions /snap/bin/ in passing")
	script := write(filepath.Join(dir, "chromium-script"), "#!/bin/sh\nexec /usr/lib/chromium/chromium \"$@\"\n")
	for _, c := range []struct {
		p    string
		want bool
	}{
		{"/snap/bin/chromium", true},
		{link, true},
		{wrapper, true},
		{own, false},
		{script, false},
		{filepath.Join(dir, "missing"), false},
	} {
		if got := snapProgram(c.p); got != c.want {
			t.Errorf("snapProgram(%s) = %v, want %v", filepath.Base(c.p), got, c.want)
		}
	}
}

// Chromium that is here only as a snap, in a folder that cannot take
// reminal's setup, is said to be one reminal cannot work with; with a
// Chromium of another kind beside it, or on another system, nothing is
// said; nor where the snap's folder can be set up.
func TestChromiumOnlyAsASnapIsSaidSo(t *testing.T) {
	if os.PathSeparator != '/' {
		t.Skip("snaps are on Linux")
	}
	oldCan := chromeSnapCanSetUp
	t.Cleanup(func() { chromeSnapCanSetUp = oldCan })
	chromeSnapCanSetUp = func(string) bool { return true }
	snapRig(t, map[string]string{"chromium": "/snap/bin/chromium"})
	if got := chromeSnapOnly("/home/someone", chromiumRow(t)); got != "" {
		t.Errorf("a snap whose folder can be set up was said not to work: %q", got)
	}
	chromeSnapCanSetUp = func(string) bool { return false }
	b := chromiumRow(t)
	own := filepath.Join(t.TempDir(), "chromium")
	if err := os.WriteFile(own, []byte("\x7fELF"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name    string
		onPath  map[string]string
		desktop []string
		snap    bool
	}{
		{"the snap's program on PATH", map[string]string{"chromium": "/snap/bin/chromium"}, nil, true},
		{"the snap and its old name", map[string]string{"chromium": "/snap/bin/chromium", "chromium-browser": "/snap/bin/chromium"}, nil, true},
		{"its own program", map[string]string{"chromium": own}, nil, false},
		{"both kinds", map[string]string{"chromium": own, "chromium-browser": "/snap/bin/chromium"}, []string{"chromium_chromium"}, false},
		{"not on PATH, the snap's menu entry", nil, []string{"chromium_chromium"}, true},
		{"not on PATH, both menu entries", nil, []string{"chromium_chromium", "chromium"}, false},
		{"not on PATH, its own menu entry", nil, []string{"chromium-browser"}, false},
		{"not here at all", nil, nil, false},
	} {
		apps := snapRig(t, c.onPath)
		for _, d := range c.desktop {
			if err := os.WriteFile(filepath.Join(apps, d+".desktop"), []byte("[Desktop Entry]\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if got := chromeSnapOnly("/home/someone", b); (got != "") != c.snap {
			t.Errorf("%s: said %q, want a snap said: %v", c.name, got, c.snap)
		}
	}
	// Only Chromium: it is the one seen not to work, and the line names the
	// others as working.
	snapRig(t, map[string]string{"brave-browser": "/snap/bin/brave"})
	for _, o := range chromeBrowserTable() {
		if o.Name == "Brave" && chromeSnapOnly("/home/someone", o) != "" {
			t.Error("Brave was said not to work")
		}
	}
	snapRig(t, map[string]string{"chromium": "/snap/bin/chromium"})
	chromeOS = "darwin"
	if got := chromeSnapOnly("/Users/someone", b); got != "" {
		t.Errorf("on macOS: %q", got)
	}
}

// Integrate never ticks such a browser "ready": the plan, the result and
// --check each say why, and the steps send the person to a browser that
// works.
func TestIntegrateSaysABrowserItCannotWorkWith(t *testing.T) {
	home := integrateRig(t, 200)
	old := chromeCannotStart
	t.Cleanup(func() { chromeCannotStart = old })
	chromeCannotStart = func(_ string, b chromeBrowser) string {
		if b.Name == "Chromium" {
			return chromeSnapWhy
		}
		return ""
	}
	if err := os.MkdirAll(filepath.Join(home, chromiumRow(t).Dir), 0o755); err != nil {
		t.Fatal(err)
	}
	out := runIntegrateRig(t, "--yes")
	if n := strings.Count(out, "installed as a snap"); n != 2 {
		t.Errorf("the plan and the result should each say it, said %d time(s):\n%s", n, out)
	}
	if regexp.MustCompile(`✓\s+Chromium`).MatchString(out) || regexp.MustCompile(`Chromium\s+ready`).MatchString(out) {
		t.Errorf("Chromium was ticked or called ready:\n%s", out)
	}
	if !regexp.MustCompile(`!\s+Chromium\s+installed as a snap`).MatchString(out) {
		t.Errorf("no row saying why for Chromium:\n%s", out)
	}
	if !regexp.MustCompile(`✓\s+Google Chrome\s+ready`).MatchString(out) {
		t.Errorf("Chrome, which works, lost its tick:\n%s", out)
	}
	check := runIntegrateRig(t, "--check")
	if !regexp.MustCompile(`!\s+Chromium\s+installed as a snap`).MatchString(check) {
		t.Errorf("--check does not say it:\n%s", check)
	}
	// With another browser here, the steps send the person to that one.
	table := map[string]chromeBrowser{}
	for _, b := range chromeBrowserTable() {
		table[b.Name] = b
	}
	if got := chromeExtensionsPage(home, []chromeBrowser{table["Chromium"], table["Microsoft Edge"]}); got != "edge://extensions" {
		t.Errorf("the steps open %s, want the browser that works", got)
	}
}

// With no browser here that can work, nothing is left to do in a browser:
// the steps for loading the extension are not said (the rows say why). With
// a browser that works beside it, they are.
func TestNoStepsWhenNoBrowserHereCanWork(t *testing.T) {
	integrateRig(t, 200)
	old := chromeCannotStart
	t.Cleanup(func() { chromeCannotStart = old })
	chromeCannotStart = func(_ string, b chromeBrowser) string {
		if b.Name == "Chromium" {
			return chromeSnapWhy
		}
		return ""
	}
	both := runIntegrateRig(t, "--yes")
	if !strings.Contains(both, "Next, in your browser") {
		t.Fatalf("with a browser that works, the steps should be said:\n%s", both)
	}
	chromeAppFound = func(_ string, b chromeBrowser) bool { return b.Name == "Chromium" }
	out := runIntegrateRig(t, "--yes")
	if !strings.Contains(out, "installed as a snap") {
		t.Fatalf("the row saying why is missing:\n%s", out)
	}
	for _, said := range []string{"Next, in your browser", "Load unpacked", "can then use the browser"} {
		if strings.Contains(out, said) {
			t.Errorf("no browser here can work, yet %q is said:\n%s", said, out)
		}
	}
}

// Chromium that is here as a snap is set up in the snap's own folder too:
// reminal's copy, and a host manifest naming the copy. Its row says ready,
// --check ticks it while the copy is this reminal, and a remove takes it
// all away.
func TestIntegrateSetsUpChromiumAsASnap(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("snaps are on Linux")
	}
	home := integrateRig(t, 200)
	chromeOS = "linux"
	oldHere, oldExe := chromeSnapHere, integrateExe
	t.Cleanup(func() { chromeSnapHere, integrateExe = oldHere, oldExe })
	chromeSnapHere = func(_ string, b chromeBrowser) bool { return b.Name == "Chromium" }
	exe := filepath.Join(home, "bin", "reminal")
	if err := os.WriteFile(exe, []byte("reminal one"), 0o755); err != nil {
		t.Fatal(err)
	}
	integrateExe = func() (string, error) { return exe, nil }
	if err := os.MkdirAll(filepath.Join(home, "snap", "chromium", "common"), 0o775); err != nil {
		t.Fatal(err)
	}
	out := runIntegrateRig(t, "--yes")
	if !regexp.MustCompile(`✓\s+Chromium\s+ready`).MatchString(out) || strings.Contains(out, "installed as a snap") {
		t.Fatalf("Chromium as a snap should be ready:\n%s", out)
	}
	if got, err := os.ReadFile(client.SnapBrowserHelper(home)); err != nil || string(got) != "reminal one" {
		t.Fatalf("reminal's copy in the snap's folder: %q, %v", got, err)
	}
	raw, err := os.ReadFile(client.SnapBrowserManifestPath(home, browserHostName+".json"))
	if err != nil || !strings.Contains(string(raw), client.SnapBrowserHelper(home)) {
		t.Fatalf("the snap's host manifest should name the copy: %s, %v", raw, err)
	}
	check := runIntegrateRig(t, "--check")
	if !regexp.MustCompile(`✓\s+Chromium\s+set up`).MatchString(check) {
		t.Errorf("--check after a setup:\n%s", check)
	}
	// reminal was upgraded since: the copy is another reminal's.
	if err := os.WriteFile(exe, []byte("reminal two!"), 0o755); err != nil {
		t.Fatal(err)
	}
	check = runIntegrateRig(t, "--check")
	if !regexp.MustCompile(`!\s+Chromium\s+set up for a different reminal`).MatchString(check) {
		t.Errorf("--check with a copy of another reminal:\n%s", check)
	}
	runIntegrateRig(t, "--yes")
	if got, _ := os.ReadFile(client.SnapBrowserHelper(home)); string(got) != "reminal two!" {
		t.Errorf("a second setup did not bring the copy up to date: %q", got)
	}
	runIntegrateRig(t, "--remove", "--yes")
	if _, err := os.Lstat(client.SnapBrowserDir(home)); !os.IsNotExist(err) {
		t.Errorf("after a remove the snap's folder of reminal's is still there: %v", err)
	}
	if _, err := os.Lstat(client.SnapBrowserManifestPath(home, browserHostName+".json")); !os.IsNotExist(err) {
		t.Errorf("after a remove the snap's host manifest is still there: %v", err)
	}
}
