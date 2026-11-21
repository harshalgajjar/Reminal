// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

// Files and the browser tools: which files on this computer an agent may
// hand to a page (browser_file_upload), and where a page saved as a PDF is
// written (browser_pdf_save). Both are decided here, where the agent runs:
// the extension is given paths already checked, and hands a PDF's bytes back.

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	browserUploadMaxFiles = 20
	browserUploadMaxBytes = 200 << 20
)

// browserSecretFolders are folders (lowercased, as part of a path) whose
// files are never uploaded: the system's keychains and the browsers' own
// profiles (cookies, saved sign-ins, keys). Any folder whose name starts
// with a dot is refused too (browserSecretPath): that is where tools keep
// their tokens (.ssh, .aws, .config/gh, .claude, .npm ...).
var browserSecretFolders = []string{
	"/library/keychains", "/library/cookies", "/library/safari",
	"/library/application support/google/chrome", "/library/application support/chromium", "/library/application support/microsoft edge",
	"/library/application support/bravesoftware", "/library/application support/arc", "/library/application support/firefox",
	"/library/application support/vivaldi", "/library/application support/com.operasoftware.opera",
	"/appdata/local/google/chrome", "/appdata/local/microsoft/edge", "/appdata/roaming/mozilla",
	"/appdata/local/chromium", "/appdata/local/bravesoftware",
	"/appdata/roaming/microsoft/credentials", "/appdata/local/microsoft/credentials", "/appdata/roaming/microsoft/protect",
	"/appdata/roaming/microsoft/crypto", "/appdata/local/microsoft/vault",
	"/snap/firefox", "/snap/chromium",
}

// browserSecretPath reports whether a file (its real, absolute path) is one
// an agent must not hand to a page. This is the one list: anything else
// that gives a local file to a page asks here. Compared in lower case: a
// Mac's disk does not tell ".SSH" from ".ssh".
func browserSecretPath(real string) bool {
	low := strings.ToLower(filepath.ToSlash(real))
	if namesAStream(low) {
		return true
	}
	for _, part := range strings.Split(low, "/") {
		if strings.HasPrefix(part, ".") && part != "." && part != ".." {
			return true
		}
	}
	for _, f := range browserSecretFolders {
		if strings.Contains(low, f+"/") {
			return true
		}
	}
	return browserSecretFile(filepath.Base(low))
}

// namesAStream: on Windows a colon after the drive names a stream inside a
// file ("key.pem::$DATA" is key.pem): never given, whatever it is called.
func namesAStream(p string) bool {
	if chromeOS != "windows" {
		return false
	}
	p = filepath.ToSlash(p)
	for _, pre := range []string{"//?/", "//./"} {
		p = strings.TrimPrefix(p, pre)
	}
	if len(p) >= 2 && p[1] == ':' {
		p = p[2:]
	}
	return strings.Contains(p, ":")
}

// browserSecretFile reports whether a file's name marks it as a secret: a
// private key, a credentials or environment file, a password store, a
// browser's own store of cookies and sign-ins.
func browserSecretFile(base string) bool {
	l := strings.ToLower(base)
	switch l {
	case ".env", ".netrc", ".npmrc", ".pypirc", ".git-credentials", "credentials", "credentials.json", "login data", "cookies", "id_rsa", "id_dsa", "id_ecdsa", "id_ed25519":
		return true
	}
	if strings.HasPrefix(l, ".env.") || strings.HasPrefix(l, "id_rsa") || strings.HasPrefix(l, "id_ed25519") || strings.HasPrefix(l, "id_ecdsa") {
		return !strings.HasSuffix(l, ".pub")
	}
	for _, ext := range []string{".pem", ".key", ".p12", ".pfx", ".keystore", ".jks", ".kdbx", ".ppk"} {
		if strings.HasSuffix(l, ext) {
			return true
		}
	}
	return false
}

// browserUploadPath checks one file an agent wants to give a page, and
// returns it absolute with links followed (the path the check was made on
// is the one the browser reads).
func browserUploadPath(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", errors.New("an empty path")
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	const refused = "%s looks like a key, a password or a sign-in file: reminal does not upload those. Ask the person to upload it."
	if namesAStream(abs) {
		return "", fmt.Errorf(refused, p)
	}
	real, err := browserRealPath(abs)
	if err != nil {
		return "", fmt.Errorf("%s: no such file", p)
	}
	st, err := os.Stat(real)
	if err != nil {
		return "", fmt.Errorf("%s: no such file", p)
	}
	if !st.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a file", p)
	}
	if st.Size() > browserUploadMaxBytes {
		return "", fmt.Errorf("%s is %d MB: at most %d MB a file", p, st.Size()>>20, browserUploadMaxBytes>>20)
	}
	if browserSecretPath(real) {
		return "", fmt.Errorf(refused, p)
	}
	return real, nil
}

// browserUploadArgs replaces args["paths"] with the checked, absolute paths.
func browserUploadArgs(args map[string]any) error {
	raw, _ := args["paths"].([]any)
	if len(raw) == 0 {
		return errors.New("paths is required: the files to give the page")
	}
	if len(raw) > browserUploadMaxFiles {
		return fmt.Errorf("at most %d files at a time", browserUploadMaxFiles)
	}
	out := make([]any, 0, len(raw))
	for _, v := range raw {
		s, _ := v.(string)
		p, err := browserUploadPath(s)
		if err != nil {
			return err
		}
		out = append(out, p)
	}
	args["paths"] = out
	return nil
}

// browserSavePDF writes a page's PDF (base64) where the agent asked, or
// under a name of its own in the working directory. Never over a file that
// is there.
func browserSavePDF(args map[string]any, b64 string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", err
	}
	name, _ := args["path"].(string)
	if strings.TrimSpace(name) == "" {
		name = "page-" + time.Now().Format("20060102-150405") + ".pdf"
	}
	if !strings.EqualFold(filepath.Ext(name), ".pdf") {
		name += ".pdf"
	}
	abs, err := filepath.Abs(name)
	if err != nil {
		return "", err
	}
	f, err := os.OpenFile(abs, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return "", fmt.Errorf("%s is already there: give another path", abs)
		}
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return fmt.Sprintf("Saved the page as a PDF: %s (%d bytes).", abs, len(data)), nil
}

// browserBatchSteps reads a batch's steps into the form they are sent in:
// each a tool's name (a string) and its arguments (an object), checked as
// that tool is on its own. What is sent on is what was checked here, never
// the caller's own JSON: the extension cannot read a step another way.
func browserBatchSteps(args map[string]any) error {
	raw, ok := args["actions"].([]any)
	if !ok || len(raw) == 0 {
		return errors.New("actions is required: [{tool, args}, ...]")
	}
	steps := make([]any, 0, len(raw))
	for i, s := range raw {
		step, ok := s.(map[string]any)
		if !ok {
			return fmt.Errorf("step %d is not {tool, args}", i+1)
		}
		name, ok := step["tool"].(string)
		if !ok || name == "" {
			return fmt.Errorf("step %d: tool must be a tool's name", i+1)
		}
		sub := map[string]any{}
		if v, has := step["args"]; has && v != nil {
			if sub, ok = v.(map[string]any); !ok {
				return fmt.Errorf("step %d: args must be an object", i+1)
			}
		}
		name = strings.TrimPrefix(name, "browser_")
		if err := browserCheckCall(name, sub); err != nil {
			return fmt.Errorf("step %d: %w", i+1, err)
		}
		steps = append(steps, map[string]any{"tool": name, "args": sub})
	}
	args["actions"] = steps
	return nil
}

// browserCheckCall is what a tool's call is held to before it leaves for
// the browser (tool without "browser_"): the files an upload names, how
// much is typed at once, a batch's steps.
func browserCheckCall(tool string, args map[string]any) error {
	switch tool {
	case "file_upload":
		return browserUploadArgs(args)
	case "type":
		if text, _ := args["text"].(string); len(text) > browserMaxType {
			return fmt.Errorf("text is %d characters: type at most %d at a time", len(text), browserMaxType)
		}
	case "batch":
		return browserBatchSteps(args)
	}
	return nil
}
