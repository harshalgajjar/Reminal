// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package updater

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// follow makes this test's binary follow a channel of the given name, with its
// own cache file, for the length of the test.
func follow(t *testing.T, name, cacheFile string) {
	t.Helper()
	old := channel
	channel = Channel{Name: name, CacheFile: cacheFile}
	t.Cleanup(func() { channel = old })
}

// fakeBuild compiles a stand-in for a downloaded build: it answers
// `version --json` as a build of channel ch would, or — with ch "" — prints a
// bare version the way a build too old to say does. If it believes it has been
// handed a live session to resume, it says so and fails, which is what asking
// a build its channel must never cause.
func fakeBuild(t *testing.T, ch string) []byte {
	t.Helper()
	if testing.Short() {
		t.Skip("compiles a program")
	}
	dir := t.TempDir()
	src := `package main

import (
	"fmt"
	"os"
)

const ch = "CHANNEL"

func main() {
	if os.Getenv("REMINAL_RESUME") != "" {
		fmt.Println("resumed a session")
		os.Exit(3)
	}
	if len(os.Args) > 2 && os.Args[1] == "version" && os.Args[2] == "--json" && ch != "" {
		fmt.Printf("{\"version\":\"9.9.9\",\"channel\":%q}\n", ch)
		return
	}
	fmt.Println("9.9.9")
}
`
	src = strings.Replace(src, "CHANNEL", ch, 1)
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module fakebuild\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "reminal")
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", out, ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build the stand-in: %v\n%s", err, b)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func tarGz(t *testing.T, files map[string][]byte) *tar.Reader {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	// A stable order, with the reminal binary last: a refused build must leave
	// the helpers that came before it uninstalled too.
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	isMain := func(n string) bool { return n == "reminal" || strings.HasSuffix(n, "/reminal") }
	sort.Slice(names, func(i, j int) bool {
		if isMain(names[i]) != isMain(names[j]) {
			return !isMain(names[i])
		}
		return names[i] < names[j]
	})
	for _, n := range names {
		if err := tw.WriteHeader(&tar.Header{Name: n, Mode: 0o755, Size: int64(len(files[n])), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(files[n]); err != nil {
			t.Fatal(err)
		}
	}
	_ = tw.Close()
	_ = gz.Close()
	zr, err := gzip.NewReader(&buf)
	if err != nil {
		t.Fatal(err)
	}
	return tar.NewReader(zr)
}

func leftovers(t *testing.T, dir, prefix string) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(dir, prefix+"*"))
	return m
}

// A downloaded build is asked which channel it follows, and one that answers
// with another channel — or cannot answer at all — is refused. It is asked in
// an environment that could not make it resume a session, even when the
// process asking is itself one.
func TestABuildIsAskedWhichChannelItFollows(t *testing.T) {
	follow(t, "alpha", "alpha.json")
	t.Setenv("REMINAL_RESUME", "1")
	dir := t.TempDir()
	write := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		if runtime.GOOS == "windows" {
			p += ".exe"
		}
		if err := os.WriteFile(p, b, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}

	if err := sameChannel(write("ours", fakeBuild(t, "alpha"))); err != nil {
		t.Fatalf("a build of this channel was refused: %v", err)
	}
	err := sameChannel(write("theirs", fakeBuild(t, "beta")))
	if err == nil || !strings.Contains(err.Error(), `"beta"`) || !strings.Contains(err.Error(), `"alpha"`) {
		t.Fatalf("a build of another channel was not refused plainly: %v", err)
	}
	if err := sameChannel(write("old", fakeBuild(t, ""))); err == nil {
		t.Fatal("a build that cannot say its channel was accepted")
	}
}

// The loose install: a refused build leaves every installed file as it was —
// the binary AND the helpers that shipped beside it — and nothing staged is
// left lying in the install directory.
func TestARefusedBuildLeavesTheInstallAlone(t *testing.T) {
	follow(t, "alpha", "alpha.json")
	dir := t.TempDir()
	bin := filepath.Join(dir, "reminal")
	if err := os.WriteFile(bin, []byte("the installed build"), 0o755); err != nil {
		t.Fatal(err)
	}

	err := installLoose(tarGz(t, map[string][]byte{
		"reminal-capture": []byte("their capture helper"),
		"reminal":         fakeBuild(t, "beta"),
	}), bin, sameChannel)
	if err == nil {
		t.Fatal("a build of another channel was installed")
	}
	if got, _ := os.ReadFile(bin); string(got) != "the installed build" {
		t.Fatal("the installed binary was replaced by a refused build")
	}
	if _, err := os.Stat(filepath.Join(dir, "reminal-capture")); err == nil {
		t.Fatal("a refused build's helper was installed")
	}
	if l := leftovers(t, dir, ".reminal.new-"); len(l) != 0 {
		t.Fatalf("staged files left behind: %v", l)
	}

	// And a build of this channel goes in, helpers and all.
	ours := fakeBuild(t, "alpha")
	if err := installLoose(tarGz(t, map[string][]byte{
		"reminal-capture": []byte("our capture helper"),
		"reminal":         ours,
	}), bin, sameChannel); err != nil {
		t.Fatalf("a build of this channel was refused: %v", err)
	}
	if got, _ := os.ReadFile(bin); !bytes.Equal(got, ours) {
		t.Fatal("the accepted build was not installed")
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "reminal-capture")); string(got) != "our capture helper" {
		t.Fatal("the accepted build's helper was not installed")
	}
	if l := leftovers(t, dir, ".reminal.new-"); len(l) != 0 {
		t.Fatalf("staged files left behind: %v", l)
	}
}

// The bundle install, the same way: a refused bundle never displaces the
// installed one.
func TestARefusedBundleLeavesTheBundleAlone(t *testing.T) {
	follow(t, "alpha", "alpha.json")
	parent := t.TempDir()
	app := filepath.Join(parent, "reminal.app")
	marker := filepath.Join(app, "Contents", "installed")
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := applyBundleChecked(tarGz(t, map[string][]byte{
		"reminal.app/Contents/MacOS/reminal": fakeBuild(t, "beta"),
	}), app, sameChannel)
	if err == nil {
		t.Fatal("a bundle of another channel was installed")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("the installed bundle was displaced by a refused one")
	}
	if l := leftovers(t, parent, ".reminal-app.new-"); len(l) != 0 {
		t.Fatalf("staging left behind: %v", l)
	}

	if err := applyBundleChecked(tarGz(t, map[string][]byte{
		"reminal.app/Contents/MacOS/reminal": fakeBuild(t, "alpha"),
	}), app, sameChannel); err != nil {
		t.Fatalf("a bundle of this channel was refused: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the accepted bundle did not replace the installed one")
	}
}

// A build whose digest does not match what its channel published is refused
// before anything reads it, and nothing of it is kept. A channel that
// publishes no digest gets the build as downloaded.
func TestFetchBuildChecksTheDigest(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	payload := []byte("a build")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(payload) }))
	defer srv.Close()
	sum := sha256.Sum256(payload)
	good := hex.EncodeToString(sum[:])

	if _, err := fetchBuild(Build{URL: srv.URL, SHA256: strings.Repeat("0", 64)}); err == nil {
		t.Fatal("a build with the wrong digest was accepted")
	}
	if l := leftovers(t, tmp, "reminal-build-"); len(l) != 0 {
		t.Fatalf("a refused download was kept: %v", l)
	}
	for _, b := range []Build{{URL: srv.URL, SHA256: good}, {URL: srv.URL, SHA256: strings.ToUpper(good)}, {URL: srv.URL}} {
		f, err := fetchBuild(b)
		if err != nil {
			t.Fatalf("%+v: %v", b, err)
		}
		got, _ := os.ReadFile(f.Name())
		discardBuild(f)
		if !bytes.Equal(got, payload) {
			t.Fatalf("%+v: got %q", b, got)
		}
	}
	if _, err := fetchBuild(Build{}); err != errNoAssetForPlatform {
		t.Fatalf("a missing build reported %v", err)
	}
}

// What one channel remembers is never another's answer — even if both were
// pointed at the same file, which is how every install shared one before
// channels existed. The file written before channels is the public releases'.
func TestCacheIsPerChannel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	follow(t, "alpha", "shared.json")
	writeCache(cacheEntry{LatestTag: "v9.0.0", AssetURL: "https://example.test/alpha"})
	if e, ok := readCache(); !ok || e.Channel != "alpha" {
		t.Fatalf("a channel cannot read its own answer: %+v %v", e, ok)
	}
	follow(t, "beta", "shared.json")
	if e, ok := readCache(); ok {
		t.Fatalf("another channel's answer was read as this one's: %+v", e)
	}

	path := filepath.Join(os.Getenv("HOME"), ".reminal", "version-check.json")
	if err := os.WriteFile(path, []byte(`{"latest_tag":"v3.14.5","asset_url":"https://example.test/public"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	old := channel
	channel = mainChannel()
	_, ok := readCache()
	channel = old
	if !ok {
		t.Fatal("the public releases lost the answer they wrote before channels")
	}
	follow(t, "beta", "version-check.json")
	if _, ok := readCache(); ok {
		t.Fatal("a pre-channel answer was taken by a channel it cannot belong to")
	}
}

func TestParseBuildChannel(t *testing.T) {
	if ch, err := parseBuildChannel([]byte(`{"version":"1.0.0","channel":"alpha"}` + "\n")); err != nil || ch != "alpha" {
		t.Fatalf("got %q, %v", ch, err)
	}
	for _, out := range []string{"", `{"version":"1.0.0"}`, "nope"} {
		if _, err := parseBuildChannel([]byte(out)); err == nil {
			t.Fatalf("%q was taken as naming a channel", out)
		}
	}
}

// A build from before builds said their channel answers with a bare version:
// a stable release, so it passes where stable releases are wanted and
// nowhere else.
func TestALegacyBuildIsAStableOne(t *testing.T) {
	got, err := parseBuildChannel([]byte("3.14.4\n"))
	if err != nil || got != legacyChannel {
		t.Fatalf("a bare version is a legacy build: %q %v", got, err)
	}
	if _, err := parseBuildChannel([]byte("reminal: command not found")); err == nil {
		t.Fatal("something that is not a version is no answer")
	}
	if !channelAccepts("stable", legacyChannel) {
		t.Fatal("stable takes a legacy build")
	}
	if channelAccepts("nightly", legacyChannel) {
		t.Fatal("another line never takes a legacy build")
	}
	if !channelAccepts("nightly", "nightly") || channelAccepts("stable", "nightly") || channelAccepts("nightly", "stable") {
		t.Fatal("otherwise a build must follow the releases wanted")
	}
}
