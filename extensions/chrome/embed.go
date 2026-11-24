// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

// Package chromeext is reminal's Chrome extension, embedded so `reminal
// integrate chrome` can write it out for "Load unpacked" (it is not in the
// Chrome Web Store yet).
package chromeext

import (
	"crypto/sha256"
	"embed"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
)

// Files is the extension: its manifest, scripts, pages and icons.
//
//go:embed manifest.json managed_schema.json *.js *.html *.css icons
var Files embed.FS

// Version is the extension's version as reminal writes it out: "0.1.a.b",
// with a and b taken from a hash of every file of it. The browser keeps
// running the extension it loaded (its service worker's script is cached
// under the extension's version, across a restart of the browser too), so
// files that change under an unchanged version are not picked up: an
// updated reminal's host then talked to the old extension, and its first
// request hung and was refused. A version that changes with the files makes
// the browser take them at its next start, and lets the running extension
// see that reminal has newer ones (the host says this version) and reload
// itself.
func Version() string {
	h := sha256.New()
	_ = fs.WalkDir(Files, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(Files, p)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", p, len(b))
		h.Write(b)
		return nil
	})
	s := h.Sum(nil)
	// Each part of a browser's extension version is at most 65535.
	return fmt.Sprintf("0.1.%d.%d", binary.BigEndian.Uint16(s[0:2]), binary.BigEndian.Uint16(s[2:4]))
}

var (
	manifestVersion = regexp.MustCompile(`"version":\s*"[^"]*"`)
	workerCode      = regexp.MustCompile(`(?m)^const CODE = '[^']*';$`)
)

// File is one file of the extension as reminal writes it out: the manifest
// with Version in place of the version it is kept with, the worker's script
// (background.js) with Version as the version it says it is (its CODE),
// every other file as it is.
//
// The worker's own copy of the version is what tells a browser running an
// older script apart: a browser keeps the worker's script with the profile,
// and started again after reminal replaced the files while it was closed, it
// can run the script it kept against the newer manifest. The manifest's
// version is then the new one whichever script runs; the script's is its
// own, and one that differs from the manifest's reloads the extension.
func File(p string) ([]byte, error) {
	b, err := fs.ReadFile(Files, p)
	if err != nil {
		return b, err
	}
	switch p {
	case "manifest.json":
		if !manifestVersion.Match(b) {
			return nil, errors.New("the extension's manifest has no version")
		}
		return manifestVersion.ReplaceAll(b, []byte(`"version": "`+Version()+`"`)), nil
	case "background.js":
		if len(workerCode.FindAll(b, -1)) != 1 {
			return nil, errors.New("the extension's worker does not say its version once")
		}
		return workerCode.ReplaceAll(b, []byte(`const CODE = '`+Version()+`';`)), nil
	}
	return b, nil
}
