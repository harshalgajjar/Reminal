// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An agent may hand a page ordinary files; never a key, a password store or
// a sign-in file, by its name or its folder, nor through a link to one.
func TestBrowserUploadRefusesSecrets(t *testing.T) {
	dir := t.TempDir()
	write := func(rel string) string {
		p := filepath.Join(dir, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	for _, rel := range []string{"notes.txt", "photo.png", "src/main.go", "id_rsa.pub", "env.md", "keynote.pdf"} {
		got, err := browserUploadPath(write(rel))
		if err != nil {
			t.Errorf("%s refused: %v", rel, err)
		} else if !filepath.IsAbs(got) {
			t.Errorf("%s: not absolute: %s", rel, got)
		}
	}
	for _, rel := range []string{".env", ".env.production", "server.pem", "tls.key", "cert.p12", "id_rsa", "id_ed25519", ".netrc", ".npmrc", ".git-credentials",
		".ssh/config", ".aws/config", ".gnupg/pubring.kbx", ".reminal/atrest.json", ".config/gcloud/application_default_credentials.json", "Chrome/Default/Cookies", "Chrome/Default/Login Data", "vault.kdbx"} {
		if _, err := browserUploadPath(write(rel)); err == nil || !strings.Contains(err.Error(), "does not upload those") {
			t.Errorf("%s: want it refused as a secret, got %v", rel, err)
		}
	}
	// A link with an innocent name to a secret is the secret.
	secret := write(".ssh/id_ed25519")
	link := filepath.Join(dir, "resume.txt")
	if err := os.Symlink(secret, link); err == nil {
		if _, err := browserUploadPath(link); err == nil {
			t.Error("a link to a private key was not refused")
		}
	}
	if _, err := browserUploadPath(filepath.Join(dir, "none.txt")); err == nil {
		t.Error("a missing file was not refused")
	}
	if _, err := browserUploadPath(dir); err == nil {
		t.Error("a folder was not refused")
	}
	args := map[string]any{"paths": []any{write("a.txt"), write(".env")}}
	if err := browserUploadArgs(args); err == nil {
		t.Error("a list with a secret in it was not refused")
	}
	if err := browserUploadArgs(map[string]any{}); err == nil {
		t.Error("no paths was not refused")
	}
}

// A page's PDF is written where asked, as a .pdf, and never over a file.
func TestBrowserSavePDFNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	data := base64.StdEncoding.EncodeToString([]byte("%PDF-1.7 test"))
	p := filepath.Join(dir, "out")
	said, err := browserSavePDF(map[string]any{"path": p}, data)
	if err != nil || !strings.Contains(said, p+".pdf") {
		t.Fatalf("save: %q, %v", said, err)
	}
	if b, _ := os.ReadFile(p + ".pdf"); string(b) != "%PDF-1.7 test" {
		t.Errorf("written %q", b)
	}
	if _, err := browserSavePDF(map[string]any{"path": p + ".pdf"}, data); err == nil || !strings.Contains(err.Error(), "already there") {
		t.Errorf("a second save over it: %v", err)
	}
}
