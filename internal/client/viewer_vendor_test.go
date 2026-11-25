// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"crypto/sha512"
	"encoding/base64"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// vendored is the third-party code the page runs, served from the page's own
// origin (web/vendor): each file at the version in its name, with the hash
// its published file has. A new version is a new name and a new line here, on
// purpose; a file changed in place fails.
var vendored = map[string]string{
	"xterm-5.5.0.min.js":               "sha384-J4qzUjBl1FxyLsl/kQPQIOeINsmp17OHYXDOMpMxlKX53ZfYsL+aWHpgArvOuof9",
	"xterm-5.5.0.min.css":              "sha384-tStR1zLfWgsiXCF3IgfB3lBa8KmBe/lG287CL9WCeKgQYcp1bjb4/+mwN6oti4Co",
	"xterm-addon-search-0.15.0.min.js": "sha384-rZV6rh0SoMasxn3Q4xrTEgBKArJ47qXr2bxeT0S4mf6sqQCkvv99FnFKawCQUqUR",
	"qrcode-1.0.0.min.js":              "sha384-3zSEDfvllQohrq0PHL1fOXJuC/jSOO34H46t6UQfobFOmxE5BpjjaIJY5F2/bMnU",
}

// The page's terminal, its search and the QR drawing are another project's
// code. They are shipped with reminal and served with the page, not fetched
// from a third party as the page loads: a request to one that failed left
// the page's own script running without them, and the page dead. Each file
// is the published one (its hash), the page names each by that hash, and it
// loads no script or stylesheet from anywhere but its own origin.
func TestThePagesLibrariesAreItsOwnFilesAtPinnedHashes(t *testing.T) {
	page, err := os.ReadFile(filepath.Join("web", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	notice, _ := os.ReadFile(filepath.Join("web", "vendor", "NOTICE"))
	for name, want := range vendored {
		b, err := os.ReadFile(filepath.Join("web", "vendor", name))
		if err != nil {
			t.Fatalf("%s is not shipped: %v", name, err)
		}
		sum := sha512.Sum384(b)
		if got := "sha384-" + base64.StdEncoding.EncodeToString(sum[:]); got != want {
			t.Errorf("%s: its content is %s, not the pinned %s", name, got, want)
		}
		tag := `="/vendor/` + name + `" integrity="` + want + `"`
		if strings.Count(string(page), tag) != 1 {
			t.Errorf("the page does not load /vendor/%s once, by its pinned hash", name)
		}
		if !strings.Contains(string(notice), name) {
			t.Errorf("web/vendor/NOTICE does not name %s and its licence", name)
		}
	}
	// Every file in the folder is one this test knows (or the notice, or the
	// handshake library, which the page's script loads by its own check).
	entries, err := os.ReadDir(filepath.Join("web", "vendor"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if _, ok := vendored[e.Name()]; !ok && e.Name() != "NOTICE" && e.Name() != "ristretto255.js" {
			t.Errorf("web/vendor/%s is shipped with no pinned hash", e.Name())
		}
	}
	outside := regexp.MustCompile(`<(?:script|link)\b[^>]*\b(?:src|href)="(?:https?:)?//[^"]*"`)
	if m := outside.FindAll(page, -1); len(m) != 0 {
		t.Errorf("the page loads a script or a stylesheet from outside its origin: %s", m)
	}
}
