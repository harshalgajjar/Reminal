// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"bytes"
	"compress/gzip"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The relay's viewer page goes compressed to a browser that takes gzip, and
// as it is to one that does not — the same bytes either way, and its
// protections (CSP) on both.
func TestTheRelayServesItsViewerCompressed(t *testing.T) {
	srv := httptest.NewServer(relayMux())
	defer srv.Close()
	page, err := viewerPage()
	if err != nil {
		t.Fatal(err)
	}
	want := page.HTML
	get := func(ae string) *http.Response {
		req, _ := http.NewRequest("GET", srv.URL+"/", nil)
		if ae != "" {
			req.Header.Set("Accept-Encoding", ae)
		}
		// Go's client would ask for gzip and undo it by itself: asked by hand.
		resp, err := (&http.Transport{DisableCompression: true}).RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	resp := get("br, gzip")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("Accept-Encoding gzip: Content-Encoding %q", resp.Header.Get("Content-Encoding"))
	}
	zr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(zr)
	if !bytes.Equal(got, want) {
		t.Fatalf("unpacked page differs from the embedded one (%d vs %d bytes)", len(got), len(want))
	}
	if len(body)*3 > len(want) {
		t.Errorf("compressed to %d of %d bytes: less than 3x", len(body), len(want))
	}
	if resp.Header.Get("Content-Security-Policy") != viewerCSP || resp.Header.Get("Vary") != "Accept-Encoding" {
		t.Errorf("headers: CSP %q, Vary %q", resp.Header.Get("Content-Security-Policy"), resp.Header.Get("Vary"))
	}
	t.Logf("viewer page: %d bytes, %d compressed", len(want), len(body))

	for _, ae := range []string{"", "br", "gzip;q=0", "identity"} {
		resp := get(ae)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.Header.Get("Content-Encoding") != "" || !bytes.Equal(body, want) {
			t.Errorf("Accept-Encoding %q: Content-Encoding %q, %d bytes (want the page as it is)", ae, resp.Header.Get("Content-Encoding"), len(body))
		}
	}
}

func TestAcceptsGzip(t *testing.T) {
	for h, want := range map[string]bool{
		"gzip": true, "br, gzip": true, "GZIP": true, "gzip;q=0.5": true, "*": true, "gzip; q=1.0": true,
		"": false, "br": false, "gzip;q=0": false, "gzip; q=0.0": false, "identity": false, "x-gzip2": false,
	} {
		if got := acceptsGzip(h); got != want {
			t.Errorf("acceptsGzip(%q) = %v, want %v", h, got, want)
		}
	}
}

// The page's guard for its own scripts is served at its fixed path, as the
// script it is: it must load even when the build the page names is gone.
func TestTheRelayServesThePagesScriptGuard(t *testing.T) {
	srv := httptest.NewServer(relayMux())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/reload.js")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	want, _ := webIndex.ReadFile("web/reload.js")
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/javascript") || string(body) != string(want) {
		t.Fatalf("/reload.js: %d %q, %d bytes", resp.StatusCode, resp.Header.Get("Content-Type"), len(body))
	}
	page, _ := viewerPage()
	if !strings.Contains(string(page.HTML), `<script src="/reload.js"></script>`) {
		t.Fatal("the page does not load its script guard")
	}
}

// The binary serves what build.json copies, each file at its path as it is
// embedded — so a copy entry that is not embedded fails here, not in a
// browser — and, like the Worker, nothing at a path it does not have.
func TestTheRelayServesWhatBuildJSONCopies(t *testing.T) {
	srv := httptest.NewServer(relayMux())
	defer srv.Close()
	s, _, err := viewer()
	if err != nil {
		t.Fatal(err)
	}
	get := func(p string) (*http.Response, []byte) {
		t.Helper()
		resp, err := http.Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, body
	}
	served := 0
	for _, c := range s.Copy {
		err := fs.WalkDir(webIndex, "web/"+c, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			want, _ := webIndex.ReadFile(p)
			rel := strings.TrimPrefix(p, "web/")
			resp, body := get("/" + rel)
			// HTML only where the file is a page (the voice frame): anywhere
			// else it is the viewer page served for a path the binary lacks.
			isHTML := strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html")
			if resp.StatusCode != http.StatusOK || !bytes.Equal(body, want) || isHTML != strings.HasSuffix(rel, ".html") {
				t.Errorf("/%s: %d %q, %d bytes (want its %d)", rel, resp.StatusCode, resp.Header.Get("Content-Type"), len(body), len(want))
			}
			served++
			return nil
		})
		if err != nil {
			t.Errorf("build.json copies %s, which this binary does not embed: %v", c, err)
		}
	}
	if served < len(s.Copy) {
		t.Fatalf("served %d files for %d copy entries", served, len(s.Copy))
	}
	for _, p := range []string{"/robots.txt", "/app.js", "/header.js", "/build.json", "/index.html", "/vendor/../build.json", "/s/ABC"} {
		if resp, _ := get(p); resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: %d %q, want not found", p, resp.StatusCode, resp.Header.Get("Content-Type"))
		}
	}
	if resp, _ := get("/?s=ABC"); resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("/?s=ABC: %d", resp.StatusCode)
	}
}
