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

const hostedPublic = "../../cloudflare/public"

func readHosted(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(hostedPublic, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The viewer holds this browser's keys, so a script it loads from somewhere
// else must be exactly the file that was reviewed. A tag without an integrity
// hash runs whatever that host serves on the day.
func TestEveryExternalScriptAndStylesheetIsPinned(t *testing.T) {
	page := viewerText(t, hostedPublic)
	tags := regexp.MustCompile(`<(script|link)\b[^>]*\b(?:src|href)="https://[^"]+"[^>]*>`).FindAllString(page, -1)
	if len(tags) == 0 {
		t.Fatal("found no external tags at all — the pattern has stopped matching the page")
	}
	for _, tag := range tags {
		if strings.HasPrefix(tag, "<link") && !strings.Contains(tag, `rel="stylesheet"`) {
			continue
		}
		if !strings.Contains(tag, `integrity="sha384-`) || !strings.Contains(tag, `crossorigin="anonymous"`) {
			t.Errorf("loaded without an integrity hash: %s", tag)
		}
	}
	// Scripts added at runtime have to carry one too.
	dyn := regexp.MustCompile(`s\.src = (\w+);([^\n]*)`).FindAllStringSubmatch(page, -1)
	if len(dyn) == 0 {
		t.Fatal("found no runtime-loaded scripts — the pattern has stopped matching the page")
	}
	for _, m := range dyn {
		if !strings.Contains(m[2], "s.integrity = ") {
			t.Errorf("runtime script %s is loaded without an integrity hash", m[1])
		}
	}
}

// What the page says the handshake library hashes to must be what the file
// actually hashes to — otherwise the browser refuses to load it and nobody
// can connect with a PIN. One file: the Worker's copy is built from it.
func TestVendoredRistrettoMatchesItsPinnedHash(t *testing.T) {
	page := viewerText(t, hostedPublic)
	m := regexp.MustCompile(`RISTRETTO_SRI = 'sha384-([^']+)'`).FindStringSubmatch(page)
	if m == nil {
		t.Fatal("RISTRETTO_SRI not found in the page")
	}
	lib, err := os.ReadFile("web/vendor/ristretto255.js")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha512.Sum384(lib)
	if got := base64.StdEncoding.EncodeToString(sum[:]); got != m[1] {
		t.Fatalf("vendor/ristretto255.js hashes to sha384-%s, the page pins sha384-%s", got, m[1])
	}
}

// The hosted viewer and the one this binary serves must be protected the same
// way, and every place the page loads a script from must be one the policy
// allows — or the page breaks the day the policy is enforced.
func TestViewerHeaders(t *testing.T) {
	headers := string(readHosted(t, "_headers"))
	for _, want := range []string{
		"X-Content-Type-Options: nosniff",
		"X-Frame-Options: DENY",
		"Referrer-Policy: no-referrer",
		"Strict-Transport-Security: max-age=",
		"Content-Security-Policy: " + viewerCSP,
	} {
		if !strings.Contains(headers, want) {
			t.Errorf("cloudflare/public/_headers is missing %q", want)
		}
	}
	page := viewerText(t, hostedPublic)
	for _, m := range regexp.MustCompile(`<script\b[^>]*\bsrc="(https://[^/"]+)`).FindAllStringSubmatch(page, -1) {
		if !strings.Contains(viewerCSP, m[1]) {
			t.Errorf("the page loads a script from %s, which the policy does not allow", m[1])
		}
	}
	for _, m := range regexp.MustCompile(`_URL = '(https://[^/']+)`).FindAllStringSubmatch(page, -1) {
		if !strings.Contains(viewerCSP, m[1]) {
			t.Errorf("the page loads a script from %s at runtime, which the policy does not allow", m[1])
		}
	}
}
