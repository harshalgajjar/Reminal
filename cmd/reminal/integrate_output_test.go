// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"
)

// Words are wrapped whole, under a hanging indent; a word longer than the
// room there is stands alone, the one line that is too wide; and what is
// marked to stay together (a path with a space in it) is not broken.
func TestWrapWords(t *testing.T) {
	for _, c := range []struct {
		text        string
		width       int
		first, rest string
		want        []string
	}{
		{"one two three", 80, "", "", []string{"one two three"}},
		{"one two three", 7, "", "", []string{"one two", "three"}},
		{"one two three", 9, "  ", "    ", []string{"  one two", "    three"}},
		{"see a/very/long/path here", 10, "", "  ", []string{"see", "  a/very/long/path", "  here"}},
		{"in " + noBreak("Application Support") + " now", 12, "", "", []string{"in", "Application Support", "now"}},
		{"", 10, "  ", "  ", []string{""}},
		{"a  b\nc", 80, "> ", "", []string{"> a b c"}},
	} {
		got := wrapWords(c.text, c.width, c.first, c.rest)
		if strings.Join(got, "\n") != strings.Join(c.want, "\n") {
			t.Errorf("wrapWords(%q, %d, %q, %q) = %q, want %q", c.text, c.width, c.first, c.rest, got, c.want)
		}
	}
}

// The width is the terminal's; with none to ask (a test's output is a pipe,
// as a piped run's and the daemon's are) it is COLUMNS, else 80, and never
// under 40.
func TestIntegrateWidthWithNoTerminal(t *testing.T) {
	for _, c := range []struct {
		columns string
		want    int
	}{{"", 80}, {"120", 120}, {"52", 52}, {"20", 40}, {"wide", 80}, {"-3", 80}} {
		t.Setenv("COLUMNS", c.columns)
		if got := integrateWidth(); got != c.want {
			t.Errorf("COLUMNS=%q: %d columns, want %d", c.columns, got, c.want)
		}
	}
}

// integrateRig is a home folder (a long one) with three agents on PATH, and
// browsers as a Mac has them: Chrome and Edge used and installed; Brave's
// folder left behind by a Brave that is gone; Chromium installed and never
// opened (no folder of its yet); Chrome Beta and Arc not there at all.
// Output is width columns wide.
func integrateRig(t *testing.T, width int) (home string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("on Windows the home folder cannot be stood in for by HOME")
	}
	home = filepath.Join(t.TempDir(), "a-rather-long-user-name-for-testing")
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, agent := range []string{"claude", "codex", "gemini"} {
		if err := os.WriteFile(filepath.Join(bin, agent), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", bin)
	t.Setenv("SHELL", "") // no login shell's PATH: only the agents above

	oldOS, oldFound, oldExe, oldWidth := chromeOS, chromeAppFound, integrateExe, integrateWidth
	t.Cleanup(func() { chromeOS, chromeAppFound, integrateExe, integrateWidth = oldOS, oldFound, oldExe, oldWidth })
	chromeOS = "darwin"
	installed := map[string]bool{"Google Chrome": true, "Microsoft Edge": true, "Chromium": true}
	chromeAppFound = func(_ string, b chromeBrowser) bool { return installed[b.Name] }
	integrateExe = func() (string, error) { return "/usr/local/bin/reminal", nil }
	integrateWidth = func() int { return width }

	for _, b := range chromeBrowserTable() {
		if b.Name == "Google Chrome" || b.Name == "Microsoft Edge" || b.Name == "Brave" {
			if err := os.MkdirAll(filepath.Join(home, b.Dir), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	return home
}

// runIntegrateRig runs integrate in the rig, keeping its PATH (runIntegrateIn
// empties it).
func runIntegrateRig(t *testing.T, args ...string) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	was := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		var b strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			b.Write(buf[:n])
			if err != nil {
				break
			}
		}
		done <- b.String()
	}()
	runErr := runIntegrate(args)
	w.Close()
	os.Stdout = was
	out := <-done
	if runErr != nil {
		t.Fatalf("integrate %v: %v\n%s", args, runErr, out)
	}
	return out
}

// rigArgs sets up the rig's three agents, asks for one that is not there,
// and the browser.
var rigArgs = []string{"claude", "codex", "gemini", "amp", "chrome", "-y"}

// fitsWidth fails for a line wider than width, other than one that is a
// single thing that cannot be broken (a path).
func fitsWidth(t *testing.T, out string, width int) {
	t.Helper()
	for _, ln := range strings.Split(out, "\n") {
		if utf8.RuneCountInString(ln) <= width {
			continue
		}
		token := strings.NewReplacer("Application Support/", "", "Microsoft Edge/", "").Replace(strings.TrimSpace(ln))
		if strings.ContainsAny(token, " \t") {
			t.Errorf("wider than %d columns: %q", width, ln)
		}
	}
}

const rigOutput80 = `Register reminal's MCP server (/usr/local/bin/reminal mcp) with:

  Claude Code     via claude mcp add + attention hooks + speak allowed
  Codex CLI       via codex mcp add
  Gemini CLI      ~/.gemini/settings.json + attention hooks + speak allowed

  not installed: Amp

Let agents in your reminal sessions use your browser (switch it off in the
extension's button):

  Google Chrome   ready
  Chromium        ready
  Microsoft Edge  ready
  the extension   ~/reminal-extension

  not installed: Google Chrome Beta, Arc

  ✓ Claude Code     via claude mcp add + attention hooks + speak allowed
  ✓ Codex CLI       via codex mcp add
  ✓ Gemini CLI      ~/.gemini/settings.json + attention hooks + speak allowed
  ✓ Google Chrome   ready
  ✓ Chromium        ready
  ✓ Microsoft Edge  ready
  also set up, app not found: Brave

Restart a running Claude Code to pick up new tools: claude --continue
Restart a running Codex CLI to pick up new tools: codex resume --last
Restart a running Gemini CLI to pick up new tools: gemini --resume latest

Next, in your browser:

  1. Open chrome://extensions
  2. Turn on Developer mode
  3. Load unpacked: ~/reminal-extension

Agents in reminal sessions on this computer can then use the browser. Switch it
off in the extension's button.
`

const rigOutput40 = `Register reminal's MCP server
(/usr/local/bin/reminal mcp) with:

  Claude Code     via claude mcp add +
                  attention hooks +
                  speak allowed
  Codex CLI       via codex mcp add
  Gemini CLI
    ~/.gemini/settings.json + attention
    hooks + speak allowed

  not installed: Amp

Let agents in your reminal sessions use
your browser (switch it off in the
extension's button):

  Google Chrome   ready
  Chromium        ready
  Microsoft Edge  ready
  the extension   ~/reminal-extension

  not installed: Google Chrome Beta, Arc

  ✓ Claude Code     via claude mcp add +
                    attention hooks +
                    speak allowed
  ✓ Codex CLI       via codex mcp add
  ✓ Gemini CLI
      ~/.gemini/settings.json +
      attention hooks + speak allowed
  ✓ Google Chrome   ready
  ✓ Chromium        ready
  ✓ Microsoft Edge  ready
  also set up, app not found: Brave

Restart a running Claude Code to pick up
  new tools: claude --continue
Restart a running Codex CLI to pick up
  new tools: codex resume --last
Restart a running Gemini CLI to pick up
  new tools: gemini --resume latest

Next, in your browser:

  1. Open chrome://extensions
  2. Turn on Developer mode
  3. Load unpacked: ~/reminal-extension

Agents in reminal sessions on this
computer can then use the browser.
Switch it off in the extension's button.
`

// What integrate prints, plan and result, at a phone's width and at 80 and
// 120: every line fits (a path alone on its line is the one thing that may
// not), no word is cut (the words are the same at every width), the host
// manifests' paths are not in it, and only the browsers installed here are
// rows. At 40 and 80 it is this, to the letter.
func TestIntegrateOutputFitsTheTerminal(t *testing.T) {
	var words []string
	for _, c := range []struct {
		width int
		want  string
	}{{1000, ""}, {40, rigOutput40}, {80, rigOutput80}, {120, ""}} {
		home := integrateRig(t, c.width)
		out := runIntegrateRig(t, rigArgs...)
		t.Logf("at %d columns:\n%s", c.width, out)
		fitsWidth(t, out, c.width)
		if c.want != "" && out != c.want {
			t.Errorf("at %d columns it is not what is written here; got:\n%s", c.width, out)
		}
		got := strings.Fields(out)
		if words == nil {
			words = got // at 1000 columns nothing is wrapped
		} else if strings.Join(got, " ") != strings.Join(words, " ") {
			t.Errorf("at %d columns the words differ from the unwrapped ones:\n%s", c.width, out)
		}
		if strings.Contains(out, "NativeMessagingHosts") || strings.Contains(out, "Application Support") {
			t.Errorf("at %d columns it prints where a host manifest is", c.width)
		}

		var ticked []string
		for _, ln := range strings.Split(out, "\n") {
			if rest, ok := strings.CutPrefix(ln, "  ✓ "); ok {
				name, _, _ := strings.Cut(rest, "  ")
				ticked = append(ticked, name)
			}
		}
		if got, want := strings.Join(ticked, ", "), "Claude Code, Codex CLI, Gemini CLI, Google Chrome, Chromium, Microsoft Edge"; got != want {
			t.Errorf("at %d columns the ticked rows are %q, want %q", c.width, got, want)
		}
		browserStepsAtTheEnd(t, out)

		// Set up: every browser whose user data folder is here, its
		// application found or not (Brave), and one whose application is
		// here with no folder yet (Chromium, never opened): its folder for
		// the manifest is made.
		for _, b := range chromeBrowserTable() {
			_, err := os.Stat(chromeHostManifestPath(home, b))
			want := b.Name == "Google Chrome" || b.Name == "Microsoft Edge" || b.Name == "Brave" || b.Name == "Chromium"
			if want != (err == nil) {
				t.Errorf("%s: host manifest there %v, want %v", b.Name, err == nil, want)
			}
		}
	}
}

// --check names each browser and its state; with -v, where its host manifest
// is too, the path kept whole. A browser set up for a different reminal has
// its path said without -v: there the path is the point.
func TestIntegrateCheckVerboseSaysWhereTheManifestsAre(t *testing.T) {
	home := integrateRig(t, 40)
	runIntegrateRig(t, rigArgs...)

	check := runIntegrateRig(t, "chrome", "--check")
	fitsWidth(t, check, 40)
	if strings.Contains(check, "NativeMessagingHosts") {
		t.Errorf("--check prints the paths without -v:\n%s", check)
	}
	for _, name := range []string{"Google Chrome", "Chromium", "Microsoft Edge", "Brave"} {
		if !strings.Contains(check, "✓ "+name) {
			t.Errorf("--check does not read %s back:\n%s", name, check)
		}
	}

	for _, flag := range []string{"-v", "--verbose"} {
		verbose := runIntegrateRig(t, "chrome", "--check", flag)
		fitsWidth(t, verbose, 40)
		for _, b := range chromeBrowsers(home) {
			if path := tildePath(home, chromeHostManifestPath(home, b)); !strings.Contains(verbose, "      "+path+"\n") {
				t.Errorf("--check %s does not say where %s's manifest is (%s), whole on a line:\n%s", flag, b.Name, path, verbose)
			}
		}
	}

	// Chrome's, as another reminal wrote it.
	chrome := chromeBrowserTable()[0]
	if err := os.WriteFile(chromeHostManifestPath(home, chrome), chromeHostManifest("/elsewhere/reminal"), 0o600); err != nil {
		t.Fatal(err)
	}
	other := runIntegrateRig(t, "chrome", "--check")
	if !strings.Contains(other, "! "+chrome.Name) || strings.Count(other, "NativeMessagingHosts") != 1 ||
		!strings.Contains(other, tildePath(home, chromeHostManifestPath(home, chrome))) {
		t.Errorf("--check does not say where the manifest of a different reminal is, and only that one:\n%s", other)
	}

	// -v is --check's.
	if err := runIntegrate([]string{"-v"}); err == nil || !strings.Contains(err.Error(), "--check") {
		t.Errorf("-v without --check: %v, want an error naming --check", err)
	}
}

// The extensions page is the first installed browser's own: Edge has its
// name for it. A browser set up from its folder alone does not count where
// another's application was found.
func TestTheStepsNameTheFirstBrowsersExtensionsPage(t *testing.T) {
	home := integrateRig(t, 80)
	table := map[string]chromeBrowser{}
	for _, b := range chromeBrowserTable() {
		table[b.Name] = b
	}
	chrome, edge, brave := table["Google Chrome"], table["Microsoft Edge"], table["Brave"]
	for _, c := range []struct {
		browsers []chromeBrowser
		want     string
	}{
		{[]chromeBrowser{chrome, edge}, "chrome://extensions"},
		{[]chromeBrowser{edge, brave}, "edge://extensions"},
		{[]chromeBrowser{brave, edge}, "edge://extensions"}, // Brave's application is not here
		{[]chromeBrowser{brave}, "chrome://extensions"},
	} {
		if got := chromeExtensionsPage(home, c.browsers); got != c.want {
			t.Errorf("%v: %s, want %s", chromeNames(c.browsers), got, c.want)
		}
	}
}

// An agent reminal was already set up with, and that asks for the tool list
// again when told it changed, is not told to restart on a later run: a
// running one finds new tools by itself. One that does not ask again still
// is, and so is every agent on its first setup.
func TestNoRestartLineForAnAgentThatFindsNewToolsItself(t *testing.T) {
	integrateRig(t, 80)
	first := runIntegrateRig(t, "claude", "-y")
	if !strings.Contains(first, "Restart a running Claude Code") {
		t.Fatalf("a first setup of Claude Code has no restart line:\n%s", first)
	}
	again := runIntegrateRig(t, "claude", "-y")
	if strings.Contains(again, "Restart a running ") {
		t.Errorf("a second run tells a running Claude Code to restart:\n%s", again)
	}
	runIntegrateRig(t, "codex", "-y")
	if again := runIntegrateRig(t, "codex", "-y"); !strings.Contains(again, "Restart a running Codex CLI") {
		t.Errorf("a second run does not tell a running Codex CLI to restart:\n%s", again)
	}
	if agentNeedsRestart("Claude Code", false) != true || agentNeedsRestart("Claude Code", true) != false || agentNeedsRestart("Codex CLI", true) != true {
		t.Error("agentNeedsRestart: first setup always; later only for an agent that does not ask again")
	}
}

// One restart line for each agent set up in this run, and none for an agent
// that was not; an agent with no resume command is still told to restart.
func TestRestartLinesAreForThisRunsAgents(t *testing.T) {
	integrateRig(t, 80)
	out := runIntegrateRig(t, "codex", "-y")
	if got := strings.Count(out, "Restart a running "); got != 1 || !strings.Contains(out, "Restart a running Codex CLI to pick up new tools: codex resume --last\n") {
		t.Errorf("setting up Codex alone says %d restart lines:\n%s", got, out)
	}
	if strings.Contains(out, "your browser") {
		t.Errorf("a run for one agent talks of the browser:\n%s", out)
	}
	for _, tg := range agentTargets() {
		line := restartLine(tg)
		if !agentNeedsRestart(tg.Name, false) || !strings.HasPrefix(line, "Restart a running ") {
			t.Errorf("%s: %q", tg.Name, line)
		}
		if resume := tg.resumeHint(); resume != "" && !strings.HasSuffix(line, ": "+noBreak(resume)) {
			t.Errorf("%s: its line does not end on its resume command %q: %q", tg.Name, resume, line)
		}
	}
}

// A real machine's look for a browser's application: its binary on PATH, or
// its entry in the applications menu (Linux), its .app (macOS).
func TestChromeAppHere(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stands in for Linux and macOS with their files")
	}
	home := t.TempDir()
	bin := filepath.Join(home, "bin")
	t.Setenv("PATH", bin)
	oldOS, oldDirs := chromeOS, chromeAppDirs
	t.Cleanup(func() { chromeOS, chromeAppDirs = oldOS, oldDirs })
	// Only the folders in the test's home: the machine's own may hold a
	// browser.
	chromeAppDirs = func(home string) []string {
		return []string{filepath.Join(home, ".local", "share", "applications"), filepath.Join(home, "Applications")}
	}
	byName := func() map[string]chromeBrowser {
		m := map[string]chromeBrowser{}
		for _, b := range chromeBrowserTable() {
			m[b.Name] = b
		}
		return m
	}
	write := func(path string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	chromeOS = "linux"
	linux := byName()
	for name, b := range linux {
		if chromeAppHere(home, b) {
			t.Errorf("control: %s found on a machine with none", name)
		}
	}
	write(filepath.Join(bin, "google-chrome-stable"))
	write(filepath.Join(home, ".local", "share", "applications", "brave-browser.desktop"))
	for name, b := range linux {
		if got, want := chromeAppHere(home, b), name == "Google Chrome" || name == "Brave"; got != want {
			t.Errorf("linux: %s found %v, want %v", name, got, want)
		}
	}

	chromeOS = "darwin"
	mac := byName()
	if err := os.MkdirAll(filepath.Join(home, "Applications", "Arc.app"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, b := range mac {
		if name == "Arc" && !chromeAppHere(home, b) {
			t.Errorf("mac: Arc.app in ~/Applications not found")
		}
		if name == "Chromium" && chromeAppHere(home, b) {
			t.Errorf("mac: Chromium found with no Chromium.app")
		}
	}
}
