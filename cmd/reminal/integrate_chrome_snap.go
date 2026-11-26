// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

// Chromium as a snap (what Ubuntu installs for "chromium") is confined: it
// may not start a program in a hidden folder of the home folder, where
// reminal is, nor reach ~/.reminal, so the extension's helper, which is this
// reminal, never started and the extension found nothing to talk to. It may
// start a program in its own folder and reach a socket there, so that is
// where its setup goes (client.SnapBrowserSetUp): a copy of reminal, the
// host manifest naming the copy, and a second socket of the daemon's hub.
// Where that folder cannot be used, the browser is said not to work, in its
// row and in --check, with what does. The usual host manifest is written
// all the same: a Chromium installed another way reads that folder.

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"reminal/internal/client"
)

// chromeSnapWhy is said of a browser that is here only as a snap, when the
// snap's folder cannot be set up.
const chromeSnapWhy = "installed as a snap, which may not start reminal, so agents cannot use it. Chrome, Edge and Brave work, and so does Chromium installed without snap"

// chromeSnapCanSetUp: the snap's folder can take reminal's setup. A var so a
// test can say.
var chromeSnapCanSetUp = client.SnapBrowserCanSetUp

// chromeSnapHere reports whether b is on this machine as a snap, alone or
// beside one installed another way: a program of its on PATH is the snap's,
// its snap entry is in the applications menu, or the snap's folder is in
// the home folder. A var so a test can say.
var chromeSnapHere = func(home string, b chromeBrowser) bool {
	if chromeOS != "linux" || b.Name != "Chromium" {
		return false
	}
	app := chromeApps[b.Name]
	for _, bin := range app.bins {
		if p, err := chromeLookPath(bin); err == nil && snapProgram(p) {
			return true
		}
	}
	for _, dir := range chromeAppDirs(home) {
		for _, name := range app.desktop {
			if strings.Contains(name, "_") {
				if _, err := os.Stat(filepath.Join(dir, name+".desktop")); err == nil {
					return true
				}
			}
		}
	}
	st, err := os.Lstat(filepath.Join(home, "snap", "chromium"))
	return err == nil && st.IsDir()
}

// chromeSnapApply sets b up in its snap's own folder when it is here as a
// snap (reminal's copy, then the host manifest naming the copy), or on
// remove takes both away, and the socket, whether the snap is still here
// or not.
func chromeSnapApply(home, exe string, b chromeBrowser, remove bool) error {
	if chromeOS != "linux" || b.Name != "Chromium" {
		return nil
	}
	name := browserHostName + ".json"
	if remove {
		err := client.SnapBrowserRemoveManifest(home, name)
		if rerr := client.SnapBrowserRemove(home); err == nil {
			err = rerr
		}
		return err
	}
	if !chromeSnapHere(home, b) {
		return nil
	}
	if !chromeSnapCanSetUp(home) {
		return nil // said in its row (chromeSnapOnly), or it works another way
	}
	if err := client.SnapBrowserSetUp(home, exe); err != nil {
		return err
	}
	return client.SnapBrowserWriteManifest(home, name, chromeHostManifest(client.SnapBrowserHelper(home)))
}

// chromeSnapSetUpCurrent reports whether b, where it is here as a snap, is
// set up there for the reminal at exe (true for one that is not a snap).
func chromeSnapSetUpCurrent(home, exe string, b chromeBrowser) bool {
	if !chromeSnapHere(home, b) || !chromeSnapCanSetUp(home) {
		return true
	}
	raw, err := client.SnapBrowserManifest(home, browserHostName+".json")
	if err != nil || string(raw) != string(chromeHostManifest(client.SnapBrowserHelper(home))) {
		return false
	}
	_, current := client.SnapBrowserSetUpDone(home, exe)
	return current
}

// chromeLookPath finds a program on PATH; a var so a test has a PATH of its
// own.
var chromeLookPath = exec.LookPath

// chromeCannotStart is why b cannot work with reminal on this machine, or
// "" when nothing says it cannot. A var so a test can say.
var chromeCannotStart = chromeSnapOnly

// chromeSnapOnly is chromeSnapWhy when b is here as a snap and in no other
// way, and the snap's folder cannot be set up (chromeSnapCanSetUp). Its programs on PATH say which: each is the snap's (snapProgram) or
// is not. With none on PATH, its entries in the applications menu do: the
// snap's is named <snap>_<app>.
func chromeSnapOnly(home string, b chromeBrowser) string {
	// Chromium alone: the one seen not to work as a snap (the line names
	// the other browsers as working).
	if chromeOS != "linux" || b.Name != "Chromium" || chromeSnapCanSetUp(home) {
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
