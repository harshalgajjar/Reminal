// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build darwin

package client

import (
	"os"
	"path/filepath"
	"testing"
)

// The gate that failed the first time. "We are running from a bundle" was true
// for a scratch install too, so an upgrade in /tmp kickstarted the machine's
// real daemon. The question has to be "are we the binary this service runs".
func TestSameInstallFollowsSymlinks(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "reminal")
	if err := os.WriteFile(real, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link-to-reminal")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "other")
	if err := os.WriteFile(other, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}

	if !sameInstall(link, real) {
		t.Error("a symlink to the binary was not recognised as the same install — " +
			"an upgrade leaves the executable exactly like this, so the bounce would never fire")
	}
	if sameInstall(real, other) {
		t.Error("two different binaries compared equal")
	}
	if sameInstall(real, "") || sameInstall("", real) {
		t.Error("an empty path (no service installed) compared equal — that is the case that must NOT bounce")
	}
	if sameInstall(real, filepath.Join(dir, "does-not-exist")) {
		t.Error("a missing path compared equal")
	}
}

// A scratch install must never be mistaken for the machine's daemon.
func TestDaemonProgramPathParsesThePlist(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// targetUser() reads the passwd home, not $HOME, so this test exercises
	// the parser directly rather than the lookup.
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
  <key>Label</key><string>sh.reminal.daemon</string>
  <key>ProgramArguments</key>
  <array>
    <string>/Users/someone/Applications/reminal.app/Contents/MacOS/reminal</string>
    <string>daemon</string>
  </array>
</dict></plist>`
	p := filepath.Join(dir, "sh.reminal.daemon.plist")
	if err := os.WriteFile(p, []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	got := parseDaemonProgram([]byte(plist))
	want := "/Users/someone/Applications/reminal.app/Contents/MacOS/reminal"
	if got != want {
		t.Errorf("parsed %q, want %q", got, want)
	}
	if parseDaemonProgram([]byte("<plist><dict></dict></plist>")) != "" {
		t.Error("a plist with no ProgramArguments produced a path; the safe answer is empty")
	}
	if parseDaemonProgram([]byte("not a plist at all")) != "" {
		t.Error("garbage produced a path")
	}
}
