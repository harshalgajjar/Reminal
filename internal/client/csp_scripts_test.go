// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The CSP names each CDN script by its exact URL, not the CDN: every script
// URL the page and its program load from a CDN is in it, and nothing else is.
func TestCSPNamesEveryCDNScriptThePageLoads(t *testing.T) {
	var used []string
	seen := map[string]bool{}
	scriptURL := regexp.MustCompile(`https://(?:cdn\.jsdelivr\.net|cdnjs\.cloudflare\.com)/[^"'\s)]+\.js`)
	for _, f := range []string{"web/index.html", "web/app.js", "web/header.js"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, u := range scriptURL.FindAllString(string(b), -1) {
			if !seen[u] {
				seen[u] = true
				used = append(used, u)
			}
		}
	}
	var named []string
	for _, f := range cspDirectives(t, viewerCSP)["script-src"] {
		if strings.HasPrefix(f, "https://") {
			named = append(named, f)
		}
	}
	sort.Strings(used)
	sort.Strings(named)
	if strings.Join(used, " ") != strings.Join(named, " ") {
		t.Fatalf("the CSP's scripts and the page's differ:\n page: %v\n CSP:  %v", used, named)
	}
	if strings.Contains(viewerCSP, "https://cdn.jsdelivr.net ") || strings.Contains(viewerCSP, "https://cdnjs.cloudflare.com ") {
		t.Fatal("the CSP allows a whole CDN")
	}
}

// viewerText is the viewer as a browser runs it: the page as served, then
// each script it loads from its own assets/, in the page's order. What the
// static checks look for in the page's code is in those scripts, not the
// page. It is the page both the binary and the Worker serve: the binary
// renders it from what it embeds, and the Worker's build is held to the same
// bytes (cloudflare/viewer_build_test.go).
func viewerText(t *testing.T, _ string) string {
	t.Helper()
	page, err := viewerPage()
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.Write(page.HTML)
	srcs := regexp.MustCompile(`<script src="/(assets/[^"/]+\.js)"`).FindAllStringSubmatch(string(page.HTML), -1)
	if len(srcs) == 0 {
		t.Fatal("the page loads no script of its own from assets/")
	}
	for _, m := range srcs {
		js, ok := page.Assets[m[1]]
		if !ok {
			t.Fatalf("the page names %s, which it does not have", m[1])
		}
		b.WriteString("\n")
		b.Write(js)
	}
	return b.String()
}

// The CSP names each CDN stylesheet and font by its exact URL: every one the
// page loads is in style-src or font-src, and nothing else from a CDN is.
// The page loads no stylesheet from a CDN any more (the terminal's is its
// own file, web/vendor), so style-src names none; the tour's fonts still
// come from one.
func TestCSPNamesEveryCDNStylesheetAndFontThePageLoads(t *testing.T) {
	b, err := os.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(b)
	for _, c := range []struct {
		directive string
		used      *regexp.Regexp
	}{
		{"style-src", regexp.MustCompile(`<link rel="stylesheet" href="(https://[^"]+)"`)},
		{"font-src", regexp.MustCompile(`url\((https://[^)]+\.woff2)\)`)},
	} {
		var used []string
		for _, m := range c.used.FindAllStringSubmatch(page, -1) {
			used = append(used, m[1])
		}
		if len(used) == 0 && c.directive == "font-src" {
			t.Fatalf("found nothing for %s in the page — the pattern has stopped matching it", c.directive)
		}
		var named []string
		for _, s := range cspDirectives(t, viewerCSP)[c.directive] {
			if strings.HasPrefix(s, "https://") {
				named = append(named, s)
			}
		}
		sort.Strings(used)
		sort.Strings(named)
		if strings.Join(used, " ") != strings.Join(named, " ") {
			t.Errorf("%s and the page differ:\n page: %v\n CSP:  %v", c.directive, used, named)
		}
	}
}
