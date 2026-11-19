package client

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// The voice frame is served with its own policy here and by the Worker, the
// same one in both places.
func TestVoiceFramePolicyIsTheSameHostedAndHere(t *testing.T) {
	headers := string(readHosted(t, "_headers"))
	i := strings.Index(headers, "\n/voice/*\n")
	if i < 0 {
		t.Fatal("cloudflare/public/_headers has no /voice/* block")
	}
	block := headers[i+1:]
	if j := strings.Index(block[1:], "\n/"); j >= 0 {
		block = block[:j+1]
	}
	for _, want := range []string{"! Content-Security-Policy", "! X-Frame-Options", "Content-Security-Policy: " + voiceCSP + "\n", "X-Frame-Options: SAMEORIGIN"} {
		if !strings.Contains(block+"\n", want) {
			t.Errorf("the hosted /voice/* block lacks %q", want)
		}
	}
}

// Every file of the frame is served with the frame's headers (a worker gets
// its policy from its own response), and the frame may be framed only by the
// viewer itself.
func TestEveryVoiceFileHasTheFramesHeaders(t *testing.T) {
	ents, err := fs.ReadDir(webIndex, "web/voice")
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) == 0 {
		t.Fatal("web/voice is empty")
	}
	srv := httptest.NewServer(relayMux())
	defer srv.Close()
	for _, e := range ents {
		name := "voice/" + e.Name()
		if copiedHeaders[name]["Content-Security-Policy"] != voiceCSP {
			t.Errorf("%s has no voice policy in copiedHeaders", name)
		}
		resp, err := http.Get(srv.URL + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Errorf("GET /%s: %d", name, resp.StatusCode)
			continue
		}
		if got := resp.Header.Get("Content-Security-Policy"); got != voiceCSP {
			t.Errorf("/%s policy %q", name, got)
		}
		if got := resp.Header.Get("X-Frame-Options"); got != "SAMEORIGIN" {
			t.Errorf("/%s X-Frame-Options %q", name, got)
		}
	}
	if !strings.Contains(voiceCSP, "frame-ancestors 'self'") {
		t.Error("the voice frame can be framed by other sites")
	}
}

// The page that holds the session's keys gains only the right to frame its
// own origin; WebAssembly, the CDNs and the model hosts stay the frame's.
func TestTheViewerPageItselfStaysStrict(t *testing.T) {
	for _, no := range []string{"wasm-unsafe-eval", "huggingface", "hf.co", "kokoro", "onnxruntime"} {
		if strings.Contains(viewerCSP, no) {
			t.Errorf("the viewer's own policy allows %q", no)
		}
	}
}

// No emoji in the UI: the voice button is a drawn icon, and the frame's page
// has no emoji either.
func TestVoiceUIHasNoEmoji(t *testing.T) {
	page, err := webIndex.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	s := string(page)
	i := strings.Index(s, `<button id="voice-btn"`)
	if i < 0 {
		t.Fatal("no voice button")
	}
	btn := s[i : i+strings.Index(s[i:], "</button>")]
	if !strings.Contains(btn, "<svg") {
		t.Error("the voice button is not a drawn icon")
	}
	frame, err := webIndex.ReadFile("web/voice/voice.html")
	if err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{"the voice button": btn, "web/voice/voice.html": string(frame)} {
		for _, r := range text {
			if r >= 0x1F000 || (r >= 0x2600 && r <= 0x26FF) {
				t.Errorf("%s has an emoji, %q", name, r)
				break
			}
		}
	}
}

var espeak = regexp.MustCompile(`\bespeak`)

// The voice makes its phonemes itself (g2p.js): nothing under web/voice loads
// kokoro-js or phonemizer, and no eSpeak (GPL) reaches the page, nor may the
// frame's policy let either library load.
func TestVoiceLoadsNoESpeak(t *testing.T) {
	bad := []string{"kokoro-js@", "npm/kokoro-js", "'kokoro-js'", `"kokoro-js"`, "phonemizer@", "npm/phonemizer", "'phonemizer'", `"phonemizer"`}
	n := 0
	err := fs.WalkDir(webIndex, "web/voice", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		n++
		b, err := webIndex.ReadFile(p)
		if err != nil {
			return err
		}
		s := strings.ToLower(string(b))
		if espeak.MatchString(s) {
			t.Errorf("%s mentions eSpeak", p)
		}
		for _, w := range bad {
			if strings.Contains(s, w) {
				t.Errorf("%s mentions %s", p, w)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("web/voice is empty")
	}
	for _, w := range []string{"kokoro-js", "phonemizer", "espeak"} {
		if strings.Contains(strings.ToLower(voiceCSP), w) {
			t.Errorf("the voice policy still allows %s", w)
		}
	}
}
