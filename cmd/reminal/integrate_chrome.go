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
//   - writes the extension's files to ~/reminal-extension, to load with
//     "Load unpacked" (it is not in the Chrome Web Store yet). A folder the
//     file picker shows: it was ~/.reminal/chrome-extension, which is kept
//     up to date where a browser already loads the extension from it.
// The browser_* tools are in reminal's MCP server, which integrate registers
// with each agent anyway. --remove takes these away with the rest; --check
// reads them back. On Windows the manifest is one file under ~/.reminal and
// each browser is pointed at it in the person's own registry hive.

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	chromeext "reminal/extensions/chrome"
)

// browserExtensionID is the id the "key" in extensions/chrome/manifest.json
// gives reminal's extension, wherever it is loaded from.
const browserExtensionID = "igahbbfpoepnabdbgndnheeieimlhpjh"

// browserHostName is the native messaging host's name, as the extension
// connects to it.
const browserHostName = "app.reminal.browser"

// chromeBrowser is a browser of Chrome's family: its name, and its user data
// folder (relative to $HOME), whose NativeMessagingHosts it reads. On
// Windows it reads them from the registry instead, under Reg (in HKCU).
type chromeBrowser struct {
	Name string
	Dir  string
	Reg  string
}

// chromeOS and the registry calls, as vars so a test can stand in for
// Windows.
var (
	chromeOS = runtime.GOOS
	regSet   = chromeRegSet
	regGet   = chromeRegGet
	regDel   = chromeRegDel
)

func chromeBrowserTable() []chromeBrowser {
	if chromeOS == "windows" {
		// Chrome Beta and Brave read Chrome's own key; Edge and Chromium
		// each have theirs.
		al := filepath.Join("AppData", "Local")
		return []chromeBrowser{
			{"Google Chrome", filepath.Join(al, "Google", "Chrome", "User Data"), `Software\Google\Chrome`},
			{"Google Chrome Beta", filepath.Join(al, "Google", "Chrome Beta", "User Data"), `Software\Google\Chrome`},
			{"Chromium", filepath.Join(al, "Chromium", "User Data"), `Software\Chromium`},
			{"Microsoft Edge", filepath.Join(al, "Microsoft", "Edge", "User Data"), `Software\Microsoft\Edge`},
			{"Brave", filepath.Join(al, "BraveSoftware", "Brave-Browser", "User Data"), `Software\Google\Chrome`},
		}
	}
	if chromeOS == "darwin" {
		as := filepath.Join("Library", "Application Support")
		return []chromeBrowser{
			{"Google Chrome", filepath.Join(as, "Google", "Chrome"), ""},
			{"Google Chrome Beta", filepath.Join(as, "Google", "Chrome Beta"), ""},
			{"Chromium", filepath.Join(as, "Chromium"), ""},
			{"Microsoft Edge", filepath.Join(as, "Microsoft Edge"), ""},
			{"Brave", filepath.Join(as, "BraveSoftware", "Brave-Browser"), ""},
			{"Arc", filepath.Join(as, "Arc", "User Data"), ""},
		}
	}
	return []chromeBrowser{
		{"Google Chrome", filepath.Join(".config", "google-chrome"), ""},
		{"Google Chrome Beta", filepath.Join(".config", "google-chrome-beta"), ""},
		{"Chromium", filepath.Join(".config", "chromium"), ""},
		{"Microsoft Edge", filepath.Join(".config", "microsoft-edge"), ""},
		{"Brave", filepath.Join(".config", "BraveSoftware", "Brave-Browser"), ""},
	}
}

// chromeBrowsers are the ones installed here: their user data folder is.
func chromeBrowsers(home string) []chromeBrowser {
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
		if b.Reg != "" {
			// Several browsers read one key: only those that are here.
			if st, err := os.Stat(filepath.Join(home, b.Dir)); err != nil || !st.IsDir() {
				continue
			}
		}
		raw, err := os.ReadFile(chromeHostManifestPath(home, b))
		if err != nil {
			continue
		}
		var m struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(raw, &m) == nil && m.Name == browserHostName && chromePointed(home, b) {
			out = append(out, b)
		}
	}
	return out
}

func chromeHostManifestPath(home string, b chromeBrowser) string {
	if b.Reg != "" {
		// One file for every browser here: each is pointed at it.
		return filepath.Join(home, ".reminal", "browser-host", browserHostName+".json")
	}
	return filepath.Join(home, b.Dir, "NativeMessagingHosts", browserHostName+".json")
}

// chromePointed: the browser is pointed at reminal's manifest (Windows; the
// others find it in their own folder, so there it is always so).
func chromePointed(home string, b chromeBrowser) bool {
	return b.Reg == "" || strings.EqualFold(regGet(b.Reg), chromeHostManifestPath(home, b))
}

// chromeHostManifest is the host manifest reminal writes: this reminal, for
// reminal's extension only.
func chromeHostManifest(exe string) []byte {
	m := map[string]any{
		"name":            browserHostName,
		"description":     "reminal: agents in your reminal sessions use this browser",
		"path":            exe,
		"type":            "stdio",
		"allowed_origins": []string{"chrome-extension://" + browserExtensionID + "/"},
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	return append(b, '\n')
}

// chromeExtensionDir is where the extension's files are written, to load
// with "Load unpacked": a folder in the home folder that a file picker
// shows (one whose name starts with a dot is hidden there).
func chromeExtensionDir(home string) string {
	return filepath.Join(home, "reminal-extension")
}

// chromeExtensionOldDir is where they were written until 3.22.2. A browser
// that loaded the extension from there goes on reading that folder, so it is
// kept up to date for as long as it is there; no setup makes it anew.
func chromeExtensionOldDir(home string) string {
	return filepath.Join(home, ".reminal", "chrome-extension")
}

// chromeExtensionOurs reports whether dir is reminal's to write over or take
// away: not there, empty, or holding reminal's extension (its manifest
// carries reminal's key). A folder of the person's own that happens to have
// the name is never touched.
func chromeExtensionOurs(dir string) bool {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return true
	}
	if err != nil {
		return false
	}
	if len(entries) == 0 {
		return true
	}
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return false
	}
	var theirs, ours struct {
		Key string `json:"key"`
	}
	mine, err := chromeext.File("manifest.json")
	if err != nil || json.Unmarshal(mine, &ours) != nil || ours.Key == "" {
		return false
	}
	return json.Unmarshal(raw, &theirs) == nil && theirs.Key == ours.Key
}

// chromeLoadLine is said at the end of a setup, until the extension is in
// the Chrome Web Store.
func chromeLoadLine(home string) string {
	return fmt.Sprintf("Your browser: load reminal's extension once (chrome://extensions, Developer mode, Load unpacked: %s). After that, agents in your reminal sessions on this computer can use this browser without asking; switch it off in the extension's button.", tildePath(home, chromeExtensionDir(home)))
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
		// Where a browser may still load it from the folder of before.
		if _, err := os.Stat(chromeExtensionOldDir(home)); err == nil {
			_ = writeChromeExtension(chromeExtensionOldDir(home))
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
			if b.Reg != "" {
				if rerr := regDel(b.Reg); err == nil {
					err = rerr
				}
			}
		} else if err = os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
			if err = writeAgentConfig(path, chromeHostManifest(exe)); err == nil && b.Reg != "" {
				err = regSet(b.Reg, path)
			}
		}
		if err != nil {
			failures++
			fmt.Printf("  ✗ %-18s %v\n", b.Name, err)
			continue
		}
		fmt.Printf("  ✓ %-18s %s\n", b.Name, cDim("browser"))
	}
	if remove {
		for _, dir := range []string{chromeExtensionDir(home), chromeExtensionOldDir(home)} {
			if !chromeExtensionOurs(dir) {
				continue
			}
			if err := os.RemoveAll(dir); err != nil {
				failures++
				fmt.Printf("  ✗ %-18s %v\n", "the extension", err)
			}
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
			if string(raw) == string(chromeHostManifest(exe)) && chromePointed(home, b) {
				mark, what = "✓", "set up (browser)"
			}
		}
		fmt.Printf("  %s %-18s %s\n", mark, b.Name, what)
	}
}

// writeChromeExtension writes the extension's files (embedded in this
// binary) to dir, replacing what an earlier reminal wrote there.
func writeChromeExtension(dir string) error {
	if !chromeExtensionOurs(dir) {
		return fmt.Errorf("%s is there and is not reminal's extension: move it away and run this again", dir)
	}
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
		b, err := chromeext.File(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	})
}

// refreshChromeExtension brings the extension's files in dir up to this
// binary's, when a setup wrote them there before and they are another
// version's (reminal was upgraded since). The browser's host does it as the
// extension connects, so the extension, told the version, reloads itself
// from files that are there. A folder no setup wrote is left alone.
func refreshChromeExtension(dir string) error {
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil
	}
	var m struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(raw, &m) == nil && m.Version == chromeext.Version() {
		return nil
	}
	return writeChromeExtension(dir)
}
