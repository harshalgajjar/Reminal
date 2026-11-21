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

// browserSecretDirs are folders whose files are never uploaded: keys,
// tokens and sign-ins live there.
var browserSecretDirs = map[string]bool{
	".ssh": true, ".gnupg": true, ".aws": true, ".azure": true, ".kube": true, ".docker": true,
	".reminal": true, ".password-store": true, "Keychains": true, ".config/gcloud": true,
}

// (Any file whose name starts with a dot is refused too: settings and
// tokens, not what a person uploads to a page.)
//
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
	real, err := filepath.EvalSymlinks(abs)
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
	const refused = "%s looks like a key, a password or a sign-in file: reminal does not upload those. Ask the person to upload it."
	if browserSecretFile(filepath.Base(real)) || strings.HasPrefix(filepath.Base(real), ".") {
		return "", fmt.Errorf(refused, p)
	}
	parts := strings.Split(filepath.ToSlash(filepath.Dir(real)), "/")
	for i, part := range parts {
		if browserSecretDirs[part] || (i > 0 && browserSecretDirs[parts[i-1]+"/"+part]) {
			return "", fmt.Errorf(refused, p)
		}
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
