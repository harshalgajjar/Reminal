// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/reminal/reminal/internal/crypto"
)

// isolateReminalHome points reminalDir() at a temp dir on both Unix (HOME)
// and Windows (USERPROFILE).
func isolateReminalHome(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
}

func TestScrollbackDumpRoundTripReencrypts(t *testing.T) {
	oldKey, err := crypto.NewSessionKey()
	if err != nil {
		t.Fatal(err)
	}
	oldBox, err := crypto.NewBox(oldKey)
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{sessionID: "ABCD2345", box: oldBox, buf: newScrollback(1 << 20)}
	a.buf.SetBase(80, 24)
	a.record([]byte("hello from before restart\r\n"))
	a.buf.AppendResize(100, 30)
	a.record([]byte("after resize\r\n"))
	barEnc, err := oldBox.Encrypt([]byte("status-bar-chrome"))
	if err != nil {
		t.Fatal(err)
	}
	a.buf.AppendBar(barEnc)

	isolateReminalHome(t)
	path, err := a.writeScrollbackDump()
	if err != nil {
		t.Fatal(err)
	}
	if path == "" {
		t.Fatal("expected a dump file")
	}
	if st, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Fatalf("dump mode %o, want 0600", st.Mode().Perm())
	}

	dump := takeScrollbackDump(path)
	if dump == nil {
		t.Fatal("takeScrollbackDump returned nil")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("dump file should be deleted after take")
	}
	if got := string(dump.Entries[0].Data); got != "hello from before restart\r\n" {
		t.Fatalf("plaintext[0] = %q", got)
	}

	newKey, err := crypto.NewSessionKey()
	if err != nil {
		t.Fatal(err)
	}
	newBox, err := crypto.NewBox(newKey)
	if err != nil {
		t.Fatal(err)
	}
	b := &Agent{box: newBox, buf: newScrollback(1 << 20), resumeDump: dump}
	b.restoreResumedScrollback()

	if b.buf.LatestSeq() != a.buf.LatestSeq() {
		t.Fatalf("nextSeq %d, want %d", b.buf.LatestSeq(), a.buf.LatestSeq())
	}
	got := b.buf.From(0)
	if len(got) != 4 {
		t.Fatalf("entries %d, want 4 (2 data + resize + bar)", len(got))
	}
	pt, err := newBox.Decrypt(got[0].Data)
	if err != nil {
		t.Fatalf("new key must decrypt restored entry: %v", err)
	}
	if string(pt) != "hello from before restart\r\n" {
		t.Fatalf("restored plaintext = %q", pt)
	}
	if _, err := oldBox.Decrypt(got[0].Data); err == nil {
		t.Fatal("old session key still decrypted the restored entry")
	}
	if got[1].Cols != 100 || got[1].Rows != 30 {
		t.Fatalf("resize marker = %dx%d", got[1].Cols, got[1].Rows)
	}
	if !got[3].Bar {
		t.Fatal("bar flag lost across restore")
	}
	barPT, err := newBox.Decrypt(got[3].Data)
	if err != nil {
		t.Fatalf("bar decrypt: %v", err)
	}
	if string(barPT) != "status-bar-chrome" {
		t.Fatalf("bar plaintext = %q", barPT)
	}
	bc, br := b.buf.Base()
	if bc != 80 || br != 24 {
		t.Fatalf("base %dx%d, want 80x24", bc, br)
	}
}

func TestRestoreResumedScrollbackReplaysScreen(t *testing.T) {
	oldKey, err := crypto.NewSessionKey()
	if err != nil {
		t.Fatal(err)
	}
	oldBox, err := crypto.NewBox(oldKey)
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{sessionID: "SCRN2345", box: oldBox, buf: newScrollback(1 << 20)}
	isolateReminalHome(t)
	a.initScreen()
	a.record([]byte("kept across restart\r\n"))

	path, err := a.writeScrollbackDump()
	if err != nil || path == "" {
		t.Fatalf("dump: path=%q err=%v", path, err)
	}
	dump := takeScrollbackDump(path)
	if dump == nil {
		t.Fatal("take returned nil")
	}

	newKey, err := crypto.NewSessionKey()
	if err != nil {
		t.Fatal(err)
	}
	newBox, err := crypto.NewBox(newKey)
	if err != nil {
		t.Fatal(err)
	}
	b := &Agent{box: newBox, buf: newScrollback(1 << 20), resumeDump: dump}
	b.initScreen()
	b.restoreResumedScrollback()
	if b.screen == nil {
		t.Fatal("expected emulator after initScreen")
	}
	if !strings.Contains(b.screen.Render(), "kept across restart") {
		t.Fatalf("screen missing restored text: %q", b.screen.Render())
	}
}

func TestScrollbackDumpEmptyIsNoop(t *testing.T) {
	key, _ := crypto.NewSessionKey()
	box, _ := crypto.NewBox(key)
	a := &Agent{sessionID: "EMPTY001", box: box, buf: newScrollback(1 << 20)}
	isolateReminalHome(t)
	path, err := a.writeScrollbackDump()
	if err != nil {
		t.Fatal(err)
	}
	if path != "" {
		t.Fatalf("empty dump path = %q, want empty", path)
	}
}

func TestTakeScrollbackDumpMissing(t *testing.T) {
	if dump := takeScrollbackDump(filepath.Join(t.TempDir(), "nope.json")); dump != nil {
		t.Fatal("missing file should yield nil")
	}
}

func TestTakeScrollbackDumpCorruptDeletes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dump.json")
	if err := os.WriteFile(path, []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if dump := takeScrollbackDump(path); dump != nil {
		t.Fatal("corrupt JSON should yield nil")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("corrupt dump should be deleted")
	}
}

func TestTakeScrollbackDumpWrongVersionDeletes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dump.json")
	body, err := json.Marshal(scrollbackDump{
		Version: 99,
		NextSeq: 1,
		Entries: []scrollDumpEntry{{Seq: 1, Data: []byte("secret")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if dump := takeScrollbackDump(path); dump != nil {
		t.Fatal("wrong version should yield nil")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("wrong-version dump should be deleted")
	}
}

func TestWriteScrollbackDumpReplacesLeftover(t *testing.T) {
	isolateReminalHome(t)
	key, err := crypto.NewSessionKey()
	if err != nil {
		t.Fatal(err)
	}
	box, err := crypto.NewBox(key)
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{sessionID: "LEFT2345", box: box, buf: newScrollback(1 << 20)}
	a.record([]byte("first\r\n"))
	path, err := a.writeScrollbackDump()
	if err != nil || path == "" {
		t.Fatalf("first dump: path=%q err=%v", path, err)
	}
	a.record([]byte("second\r\n"))
	path2, err := a.writeScrollbackDump()
	if err != nil || path2 == "" {
		t.Fatalf("replace dump: path=%q err=%v", path2, err)
	}
	if path2 != path {
		t.Fatalf("path changed %q -> %q", path, path2)
	}
	dump := takeScrollbackDump(path2)
	if dump == nil || len(dump.Entries) < 2 {
		t.Fatalf("replaced dump = %+v", dump)
	}
	if got := string(dump.Entries[len(dump.Entries)-1].Data); got != "second\r\n" {
		t.Fatalf("replaced plaintext = %q", got)
	}
}

func TestRemoveScrollbackDump(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dump.json")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".tmp", []byte("y"), 0o600); err != nil {
		t.Fatal(err)
	}
	removeScrollbackDump(path)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("path should be gone")
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal(".tmp should be gone")
	}
	removeScrollbackDump("") // no panic
}

func TestLoadResumeStateTakesDumpOnValidationError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dump.json")
	body, err := json.Marshal(scrollbackDump{
		Version: scrollbackDumpVersion,
		NextSeq: 1,
		Entries: []scrollDumpEntry{{Seq: 1, Data: []byte("secret")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envResume, "1")
	t.Setenv(envResumeScrollback, path)
	// Leave id/pin empty so validation fails after the take.
	if _, err := LoadResumeState(); err == nil {
		t.Fatal("expected validation error")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("dump should be deleted even when resume validation fails")
	}
}
