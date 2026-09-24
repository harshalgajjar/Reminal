// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package updater

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// A channel is one line of releases.
//
// A build follows exactly one, fixed when it is built, and nothing it
// downloads can move it to another. Everything the updater learns — the
// newest release, where its build lives, what forces an upgrade, what this
// machine last saw — comes from that one channel and is kept apart from any
// other's. So no answer meant for one line of releases can install a build
// from another: the cache is per channel, the forced-upgrade floor is per
// channel, and a downloaded build is asked which channel it follows before it
// replaces anything.
type Channel struct {
	// Name is what `reminal version --json` reports, and what an upgrade
	// checks a downloaded build reports before installing it.
	Name string
	// CacheFile, under ~/.reminal, is where this machine remembers what the
	// channel last reported.
	CacheFile string
	// Latest is the newest release's tag ("v3.6.4"), "" when there is none.
	Latest func(ctx context.Context) (string, error)
	// Releases lists the channel's releases with their notes, newest first.
	Releases func(ctx context.Context) ([]Release, error)
	// Build is where a release's build for a platform is downloaded from, and
	// the digest it must match when the channel publishes one.
	Build func(ctx context.Context, tag, goos, goarch string) (Build, error)
	// CriticalMin is the version below which an upgrade is forced, "" for none.
	CriticalMin func() string
	// Reinstall is how someone reinstalls by hand when an upgrade cannot.
	Reinstall string
}

// Build is one downloadable build of a release for one platform.
type Build struct {
	URL    string
	SHA256 string // hex; "" when the channel publishes no digests
}

// channel is the one this binary follows.
var channel = mainChannel()

// ChannelName is the name of the channel this build follows.
func ChannelName() string { return channel.Name }

// mainChannel is the public releases, as published on GitHub.
func mainChannel() Channel {
	return Channel{
		Name:      "stable",
		CacheFile: "version-check.json",
		Latest:    fetchLatestTag,
		Releases:  fetchReleases,
		Build: func(_ context.Context, tag, goos, goarch string) (Build, error) {
			return Build{URL: assetURLFor(tag, goos, goarch)}, nil
		},
		CriticalMin: func() string { return fetchCriticalMin(httpTimeoutBackground) },
		Reinstall:   "curl -fsSL https://raw.githubusercontent.com/" + repo + "/main/install.sh | sh",
	}
}

// fetchBuild downloads a build into a temporary file and hands it back
// rewound, having checked its digest when the channel published one. Whole
// before anything is installed: a build that is cut short, or is not the one
// the channel named, must not get as far as replacing a working install. The
// caller removes the file.
func fetchBuild(b Build) (*os.File, error) {
	if b.URL == "" {
		return nil, errNoAssetForPlatform
	}
	// 10-minute ceiling. Big enough that even a slow phone-hotspot download of
	// a ~20 MB build completes; small enough that a hung connection doesn't tie
	// up the user's terminal until they notice and Ctrl-C.
	resp, err := (&http.Client{Timeout: 10 * time.Minute}).Get(b.URL)
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download: %s (url: %s)", resp.Status, b.URL)
	}
	f, err := os.CreateTemp("", "reminal-build-*.tar.gz")
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), resp.Body); err != nil { //nolint:gosec // a release build, sized by the server
		discardBuild(f)
		return nil, fmt.Errorf("download: %w", err)
	}
	if want := strings.ToLower(strings.TrimSpace(b.SHA256)); want != "" {
		if got := hex.EncodeToString(h.Sum(nil)); got != want {
			discardBuild(f)
			return nil, fmt.Errorf("the downloaded build is not the one that was published (sha256 %s, expected %s) — nothing was installed", got, want)
		}
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		discardBuild(f)
		return nil, err
	}
	return f, nil
}

func discardBuild(f *os.File) {
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
}

// buildChannelTimeout bounds asking a downloaded build its channel. It is one
// fork and a JSON line; a build that takes longer is not one to install.
const buildChannelTimeout = 20 * time.Second

// buildChannel asks a downloaded build which channel it follows, by running
// `version --json`. Running it is the one question a mislabelled upload, a
// mixed-up URL or a stale cache cannot answer wrongly: the build says what it
// is.
//
// It runs with an environment holding nothing but what a program needs to
// start. A hot restart hands a live session to the next binary through the
// environment, and a build being inspected must not mistake itself for one.
func buildChannel(path string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), buildChannelTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "version", "--json")
	cmd.Env = minimalEnv()
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return parseBuildChannel(out.Bytes())
}

// legacyChannel is what a build from before builds said which releases they
// follow answers with: a bare version. Every such build is a release of the
// stable line — no other line existed then — so it is taken where stable
// releases are wanted, and nowhere else: an earlier release can still be
// installed, and a machine on another line can come back to the stable
// release of the day even before one that answers exists.
const legacyChannel = "legacy"

var bareVersionRe = regexp.MustCompile(`^v?\d+\.\d+\.\d+`)

// parseBuildChannel reads the channel out of `version --json`. A build too
// old to answer prints a bare version: legacyChannel. Anything else is no
// answer at all.
func parseBuildChannel(out []byte) (string, error) {
	var v struct {
		Channel string `json:"channel"`
	}
	trimmed := bytes.TrimSpace(out)
	if err := json.Unmarshal(trimmed, &v); err != nil || v.Channel == "" {
		if bareVersionRe.Match(trimmed) {
			return legacyChannel, nil
		}
		return "", fmt.Errorf("it does not say which releases it follows")
	}
	return v.Channel, nil
}

// channelAccepts is whether a build that follows got may be installed where
// want is followed.
func channelAccepts(want, got string) bool {
	return got == want || (got == legacyChannel && want == mainChannel().Name)
}

func minimalEnv() []string {
	keep := []string{"PATH", "HOME", "TMPDIR"}
	if runtime.GOOS == "windows" {
		keep = append(keep, "SystemRoot", "USERPROFILE", "TEMP", "TMP", "ComSpec")
	}
	var env []string
	for _, k := range keep {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// sameChannel refuses a downloaded build that does not follow this binary's
// channel. Every install goes through it: a routine upgrade and a forced one
// alike.
func sameChannel(path string) error {
	got, err := buildChannel(path)
	if err != nil {
		return fmt.Errorf("could not ask the downloaded build which releases it follows (%v) — nothing was installed", err)
	}
	if !channelAccepts(channel.Name, got) {
		return fmt.Errorf("the downloaded build follows %q releases, and this install follows %q — nothing was installed", got, channel.Name)
	}
	return nil
}

// SwitchChannel moves this install onto another channel: the newest release
// the manifest at manifestURL describes, which must say it is channel want.
// The build is checked as every install is — its digest, then, run once, which
// channel it follows — but against want rather than this build's own channel;
// and it must be signed exactly as this install is, so that a switch can only
// ever land on one of ours, and a macOS grant made for this app survives it.
//
// The caller has already proven the request comes from an owner of this
// machine, for this manifest and this channel. Returns the version installed.
// Nothing is replaced unless every check passes.
func SwitchChannel(manifestURL, want string) (string, error) {
	want = strings.TrimSpace(want)
	if want == "" {
		return "", fmt.Errorf("no channel named to switch to")
	}
	ctx, cancel := context.WithTimeout(context.Background(), httpTimeoutInteractive)
	defer cancel()
	m, err := FetchManifest(ctx, manifestURL)
	if err != nil {
		return "", err
	}
	if m.Channel != want {
		return "", fmt.Errorf("the manifest describes %q releases, not %q — nothing was installed", m.Channel, want)
	}
	rs := m.Releases()
	if len(rs) == 0 {
		return "", fmt.Errorf("there are no %q releases yet", want)
	}
	b, err := m.Build("v"+rs[0].Version, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return "", err
	}
	accept := func(bin string) error {
		got, err := buildChannel(bin)
		if err != nil {
			return fmt.Errorf("could not ask the downloaded build which releases it follows (%v) — nothing was installed", err)
		}
		if !channelAccepts(want, got) {
			return fmt.Errorf("the downloaded build follows %q releases, not %q — nothing was installed", got, want)
		}
		return sameSigner(bin)
	}
	if err := applyWith(b, accept); err != nil {
		return "", err
	}
	return rs[0].Version, nil
}
