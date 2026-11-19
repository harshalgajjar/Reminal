package client

import (
	"errors"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"

	"reminal/internal/config"
)

// The voice panel's libraries and models are served by us, under
// /voice-assets/, at fixed versions (voice/assets/manifest.json lists them
// with their SHA-256s). The hosted viewer's Worker serves them from storage;
// the viewer this binary serves (a self-hosted relay, an exposed port) passes
// the same paths on to it, so the panel loads from its own origin either way
// and its policy names no other host.

// voiceAssetsUpstream is where /voice-assets/ comes from here:
// REMINAL_VOICE_ASSETS, or the hosted viewer's (not REMINAL_WEB: a self-hosted
// relay is often that very server, and has no mirror of its own).
func voiceAssetsUpstream() string {
	if u := strings.TrimSpace(os.Getenv("REMINAL_VOICE_ASSETS")); u != "" {
		return strings.TrimSuffix(u, "/") + "/"
	}
	return config.DefaultCloudWeb + "/voice-assets/"
}

// A path as the mirror lays them out (voice/assets/mirror.mjs; the hosted
// Worker, cloudflare/src/voiceassets.ts, serves the same): npm/<pkg>@<version>/... or
// hf/<owner>/<repo>/<commit>/...; no "..", no empty segment, nothing else.
var voiceAssetPath = regexp.MustCompile(`^(npm/(@[a-z0-9-]+/)?[a-z0-9.-]+@[0-9][0-9A-Za-z.+-]*|hf/[A-Za-z0-9._-]+/[A-Za-z0-9._-]+/[0-9a-f]{40})(/[A-Za-z0-9._-]+)+$`)

// The headers passed back: what a range read and a cache need, nothing that
// would set cookies or policy on this origin.
var voiceAssetHeaders = []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "ETag", "Last-Modified", "Cache-Control"}

// errVoiceAssetRedirect: the upstream answered with a redirect. A mirrored
// file is served where its path says, never from wherever the upstream
// points: a redirect is an upstream that is not ours (or not set up), so it
// is not followed.
var errVoiceAssetRedirect = errors.New("voice assets upstream redirected")

// voiceAssetsClient fetches from the upstream: no overall timeout, as a model
// is hundreds of MB and the viewer's request ends it; no redirects.
func voiceAssetsClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return errVoiceAssetRedirect },
	}
}

// voiceAssetsUnavailable is the answer when the upstream cannot be reached
// (or redirected): 502, short and plain, never cached.
func voiceAssetsUnavailable(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, "voice assets unavailable", http.StatusBadGateway)
}

func serveVoiceAssets(mux *http.ServeMux) {
	upstream := voiceAssetsUpstream()
	client := voiceAssetsClient()
	mux.HandleFunc("/voice-assets/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p := strings.TrimPrefix(r.URL.Path, "/voice-assets/")
		// The pattern lets "." through inside names, so ".." is refused here.
		if strings.Contains(p, "..") || !voiceAssetPath.MatchString(p) {
			http.NotFound(w, r)
			return
		}
		req, err := http.NewRequestWithContext(r.Context(), r.Method, upstream+p, nil)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		for _, h := range []string{"Range", "If-None-Match", "If-Modified-Since"} {
			if v := r.Header.Get(h); v != "" {
				req.Header.Set(h, v)
			}
		}
		resp, err := client.Do(req)
		if err != nil {
			voiceAssetsUnavailable(w)
			return
		}
		defer resp.Body.Close()
		for _, h := range voiceAssetHeaders {
			if v := resp.Header.Get(h); v != "" {
				w.Header().Set(h, v)
			}
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(resp.StatusCode)
		if r.Method == http.MethodGet {
			_, _ = io.Copy(w, resp.Body)
		}
	})
}
