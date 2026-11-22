// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

// Which browsers integrate shows as rows: those whose application is on this
// machine. A browser's user data folder outlives the browser (and some are
// made by other programs), so a row for each folder ticked browsers the
// person does not have. A browser with a folder and no application found is
// set up all the same (chromeHere), so one this look misses still works; it
// is named in one line.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// chromeApp is where a browser's application is looked for, by the name its
// row has in chromeBrowserTable.
type chromeApp struct {
	mac     string   // its .app, in /Applications or ~/Applications
	bins    []string // Linux: its binary on PATH
	desktop []string // Linux: its .desktop file, without the extension
	winDir  string   // Windows: its install folder, under Program Files or LocalAppData
	winExe  string   // Windows: its program, in winDir and as its App Paths key
}

var chromeApps = map[string]chromeApp{
	"Google Chrome": {
		mac:     "Google Chrome.app",
		bins:    []string{"google-chrome", "google-chrome-stable"},
		desktop: []string{"google-chrome", "com.google.Chrome"},
		winDir:  `Google\Chrome\Application`, winExe: "chrome.exe",
	},
	"Google Chrome Beta": {
		mac:     "Google Chrome Beta.app",
		bins:    []string{"google-chrome-beta"},
		desktop: []string{"google-chrome-beta"},
		winDir:  `Google\Chrome Beta\Application`, winExe: "chrome.exe",
	},
	"Chromium": {
		mac:     "Chromium.app",
		bins:    []string{"chromium", "chromium-browser"},
		desktop: []string{"chromium", "chromium-browser", "chromium_chromium", "org.chromium.Chromium"},
		winDir:  `Chromium\Application`, winExe: "chrome.exe",
	},
	"Microsoft Edge": {
		mac:     "Microsoft Edge.app",
		bins:    []string{"microsoft-edge", "microsoft-edge-stable"},
		desktop: []string{"microsoft-edge", "com.microsoft.Edge"},
		winDir:  `Microsoft\Edge\Application`, winExe: "msedge.exe",
	},
	"Brave": {
		mac:     "Brave Browser.app",
		bins:    []string{"brave-browser", "brave"},
		desktop: []string{"brave-browser", "brave_brave", "com.brave.Browser"},
		winDir:  `BraveSoftware\Brave-Browser\Application`, winExe: "brave.exe",
	},
	"Arc": {
		mac: "Arc.app",
	},
}

// chromeAppFound reports whether b's application is on this machine. A var
// so a test can say which are.
var chromeAppFound = chromeAppHere

// regAppPath is the program Windows starts for a name (its App Paths key),
// as a var for the same reason as the other registry calls.
var regAppPath = chromeRegAppPath

// chromeAppDirs are the folders applications are listed in: where a .app is
// on macOS, and a .desktop file on Linux. A var so a test looks only in
// folders of its own.
var chromeAppDirs = func(home string) []string {
	if chromeOS == "darwin" {
		return []string{"/Applications", filepath.Join(home, "Applications")}
	}
	return []string{
		filepath.Join(home, ".local", "share", "applications"),
		"/usr/share/applications",
		"/usr/local/share/applications",
		"/var/lib/snapd/desktop/applications",
		"/var/lib/flatpak/exports/share/applications",
		filepath.Join(home, ".local", "share", "flatpak", "exports", "share", "applications"),
	}
}

func chromeAppHere(home string, b chromeBrowser) bool {
	app, ok := chromeApps[b.Name]
	if !ok {
		return false
	}
	isFile := func(p string) bool {
		st, err := os.Stat(p)
		return err == nil && !st.IsDir()
	}
	switch chromeOS {
	case "darwin":
		for _, dir := range chromeAppDirs(home) {
			if st, err := os.Stat(filepath.Join(dir, app.mac)); app.mac != "" && err == nil && st.IsDir() {
				return true
			}
		}
	case "windows":
		if app.winDir == "" {
			return false
		}
		for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "LocalAppData"} {
			if root := os.Getenv(env); root != "" && isFile(filepath.Join(root, app.winDir, app.winExe)) {
				return true
			}
		}
		// Installed somewhere else: where Windows itself says the program
		// is. Several browsers are a chrome.exe, so the path has to be this
		// one's.
		p := strings.ToLower(strings.Trim(regAppPath(app.winExe), `"`))
		return p != "" && strings.Contains(p, strings.ToLower(`\`+app.winDir+`\`)) && isFile(p)
	default:
		for _, bin := range app.bins {
			if _, err := exec.LookPath(bin); err == nil {
				return true
			}
		}
		// Not on PATH (a snap or a flatpak, a launcher only): its entry in
		// the applications menu.
		for _, dir := range chromeAppDirs(home) {
			for _, name := range app.desktop {
				if isFile(filepath.Join(dir, name+".desktop")) {
					return true
				}
			}
		}
	}
	return false
}

// chromeFound splits the table for what integrate prints: rows, the
// browsers in setUp whose application is here; noApp, those in setUp whose
// application was not found (set up from their user data folder alone);
// and absent, the rest of the table: not installed.
func chromeFound(home string, setUp []chromeBrowser) (rows, noApp []chromeBrowser, absent []string) {
	in := map[string]bool{}
	for _, b := range setUp {
		in[b.Name] = true
		if chromeAppFound(home, b) {
			rows = append(rows, b)
		} else {
			noApp = append(noApp, b)
		}
	}
	for _, b := range chromeBrowserTable() {
		if !in[b.Name] {
			absent = append(absent, b.Name)
		}
	}
	return rows, noApp, absent
}

func chromeNames(bs []chromeBrowser) []string {
	out := make([]string, 0, len(bs))
	for _, b := range bs {
		out = append(out, b.Name)
	}
	return out
}
