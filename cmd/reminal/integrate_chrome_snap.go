// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

// Chromium as a snap (what Ubuntu installs for "chromium") is confined: it
// may not start a program outside its own snap, so the extension's helper,
// which is this reminal, never starts and the extension finds nothing to
// talk to. Integrate ticked it "ready" all the same and it failed with no
// word. A browser reminal cannot work with is said so, in its row and in
// --check, with what does work. The host manifest is still written: a
// Chromium installed another way later reads the same folder.

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// chromeSnapWhy is said of a browser that is here only as a snap.
const chromeSnapWhy = "installed as a snap, which may not start reminal, so agents cannot use it. Chrome, Edge and Brave work, and so does Chromium installed without snap"

// chromeLookPath finds a program on PATH; a var so a test has a PATH of its
// own.
var chromeLookPath = exec.LookPath

// chromeCannotStart is why b cannot work with reminal on this machine, or
// "" when nothing says it cannot. A var so a test can say.
var chromeCannotStart = chromeSnapOnly

// chromeSnapOnly is chromeSnapWhy when b is here as a snap and in no other
// way. Its programs on PATH say which: each is the snap's (snapProgram) or
// is not. With none on PATH, its entries in the applications menu do: the
// snap's is named <snap>_<app>.
func chromeSnapOnly(home string, b chromeBrowser) string {
	// Chromium alone: the one seen not to work as a snap (the line names
	// the other browsers as working).
	if chromeOS != "linux" || b.Name != "Chromium" {
		return ""
	}
	app, ok := chromeApps[b.Name]
	if !ok {
		return ""
	}
	snap := false
	for _, bin := range app.bins {
		p, err := chromeLookPath(bin)
		if err != nil {
			continue
		}
		if !snapProgram(p) {
			return ""
		}
		snap = true
	}
	if snap {
		return chromeSnapWhy
	}
	for _, dir := range chromeAppDirs(home) {
		for _, name := range app.desktop {
			if _, err := os.Stat(filepath.Join(dir, name+".desktop")); err != nil {
				continue
			}
			if !strings.Contains(name, "_") {
				return ""
			}
			snap = true
		}
	}
	if snap {
		return chromeSnapWhy
	}
	return ""
}

// snapProgram reports whether the program at p is a snap's: in snap's own
// folder of programs, a link to snap itself (as each of those is), or the
// small script a distribution puts in a package's old place to start the
// snap (Ubuntu's /usr/bin/chromium-browser).
func snapProgram(p string) bool {
	inSnap := func(q string) bool {
		q = filepath.ToSlash(q)
		return strings.HasPrefix(q, "/snap/") || strings.Contains(q, "/snapd/snap/") || filepath.Base(q) == "snap"
	}
	if inSnap(p) {
		return true
	}
	if q, err := filepath.EvalSymlinks(p); err == nil && inSnap(q) {
		return true
	}
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	head, _ := io.ReadAll(io.LimitReader(f, 8<<10))
	if !bytes.HasPrefix(head, []byte("#!")) {
		return false
	}
	return bytes.Contains(head, []byte("/snap/bin/")) || bytes.Contains(head, []byte("snap run "))
}
