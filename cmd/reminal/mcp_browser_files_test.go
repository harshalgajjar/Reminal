// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"encoding/base64"
	"encoding/json"
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
		".ssh/config", ".aws/config", ".gnupg/pubring.kbx", ".reminal/atrest.json", ".config/gcloud/application_default_credentials.json", "Chrome/Default/Cookies", "Chrome/Default/Login Data", "vault.kdbx",
		// A Mac's disk does not tell upper case from lower: neither does the check.
		".SSH/config", ".Aws/credentials", "Library/Keychains/login.keychain-db", "LIBRARY/KEYCHAINS/x.db", "ID_RSA", "Server.PEM",
		// Where tools keep their tokens: any folder that starts with a dot.
		".config/gh/hosts.yml", ".claude/settings.json", ".codex/auth.json", ".npm/_cacache/x", "work/.git/config", ".local/share/keyrings/login.keyring",
		// The browsers' own profiles, whatever the file.
		"Library/Application Support/Google/Chrome/Default/Web Data", "Library/Application Support/Firefox/Profiles/x/key4.db", "Library/Application Support/Firefox/Profiles/x/logins.json",
		"Library/Application Support/BraveSoftware/Brave-Browser/Default/History", "Library/Safari/History.db"} {
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

// A batch's steps are sent on as they were checked: a step whose tool is
// not a name, or whose arguments are not an object, is refused (it must not
// be read one way here and another way in the browser), and an upload step
// is held to the upload's rules.
func TestBrowserBatchStepsAreCheckedAsSent(t *testing.T) {
	dir := t.TempDir()
	ok := filepath.Join(dir, "a.txt")
	secret := filepath.Join(dir, "server.pem")
	for _, p := range []string{ok, secret} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bad := []map[string]any{
		{"actions": []any{map[string]any{"tool": []any{"file_upload"}, "args": map[string]any{"paths": []any{secret}}}}},
		{"actions": []any{map[string]any{"tool": map[string]any{"toString": "file_upload"}, "args": map[string]any{"paths": []any{secret}}}}},
		{"actions": []any{map[string]any{"tool": 7, "args": map[string]any{}}}},
		{"actions": []any{map[string]any{"tool": "click", "args": []any{"ref_1"}}}},
		{"actions": []any{map[string]any{"tool": "click", "args": "ref_1"}}},
		{"actions": []any{"click"}},
		{"actions": []any{map[string]any{"tool": "file_upload", "args": map[string]any{"paths": []any{secret}}}}},
		{"actions": []any{map[string]any{"tool": "browser_file_upload", "args": map[string]any{"paths": []any{ok, secret}}}}},
		{"actions": []any{map[string]any{"tool": "file_upload"}}},
		{"actions": []any{map[string]any{"tool": "batch", "args": map[string]any{"actions": []any{map[string]any{"tool": "file_upload", "args": map[string]any{"paths": []any{secret}}}}}}}},
		{"actions": []any{map[string]any{"tool": "type", "args": map[string]any{"text": strings.Repeat("x", browserMaxType+1)}}}},
		{"actions": "click"},
		{},
	}
	for i, args := range bad {
		if err := browserCheckCall("batch", args); err == nil {
			t.Errorf("bad batch %d was let through: %v", i, args)
		}
	}
	args := map[string]any{"actions": []any{
		map[string]any{"tool": "browser_click", "args": map[string]any{"ref": "ref_1"}, "extra": "dropped"},
		map[string]any{"tool": "file_upload", "args": map[string]any{"paths": []any{ok}}},
		map[string]any{"tool": "read_page"},
	}}
	if err := browserCheckCall("batch", args); err != nil {
		t.Fatal(err)
	}
	steps := args["actions"].([]any)
	first := steps[0].(map[string]any)
	if first["tool"] != "click" || len(first) != 2 {
		t.Errorf("a step is not sent on as {tool, args} alone: %v", first)
	}
	if _, isObj := steps[2].(map[string]any)["args"].(map[string]any); !isObj {
		t.Errorf("a step with no args is not sent with an empty object: %v", steps[2])
	}
	// The browser's host looks again: a request that reached the hub another
	// way, with a secret, or a path not as the check leaves it, is answered
	// with a refusal in its place; a checked one goes through.
	// (A path goes into the JSON as JSON: a Windows path has backslashes.)
	q := func(p string) string {
		b, _ := json.Marshal(p)
		return string(b)
	}
	real, _ := browserUploadPath(ok)
	if b := hostRefuses([]byte(`{"t":"req","id":4,"tool":"file_upload","args":{"paths":[` + q(real) + `]}}`)); b != nil {
		t.Errorf("a checked upload was refused by the host: %s", b)
	}
	realSecret, _ := browserRealPath(secret)
	for _, msg := range []string{
		`{"t":"req","id":5,"tool":"file_upload","args":{"paths":[` + q(realSecret) + `]}}`,
		`{"t":"req","id":5,"tool":"file_upload","args":{"paths":["a.txt"]}}`,
		`{"t":"req","id":5,"tool":"file_upload","args":{}}`,
		`{"t":"req","id":5,"tool":"batch","args":{"actions":[{"tool":"file_upload","args":{"paths":[` + q(realSecret) + `]}}]}}`,
		`{"t":"req","id":5,"tool":"batch","args":{"actions":[{"tool":["file_upload"],"args":{"paths":[` + q(realSecret) + `]}}]}}`,
	} {
		b := hostRefuses([]byte(msg))
		if b == nil || !strings.Contains(string(b), `"ok":false`) || !strings.Contains(string(b), `"id":5`) {
			t.Errorf("the host let through, or did not answer: %s -> %s", msg, b)
		}
	}
	if b := hostRefuses([]byte(`{"t":"req","id":6,"tool":"click","args":{"ref":"ref_1"}}`)); b != nil {
		t.Errorf("an ordinary request was refused by the host: %s", b)
	}
}

// A file on another computer is turned down by its name alone.
func TestBrowserUploadOnlyFromThisComputer(t *testing.T) {
	old := chromeOS
	t.Cleanup(func() { chromeOS = old })
	chromeOS = "windows"
	for p, want := range map[string]bool{
		`\\server\share\a.txt`:                                   true,
		`//server/share/a.txt`:                                   true,
		`\\?\UNC\server\share\a.txt`:                             true,
		`\\?\unc\server\share\a.txt`:                             true,
		`\\10.0.0.5\c$\Users\a.txt`:                              true,
		`\\.\UNC\server\share\a.txt`:                             true,
		`\\?\GLOBALROOT\Device\Mup\server\share\a.txt`:           true,
		`\\.\GLOBALROOT\Device\Mup\server\share\a.txt`:           true,
		`\\.\pipe\x`:                                             true,
		`\\?\Volume{4c1b02c1-d990-11dc-99ae-806e6f6e6963}\a.txt`: true,
		`\\?\C:`:                       true,
		`C:\Users\me\a.txt`:            false,
		`\\?\C:\Users\me\a.txt`:        false,
		`\\.\C:\Users\me\a.txt`:        false,
		`C:/Users/me/a.txt`:            false,
		`C:\Users\me\server\share.txt`: false,
	} {
		if got := namesAnotherComputer(p); got != want {
			t.Errorf("namesAnotherComputer(%q) = %v, want %v", p, got, want)
		}
	}
	chromeOS = "linux"
	if namesAnotherComputer("//server/share/a.txt") {
		t.Error("off Windows, a path with two slashes is an ordinary path")
	}
}
