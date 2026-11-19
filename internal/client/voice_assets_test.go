package client

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// /voice-assets/ passes a mirrored file on from the hosted viewer, a range of
// it as a range, and nothing else: no other path, no other method, none of the
// upstream's own headers beyond what a range read and a cache need.
func TestVoiceAssetsPassOnOnlyMirroredFiles(t *testing.T) {
	const file = "npm/onnxruntime-web@1.22.0/dist/ort-wasm-simd-threaded.wasm"
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/voice-assets/"+file {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/wasm")
		w.Header().Set("Set-Cookie", "x=1")
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		if r.Header.Get("Range") == "bytes=0-3" {
			w.Header().Set("Content-Range", "bytes 0-3/10")
			w.WriteHeader(http.StatusPartialContent)
			io.WriteString(w, "0123")
			return
		}
		io.WriteString(w, "0123456789")
	}))
	defer up.Close()
	t.Setenv("REMINAL_VOICE_ASSETS", up.URL+"/voice-assets")
	srv := httptest.NewServer(relayMux())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/voice-assets/" + file)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "0123456789" || resp.Header.Get("Content-Type") != "application/wasm" {
		t.Fatalf("whole file: %d %q %q", resp.StatusCode, body, resp.Header.Get("Content-Type"))
	}
	if resp.Header.Get("Set-Cookie") != "" {
		t.Error("an upstream cookie was passed on")
	}

	req, _ := http.NewRequest("GET", srv.URL+"/voice-assets/"+file, nil)
	req.Header.Set("Range", "bytes=0-3")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 206 || string(body) != "0123" || resp.Header.Get("Content-Range") != "bytes 0-3/10" {
		t.Fatalf("range: %d %q %q", resp.StatusCode, body, resp.Header.Get("Content-Range"))
	}

	for _, bad := range []string{
		"etc/passwd",
		"npm/../../secret",
		"npm/onnxruntime-web/dist/x.wasm", // no version
		"hf/onnx-community/Kokoro-82M-v1.0-ONNX/main/config.json", // a branch, not a commit
	} {
		resp, err := http.Get(srv.URL + "/voice-assets/" + bad)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", bad, resp.StatusCode)
		}
	}

	resp, err = http.Post(srv.URL+"/voice-assets/"+file, "text/plain", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d, want 405", resp.StatusCode)
	}
}

// The voice frame loads everything from its own origin: its policy names no
// other host.
func TestVoiceFrameNamesNoOtherHost(t *testing.T) {
	for _, no := range []string{"http:", "https:", "jsdelivr", "huggingface", "hf.co"} {
		if strings.Contains(voiceCSP, no) {
			t.Errorf("voiceCSP allows %q", no)
		}
	}
}

// An upstream that redirects is not followed: the file is served from where
// its path says or not at all (502), and the place it pointed to is never
// asked. One that cannot be reached is a short plain 502, never cached.
func TestVoiceAssetsRefuseRedirectsAndSayWhenUnreachable(t *testing.T) {
	const file = "npm/onnxruntime-web@1.22.0/dist/ort-wasm-simd-threaded.wasm"
	asked := 0
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked++
		io.WriteString(w, "not ours")
	}))
	defer elsewhere.Close()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/x.wasm", http.StatusFound)
	}))
	defer up.Close()

	get := func() (*http.Response, string) {
		t.Helper()
		srv := httptest.NewServer(relayMux())
		defer srv.Close()
		resp, err := http.Get(srv.URL + "/voice-assets/" + file)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(body)
	}

	t.Setenv("REMINAL_VOICE_ASSETS", up.URL+"/voice-assets")
	resp, body := get()
	if resp.StatusCode != http.StatusBadGateway || asked != 0 || strings.Contains(body, "not ours") {
		t.Fatalf("redirect: %d %q, the other server asked %d times", resp.StatusCode, body, asked)
	}

	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	t.Setenv("REMINAL_VOICE_ASSETS", deadURL+"/voice-assets")
	resp, body = get()
	if resp.StatusCode != http.StatusBadGateway || strings.TrimSpace(body) != "voice assets unavailable" ||
		!strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("unreachable: %d %q %q %q", resp.StatusCode, body, resp.Header.Get("Content-Type"), resp.Header.Get("Cache-Control"))
	}
}
