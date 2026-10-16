// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
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
