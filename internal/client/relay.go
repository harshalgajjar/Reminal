// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"bytes"
	"compress/gzip"
	"embed"
	"fmt"
	"io/fs"
	"log"
	"mime"
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
// CDNs (TestCSPNamesEveryCDNScriptThePageLoads keeps the list true). Every
// other kind of source is named too, from default-src 'none': only what the
// page uses (TestViewerPolicyAllowsOnlyWhatThePageUses) — its own origin for
// fetch and sockets (ws/wss included), data: and blob: images, the xterm
// stylesheet and inline styles, the tour's two fonts, its service workers,
// and blob: frames for a previewed PDF.
const viewerCSP = "default-src 'none'; frame-ancestors 'none'; base-uri 'none'; object-src 'none'; form-action 'self'; script-src 'self' https://cdn.jsdelivr.net/npm/@xterm/xterm@5.5.0/lib/xterm.min.js https://cdn.jsdelivr.net/npm/@xterm/addon-search@0.15.0/lib/addon-search.min.js https://cdn.jsdelivr.net/npm/tweetnacl@1.0.3/nacl.min.js https://cdnjs.cloudflare.com/ajax/libs/qrcodejs/1.0.0/qrcode.min.js; style-src 'unsafe-inline' https://cdn.jsdelivr.net/npm/@xterm/xterm@5.5.0/css/xterm.min.css; font-src https://cdn.jsdelivr.net/npm/geist@1.3.1/dist/fonts/geist-sans/Geist-Variable.woff2 https://cdn.jsdelivr.net/npm/geist@1.3.1/dist/fonts/geist-mono/GeistMono-Variable.woff2; img-src 'self' data: blob:; connect-src 'self'; worker-src 'self'; frame-src blob:; manifest-src 'self'"

// The page's sources and what build.json copies (a test holds every copy
// entry embedded).
//
//go:embed web/index.html web/build.json web/app.js web/moments.js web/header.js web/pick.js web/reload.js web/sw.js web/manifest.webmanifest web/icons web/vendor
var webIndex embed.FS

// viewer is the page this binary serves, rendered once from the sources it
// embeds (internal/viewerbuild, by web/build.json) when the relay's mux is
// built (relayMux): the page and its scripts always come from the same
// build, so a binary can never serve a page that names a script it does not
// have.
var viewerBuilt = sync.OnceValue(func() (b struct {
	spec viewerbuild.Spec
	page viewerbuild.Page
	err  error
}) {
	b.spec, b.page, b.err = viewerbuild.FromFS(webIndex, "web")
	return b
})

func viewer() (viewerbuild.Spec, viewerbuild.Page, error) {
	b := viewerBuilt()
	return b.spec, b.page, b.err
}

func viewerPage() (viewerbuild.Page, error) {
	_, p, err := viewer()
	return p, err
}

// copiedHeaders are the headers of a file build.json copies, beyond its type,
// as the Worker serves it (cloudflare/public/_headers adds its own).
var copiedHeaders = map[string]map[string]string{
	// The page's guard for its own scripts: at a fixed path, unhashed, so it
	// loads even when the build the page names is gone.
	"reload.js": {"Cache-Control": "no-cache"},
	// The phone-alert service worker. Served so the page can register it here
	// too; subscribing still needs a relay with push keys (the hosted one), and
	// the page says so rather than failing silently.
	"sw.js": {"Cache-Control": "no-store, must-revalidate"},
}

// copiedType is the Content-Type of a file build.json copies.
func copiedType(name string) string {
	switch path.Ext(name) {
	case ".js":
		return "text/javascript; charset=utf-8"
	case ".webmanifest":
		return "application/manifest+json"
	case ".png":
		return "image/png"
	}
	if t := mime.TypeByExtension(path.Ext(name)); t != "" {
		return t
	}
	return "application/octet-stream"
}

// serveCopied serves what build.json copies, each at its path — a file, or a
// directory's files under it — and nothing else of web.
func serveCopied(mux *http.ServeMux, s viewerbuild.Spec) {
	serve := func(w http.ResponseWriter, r *http.Request, name string) {
		if !fs.ValidPath(name) {
			http.NotFound(w, r)
			return
		}
		data, err := webIndex.ReadFile("web/" + name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		ct := copiedType(name)
		w.Header().Set("Content-Type", ct)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		for k, v := range copiedHeaders[name] {
			w.Header().Set(k, v)
		}
		if strings.HasPrefix(ct, "image/") {
			_, _ = w.Write(data) // compressed already
			return
		}
		writeEmbedded(w, r, "web/"+name, data)
	}
	for _, c := range s.Copy {
		if st, err := fs.Stat(webIndex, "web/"+c); err == nil && st.IsDir() {
			mux.HandleFunc("GET /"+c+"/{name...}", func(w http.ResponseWriter, r *http.Request) {
				serve(w, r, c+"/"+r.PathValue("name"))
			})
			continue
		}
		mux.HandleFunc("GET /"+c, func(w http.ResponseWriter, r *http.Request) { serve(w, r, c) })
	}
}

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
	// What build.json copies, each at its path. A build.json that cannot be
	// read serves nothing of it; the page says why.
	if s, _, err := viewer(); err == nil {
		serveCopied(mux, s)
	}
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
	// The page, at / alone: any other path is not found, as at the Worker.
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
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
		// A page this one opens on another origin (a forwarded app) gets no
		// handle back on it (the Worker's _headers says the same).
		h.Set("Cross-Origin-Opener-Policy", "same-origin-allow-popups")
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
