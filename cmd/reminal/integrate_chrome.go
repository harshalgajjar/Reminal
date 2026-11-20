// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

// `reminal integrate chrome`: lets agents in this machine's reminal sessions
// use the person's own browser, through reminal's extension, asked in the
// browser for each session. Its own opt-in, never part of a plain
// `reminal integrate`. It:
//   - writes the extension's files to ~/.reminal/chrome-extension, to load
//     there with "Load unpacked" (it is not in the Chrome Web Store yet);
//   - registers `reminal` as the extension's native messaging host
//     (NativeMessagingHosts/app.reminal.browser.json), for each browser of
//     Chrome's family whose user data folder is here: Chrome, Chromium,
//     Edge, Brave (and Arc on macOS).
// --remove takes both away; --check says what is there.

import (
	"bufio"
	"encoding/json"
	"errors"
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
func chromeBrowsers(home string) []chromeBrowser {
	var out []chromeBrowser
	for _, b := range chromeBrowserTable() {
		if st, err := os.Stat(filepath.Join(home, b.Dir)); err == nil && st.IsDir() {
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

func integrateChrome(home, exe string, remove, check, assumeYes, dryRun bool) error {
	if runtime.GOOS == "windows" {
		return errors.New("using your browser is not available on Windows yet")
	}
	browsers := chromeBrowsers(home)
	extDir := chromeExtensionDir(home)
	if check {
		if len(browsers) == 0 {
			fmt.Println("  · no Chrome, Chromium, Edge, Brave or Arc found")
		}
		for _, b := range browsers {
			what := "not set up"
			if raw, err := os.ReadFile(chromeHostManifestPath(home, b)); err == nil {
				what = "set up for a different reminal (re-run `reminal integrate chrome`)"
				if string(raw) == string(chromeHostManifest(exe)) {
					what = "set up"
				}
			}
			fmt.Printf("  %-18s %s\n", b.Name, what)
		}
		return nil
	}
	if len(browsers) == 0 && !remove {
		return errors.New("no Chrome, Chromium, Edge, Brave or Arc found on this computer")
	}
	action := "Let agents in your reminal sessions use"
	if remove {
		action = "Stop letting agents use"
	}
	fmt.Printf("%s your browser (reminal's extension; asked in the browser for each session):\n\n", action)
	for _, b := range browsers {
		fmt.Printf("  %-18s %s\n", b.Name, cDim(tildePath(home, chromeHostManifestPath(home, b))))
	}
	fmt.Printf("  %-18s %s\n", "the extension", cDim(tildePath(home, extDir)))
	if dryRun {
		fmt.Println("\nDry run: nothing changed.")
		return nil
	}
	if !assumeYes {
		fmt.Print("\nProceed? [y/N] ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			fmt.Println("Cancelled.")
			return nil
		}
	}
	fmt.Println()
	if remove {
		return removeChrome(home, browsers, extDir)
	}
	if err := writeChromeExtension(extDir); err != nil {
		return fmt.Errorf("writing the extension: %w", err)
	}
	var failed int
	for _, b := range browsers {
		path := chromeHostManifestPath(home, b)
		err := os.MkdirAll(filepath.Dir(path), 0o755)
		if err == nil {
			err = writeAgentConfig(path, chromeHostManifest(exe))
		}
		if err != nil {
			failed++
			fmt.Printf("  ✗ %-18s %v\n", b.Name, err)
			continue
		}
		fmt.Printf("  ✓ %-18s\n", b.Name)
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d failed", failed, len(browsers))
	}
	fmt.Printf("\nNow load the extension, once: open chrome://extensions, turn on Developer mode,\n"+
		"choose Load unpacked, and pick %s\n"+
		"An agent asks in the browser before it uses it, once per session; you can stop it\n"+
		"from the extension's button.\n", tildePath(home, extDir))
	return nil
}

func removeChrome(home string, browsers []chromeBrowser, extDir string) error {
	for _, b := range chromeBrowserTable() {
		path := chromeHostManifestPath(home, b)
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var m struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(raw, &m) != nil || m.Name != browserHostName {
			continue // not ours
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		fmt.Printf("  ✓ %-18s removed\n", b.Name)
	}
	if err := os.RemoveAll(extDir); err != nil {
		return err
	}
	fmt.Println("\nRemoved. Remove the extension itself from chrome://extensions.")
	return nil
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
