// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// On a real Windows the never-upload check is made on the file's long, real
// name, whatever name it was asked for by: a short 8.3 name, a name with a
// dot or a space after it, a junction, a \\?\ path and a stream all reach
// the same refusal as the plain name does.
func TestUploadRefusalsHoldForEveryWindowsNameOfAFile(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	write := func(p string) string {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	plain := write(filepath.Join(dir, "documents", "notes.txt"))
	key := write(filepath.Join(dir, "documents", "server-certificate.pem"))
	hidden := write(filepath.Join(dir, ".longsecretfolder", "notes.txt"))

	if got, err := browserUploadPath(plain); err != nil || !strings.EqualFold(got, plain) {
		t.Fatalf("control: an ordinary file is refused: %q %v", got, err)
	}
	// Refused as a secret, not for some other reason (a name that could
	// not be opened would prove nothing).
	refused := func(what, p string) {
		t.Helper()
		got, err := browserUploadPath(p)
		if err == nil {
			t.Errorf("%s: %s may be given to a page (as %s)", what, p, got)
			return
		}
		if !strings.Contains(err.Error(), "looks like a key") {
			t.Errorf("%s: turned down, but not as a secret: %v", what, err)
			return
		}
		t.Logf("%s: refused", what)
	}
	refused("the plain name of a key", key)
	refused("the plain name in a dot folder", hidden)
	refused("a dot after the name", key+".")
	refused("a space after the name", key+" ")
	refused("a stream of the file", key+"::$DATA")
	refused("a stream of an ordinary file", plain+"::$DATA")
	refused(`a \\?\ path`, `\\?\`+key)
	refused(`a \\?\ path into a dot folder`, `\\?\`+hidden)
	refused("upper case", strings.ToUpper(key))

	// The short (8.3) names, where this disk keeps them.
	short := func(p string) string {
		in, _ := windows.UTF16PtrFromString(p)
		buf := make([]uint16, 1024)
		n, err := windows.GetShortPathName(in, &buf[0], uint32(len(buf)))
		if err != nil || n == 0 {
			return ""
		}
		return windows.UTF16ToString(buf[:n])
	}
	if s := short(hidden); s != "" && !strings.EqualFold(s, hidden) {
		refused("a short name of a dot folder", s)
	} else {
		t.Logf("this disk keeps no short name for %s (%q): not checked", hidden, s)
	}
	if s := short(key); s != "" && !strings.EqualFold(s, key) {
		refused("a short name of a key", s)
	}

	// A junction to the dot folder, under a name with no dot.
	link := filepath.Join(dir, "documents", "shortcut")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, filepath.Dir(hidden)).CombinedOutput(); err != nil {
		t.Fatalf("mklink /J: %v %s", err, out)
	}
	refused("through a junction", filepath.Join(link, "notes.txt"))
	// And a junction to an ordinary folder is no reason to refuse.
	ok := filepath.Join(dir, "elsewhere")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", ok, filepath.Dir(plain)).CombinedOutput(); err != nil {
		t.Fatalf("mklink /J: %v %s", err, out)
	}
	if _, err := browserUploadPath(filepath.Join(ok, "notes.txt")); err != nil {
		t.Errorf("control: an ordinary file through a junction is refused: %v", err)
	}
}
