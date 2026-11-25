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

// vendored is the third-party code and fonts the page uses, served from the
// page's own origin (web/vendor): each file at the version in its name, with
// the hash its published file has, and how the page (or its script) names
// it. A new version is a new name and a new line here, on purpose; a file
// changed in place fails.
var vendored = []struct{ name, hash, in, as string }{
	{"xterm-5.5.0.min.js", "sha384-J4qzUjBl1FxyLsl/kQPQIOeINsmp17OHYXDOMpMxlKX53ZfYsL+aWHpgArvOuof9", "index.html", `<script src="/vendor/xterm-5.5.0.min.js" integrity="{hash}"></script>`},
	{"xterm-5.5.0.min.css", "sha384-tStR1zLfWgsiXCF3IgfB3lBa8KmBe/lG287CL9WCeKgQYcp1bjb4/+mwN6oti4Co", "index.html", `<link rel="stylesheet" href="/vendor/xterm-5.5.0.min.css" integrity="{hash}">`},
	{"xterm-addon-search-0.15.0.min.js", "sha384-rZV6rh0SoMasxn3Q4xrTEgBKArJ47qXr2bxeT0S4mf6sqQCkvv99FnFKawCQUqUR", "index.html", `<script src="/vendor/xterm-addon-search-0.15.0.min.js" integrity="{hash}"></script>`},
	{"qrcode-1.0.0.min.js", "sha384-3zSEDfvllQohrq0PHL1fOXJuC/jSOO34H46t6UQfobFOmxE5BpjjaIJY5F2/bMnU", "index.html", `<script src="/vendor/qrcode-1.0.0.min.js" integrity="{hash}"></script>`},
	// Loaded by the page's script, only in a browser without X25519 of its own, by this hash.
	{"tweetnacl-1.0.3.min.js", "sha384-LMUiUHpaYNGZFzWFRjsADnCSqae1Mk5llcUOHOLDhCxkyF2cdsWAueTZAzV+swW/", "app.js", "const NACL_URL = '/vendor/tweetnacl-1.0.3.min.js';\n    const NACL_SRI = '{hash}';"},
	// The welcome tour's fonts: a stylesheet's url() carries no hash, so this is where theirs is held.
	{"geist-1.3.1-sans-variable.woff2", "sha384-uW1Noh5mZZgvALaFJKc7ByimdIxIHO0ZHwNVcyC7seVa1/r2wOPOC7ln8tvE9p5U", "index.html", `url(/vendor/geist-1.3.1-sans-variable.woff2)`},
	{"geist-1.3.1-mono-variable.woff2", "sha384-WFJgu49wo/lYvujAZTcYIDepm2FCsHfyGgIlKdPj/KqiyVhpyAh23X7ZEC6Jggfw", "index.html", `url(/vendor/geist-1.3.1-mono-variable.woff2)`},
}

// The page's terminal, its search, the QR drawing, a fallback for a browser
// without X25519 and the tour's fonts are other projects' work. They are
// shipped with reminal and served with the page, not fetched from a third
// party as the page loads: a request to one that failed left the page's own
// script running without the terminal, and the page dead. Each file is the
// published one (its hash), and the page names each as it is listed here.
func TestThePagesLibrariesAreItsOwnFilesAtPinnedHashes(t *testing.T) {
	notice, _ := os.ReadFile(filepath.Join("web", "vendor", "NOTICE"))
	known := map[string]bool{"NOTICE": true, "ristretto255.js": true} // the handshake library: the page's script holds its hash
	for _, v := range vendored {
		known[v.name] = true
		b, err := os.ReadFile(filepath.Join("web", "vendor", v.name))
		if err != nil {
			t.Fatalf("%s is not shipped: %v", v.name, err)
		}
		sum := sha512.Sum384(b)
		if got := "sha384-" + base64.StdEncoding.EncodeToString(sum[:]); got != v.hash {
			t.Errorf("%s: its content is %s, not the pinned %s", v.name, got, v.hash)
		}
		src, err := os.ReadFile(filepath.Join("web", v.in))
		if err != nil {
			t.Fatal(err)
		}
		if as := strings.ReplaceAll(v.as, "{hash}", v.hash); strings.Count(string(src), as) != 1 {
			t.Errorf("web/%s does not name %s once, as %s", v.in, v.name, as)
		}
		if !strings.Contains(string(notice), v.name) {
			t.Errorf("web/vendor/NOTICE does not name %s and its licence", v.name)
		}
	}
	entries, err := os.ReadDir(filepath.Join("web", "vendor"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !known[e.Name()] {
			t.Errorf("web/vendor/%s is shipped with no pinned hash", e.Name())
		}
	}
}

// A viewer's browser asks nothing of a third party to show the page: no
// script, stylesheet, font or frame of it is at another origin, its policy
// allows none, and no file of the viewer's names a public CDN.
func TestThePageLoadsNothingFromOutsideItsOrigin(t *testing.T) {
	if strings.Contains(viewerCSP, "http:") || strings.Contains(viewerCSP, "https:") {
		t.Errorf("the page's policy allows another origin: %s", viewerCSP)
	}
	page, err := os.ReadFile(filepath.Join("web", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	loads := regexp.MustCompile(`<(?:script|link|iframe|img|audio|video|source)\b[^>]*\b(?:src|href)="(?:https?:)?//[^"]*"|url\(\s*["']?(?:https?:)?//[^)]*\)|@import\s+["'](?:https?:)?//`)
	for _, m := range loads.FindAll(page, -1) {
		if s := string(m); !strings.Contains(s, `rel="noopener`) {
			t.Errorf("the page loads from outside its origin: %s", s)
		}
	}
	cdn := regexp.MustCompile(`(?i)\b(?:cdn\.jsdelivr\.net|cdnjs\.cloudflare\.com|unpkg\.com|fonts\.googleapis\.com|fonts\.gstatic\.com)\b`)
	for _, f := range []string{"index.html", "app.js", "header.js", "pick.js", "moments.js", "voiceui.js", "reload.js", "sw.js"} {
		b, err := os.ReadFile(filepath.Join("web", f))
		if err != nil {
			t.Fatal(err)
		}
		if m := cdn.Find(b); m != nil {
			t.Errorf("web/%s names a public CDN (%s)", f, m)
		}
	}
}
