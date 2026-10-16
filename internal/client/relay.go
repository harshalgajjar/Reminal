// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"bytes"
	"compress/gzip"
	"embed"
	"fmt"
	"log"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"

	"reminal/internal/config"
	"reminal/internal/relay"
	"reminal/internal/viewerbuild"
)

// viewerCSP must stay the same as the Content-Security-Policy line in
// cloudflare/public/_headers; a test compares them. Its scripts are named
// one by one — this origin's, and each CDN script the page loads, by its
// exact versioned URL — so an injected tag can load nothing else from those
// CDNs (TestCSPNamesEveryCDNScriptThePageLoads keeps the list true).
const viewerCSP = "frame-ancestors 'none'; base-uri 'none'; object-src 'none'; form-action 'self'; script-src 'self' https://cdn.jsdelivr.net/npm/@xterm/xterm@5.5.0/lib/xterm.min.js https://cdn.jsdelivr.net/npm/@xterm/addon-search@0.15.0/lib/addon-search.min.js https://cdn.jsdelivr.net/npm/tweetnacl@1.0.3/nacl.min.js https://cdnjs.cloudflare.com/ajax/libs/qrcodejs/1.0.0/qrcode.min.js"

//go:embed web/index.html web/build.json web/app.js web/header.js web/sw.js web/manifest.webmanifest web/icons web/vendor
var webIndex embed.FS

// viewerPage is the page this binary serves, rendered once from the sources
// it embeds (internal/viewerbuild, by web/build.json): the page and its
// scripts always come from the same build, so a binary can never serve a
// page that names a script it does not have.
var viewerPage = sync.OnceValues(func() (viewerbuild.Page, error) {
	_, p, err := viewerbuild.FromFS(webIndex, "web")
	return p, err
})

func RunRelay(port string) error {
	if port == "" {
		port = config.DefaultPort
	}

	mux := relayMux()

	addr := ":" + port
	base := fmt.Sprintf("http://localhost%s", addr)
	fmt.Printf("reminal relay listening on %s\n", base)
	fmt.Printf("Local mode:  REMINAL_LOCAL=1 reminal\n")
	fmt.Printf("WebSocket:   ws://localhost%s/ws/<session>/<agent|viewer>\n", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
	return nil
}

// relayMux is the relay's routes: its sockets, and the viewer it serves.
func relayMux() *http.ServeMux {
	srv := relay.NewServer()
	mux := http.NewServeMux()

	mux.HandleFunc("GET /ws/{session}/{role}", func(w http.ResponseWriter, r *http.Request) {
		srv.HandleSessionWS(w, r, r.PathValue("session"), r.PathValue("role"))
	})
	mux.HandleFunc("/ws", srv.HandleWS)
	// `reminal copy` / `reminal paste`: the same blind pairing the Worker
	// does, so handing a file over works against a self-hosted relay too —
	// without it, copy failed there with a bare "bad handshake".
	rv := relay.NewRendezvous()
	mux.HandleFunc("GET /rv/{code}/{role}", func(w http.ResponseWriter, r *http.Request) {
		rv.HandleWS(w, r, r.PathValue("code"), r.PathValue("role"))
	})
	// The phone-alert service worker. Served so the page can register it here
	// too; subscribing still needs a relay with push keys (the hosted one), and
	// the page says so rather than failing silently.
	mux.HandleFunc("GET /sw.js", func(w http.ResponseWriter, r *http.Request) {
		data, err := webIndex.ReadFile("web/sw.js")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store, must-revalidate")
		writeEmbedded(w, r, "web/sw.js", data)
	})
	// The app manifest and its icons: what lets a browser install the viewer
	// as an app rather than a bookmark.
	mux.HandleFunc("GET /manifest.webmanifest", func(w http.ResponseWriter, r *http.Request) {
		data, err := webIndex.ReadFile("web/manifest.webmanifest")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/manifest+json")
		writeEmbedded(w, r, "web/manifest.webmanifest", data)
	})
	// Scripts the viewer loads from its own origin (the CPace handshake's
	// ristretto255). path.Base keeps a request inside the directory.
	mux.HandleFunc("GET /vendor/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := "web/vendor/" + path.Base(r.PathValue("name"))
		data, err := webIndex.ReadFile(name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		writeEmbedded(w, r, name, data)
	})
	// The page's own scripts (internal/viewerbuild): named by their content,
	// so kept for good — a page names exactly the build it was made with,
	// and a new build is a new name. As the Worker serves them (_headers).
	mux.HandleFunc("GET /assets/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := "assets/" + path.Base(r.PathValue("name"))
		page, err := viewerPage()
		if err != nil {
			http.Error(w, "web UI unavailable", http.StatusInternalServerError)
			return
		}
		data, ok := page.Assets[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		writeEmbedded(w, r, name, data)
	})
	mux.HandleFunc("GET /icons/{name}", func(w http.ResponseWriter, r *http.Request) {
		data, err := webIndex.ReadFile("web/icons/" + path.Base(r.PathValue("name")))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(data)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		page, err := viewerPage()
		if err != nil {
			http.Error(w, "web UI unavailable", http.StatusInternalServerError)
			return
		}
		data := page.HTML
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// The same protections the hosted viewer is served with
		// (cloudflare/public/_headers): not framable, no type sniffing, no
		// referrer carrying a session id, and scripts only from the places the
		// page actually loads them. No HSTS: this relay is plain http.
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", viewerCSP)
		// Dev relay: never let a browser cache the page across rebuilds —
		// the embedded HTML changes on every `go build`, and a stale cached
		// copy silently masks fixes during local testing.
		w.Header().Set("Cache-Control", "no-store, must-revalidate")
		writeEmbedded(w, r, "web/index.html", data)
	})
	return mux
}

// gzipped holds each embedded file compressed, made the first time it is
// asked for (the files never change while the relay runs).
var gzipped sync.Map // name → []byte

// writeEmbedded sends an embedded file, gzip-compressed when the browser
// takes it: the viewer page is ~0.8 MB, a few seconds on a phone's network
// uncompressed and a fifth of that compressed (the hosted viewer is served
// compressed by Cloudflare). Images are not passed here: they are compressed
// already.
func writeEmbedded(w http.ResponseWriter, r *http.Request, name string, data []byte) {
	w.Header().Add("Vary", "Accept-Encoding")
	if !acceptsGzip(r.Header.Get("Accept-Encoding")) {
		_, _ = w.Write(data)
		return
	}
	gz, ok := gzipped.Load(name)
	if !ok {
		var b bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&b, gzip.BestCompression)
		_, _ = zw.Write(data)
		_ = zw.Close()
		gz, _ = gzipped.LoadOrStore(name, b.Bytes())
	}
	w.Header().Set("Content-Encoding", "gzip")
	_, _ = w.Write(gz.([]byte))
}

// acceptsGzip says whether an Accept-Encoding header takes gzip: named, or
// "*", with a q above 0.
func acceptsGzip(h string) bool {
	for _, part := range strings.Split(h, ",") {
		enc, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		enc = strings.TrimSpace(enc)
		if !strings.EqualFold(enc, "gzip") && enc != "*" {
			continue
		}
		q := 1.0
		for _, p := range strings.Split(params, ";") {
			k, v, ok := strings.Cut(strings.TrimSpace(p), "=")
			if ok && strings.EqualFold(strings.TrimSpace(k), "q") {
				if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
					q = f
				}
			}
		}
		return q > 0
	}
	return false
}
