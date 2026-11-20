// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

// The browser, as part of `reminal integrate`: agents in this machine's
// reminal sessions may use the person's own browser through reminal's
// extension, each session only once the person allows it there (the gate,
// in the browser, that makes this safe to set up by default). Integrate:
//   - registers `reminal` as the extension's native messaging host
//     (NativeMessagingHosts/app.reminal.browser.json) for each browser of
//     Chrome's family whose user data folder is here: Chrome, Chromium,
//     Edge, Brave (and Arc on macOS);
//   - writes the extension's files to ~/.reminal/chrome-extension, to load
//     with "Load unpacked" (it is not in the Chrome Web Store yet).
// The browser_* tools are in reminal's MCP server, which integrate registers
// with each agent anyway. --remove takes these away with the rest; --check
// reads them back. Not on Windows yet.

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"

	chromeext "reminal/extensions/chrome"
)

// browserExtensionID is the id the "key" in extensions/chrome/manifest.json
// gives reminal's extension, wherever it is loaded from.
const browserExtensionID = "igahbbfpoepnabdbgndnheeieimlhpjh"

// browserHostName is the native messaging host's name, as the extension
// connects to it.
const browserHostName = "app.reminal.browser"

// chromeBrowser is a browser of Chrome's family: its name, and its user data
// folder (relative to $HOME), whose NativeMessagingHosts it reads.
type chromeBrowser struct {
	Name string
	Dir  string
}

func chromeBrowserTable() []chromeBrowser {
	if runtime.GOOS == "darwin" {
		as := filepath.Join("Library", "Application Support")
		return []chromeBrowser{
			{"Google Chrome", filepath.Join(as, "Google", "Chrome")},
			{"Google Chrome Beta", filepath.Join(as, "Google", "Chrome Beta")},
			{"Chromium", filepath.Join(as, "Chromium")},
			{"Microsoft Edge", filepath.Join(as, "Microsoft Edge")},
			{"Brave", filepath.Join(as, "BraveSoftware", "Brave-Browser")},
			{"Arc", filepath.Join(as, "Arc", "User Data")},
		}
	}
	return []chromeBrowser{
		{"Google Chrome", filepath.Join(".config", "google-chrome")},
		{"Google Chrome Beta", filepath.Join(".config", "google-chrome-beta")},
		{"Chromium", filepath.Join(".config", "chromium")},
		{"Microsoft Edge", filepath.Join(".config", "microsoft-edge")},
		{"Brave", filepath.Join(".config", "BraveSoftware", "Brave-Browser")},
	}
}

// chromeBrowsers are the ones installed here: their user data folder is.
// None on Windows, where the browser is not offered yet.
func chromeBrowsers(home string) []chromeBrowser {
	if runtime.GOOS == "windows" {
		return nil
	}
	var out []chromeBrowser
	for _, b := range chromeBrowserTable() {
		if st, err := os.Stat(filepath.Join(home, b.Dir)); err == nil && st.IsDir() {
			out = append(out, b)
		}
	}
	return out
}

// chromeOurs are the browsers with reminal's host manifest in them.
func chromeOurs(home string) []chromeBrowser {
	var out []chromeBrowser
	for _, b := range chromeBrowserTable() {
		raw, err := os.ReadFile(chromeHostManifestPath(home, b))
		if err != nil {
			continue
		}
		var m struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(raw, &m) == nil && m.Name == browserHostName {
			out = append(out, b)
		}
	}
	return out
}

func chromeHostManifestPath(home string, b chromeBrowser) string {
	return filepath.Join(home, b.Dir, "NativeMessagingHosts", browserHostName+".json")
}

// chromeHostManifest is the host manifest reminal writes: this reminal, for
// reminal's extension only.
func chromeHostManifest(exe string) []byte {
	m := map[string]any{
		"name":            browserHostName,
		"description":     "reminal: agents in your reminal sessions use this browser, when you allow it",
		"path":            exe,
		"type":            "stdio",
		"allowed_origins": []string{"chrome-extension://" + browserExtensionID + "/"},
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	return append(b, '\n')
}

// chromeExtensionDir is where the extension's files are written, to load
// with "Load unpacked".
func chromeExtensionDir(home string) string {
	return filepath.Join(home, ".reminal", "chrome-extension")
}

// chromeLoadLine is said at the end of a setup, until the extension is in
// the Chrome Web Store.
func chromeLoadLine(home string) string {
	return fmt.Sprintf("Your browser: load reminal's extension once (chrome://extensions, Developer mode, Load unpacked: %s). It asks you before an agent uses it.", tildePath(home, chromeExtensionDir(home)))
}

// applyChrome sets the browser up (or, on remove, takes it away): the host
// manifest for each browser in browsers, and the extension's files. One line
// a browser, as the agents get.
func applyChrome(home, exe string, browsers []chromeBrowser, remove bool) (failures int) {
	if !remove {
		if err := writeChromeExtension(chromeExtensionDir(home)); err != nil {
			fmt.Printf("  ✗ %-18s %v\n", "the extension", err)
			return 1
		}
	}
	for _, b := range browsers {
		path := chromeHostManifestPath(home, b)
		var err error
		if remove {
			err = os.Remove(path)
			if os.IsNotExist(err) {
				err = nil
			}
		} else if err = os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
			err = writeAgentConfig(path, chromeHostManifest(exe))
		}
		if err != nil {
			failures++
			fmt.Printf("  ✗ %-18s %v\n", b.Name, err)
			continue
		}
		fmt.Printf("  ✓ %-18s %s\n", b.Name, cDim("browser"))
	}
	if remove {
		if err := os.RemoveAll(chromeExtensionDir(home)); err != nil {
			failures++
			fmt.Printf("  ✗ %-18s %v\n", "the extension", err)
		}
	}
	return failures
}

// printChromeCheck is --check's lines for the browsers here.
func printChromeCheck(home, exe string) {
	for _, b := range chromeBrowsers(home) {
		mark, what := "·", "not set up"
		if raw, err := os.ReadFile(chromeHostManifestPath(home, b)); err == nil {
			mark, what = "!", "set up for a different reminal (re-run integrate)"
			if string(raw) == string(chromeHostManifest(exe)) {
				mark, what = "✓", "set up (browser)"
			}
		}
		fmt.Printf("  %s %-18s %s\n", mark, b.Name, what)
	}
}

// writeChromeExtension writes the extension's files (embedded in this
// binary) to dir, replacing what an earlier reminal wrote there.
func writeChromeExtension(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return fs.WalkDir(chromeext.Files, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		dst := filepath.Join(dir, filepath.FromSlash(p))
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := fs.ReadFile(chromeext.Files, p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	})
}
