package client

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The voice frame checks every file from /voice-assets/ against hashes.js,
// which ships inside reminal: it must be exactly the mirror's manifest, or
// a file the mirror holds is refused (or, worse, one it should not hold is
// let through).
func TestVoiceHashesAreTheManifest(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "voice", "assets", "manifest.json"))
	if err != nil {
		t.Skip("no manifest in this tree:", err)
	}
	var manifest []struct {
		Path   string `json:"path"`
		Size   int64  `json:"size"`
		SHA256 string `json:"sha256"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	js, err := os.ReadFile(filepath.Join("web", "voice", "hashes.js"))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, m := range regexp.MustCompile(`(?m)^\s*"([^"]+)": \['([0-9a-f]{64})', ([0-9]+)\],$`).FindAllStringSubmatch(string(js), -1) {
		got[m[1]] = m[2] + " " + m[3]
	}
	if len(got) != len(manifest) {
		t.Errorf("hashes.js has %d files, the manifest %d", len(got), len(manifest))
	}
	for _, f := range manifest {
		if want := fmt.Sprintf("%s %d", f.SHA256, f.Size); got[f.Path] != want {
			t.Errorf("%s: hashes.js has %q, the manifest %q", f.Path, got[f.Path], want)
		}
	}
}

// Nothing in the frame loads from /voice-assets/ except through integrity.js:
// no static import, no <script src>, no worker or runtime path that skips it.
// Only assets.js (which names the files) and integrity.js may say the path.
func TestVoiceLoadsAssetsOnlyThroughIntegrity(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("web", "voice", "*"))
	for _, f := range files {
		base := filepath.Base(f)
		if base == "assets.js" || base == "integrity.js" || base == "hashes.js" || !(strings.HasSuffix(base, ".js") || strings.HasSuffix(base, ".html")) {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if regexp.MustCompile("['\"`]/voice-assets/").Match(b) {
			t.Errorf("%s names /voice-assets/ itself: load it through integrity.js", base)
		}
	}
}
