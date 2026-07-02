// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

// Package updater implements the in-binary version check and self-update flow
// used by `reminal` and `reminal --connect`. On start we read a 24h cache at
// ~/.reminal/version-check.json; if a newer release exists we prompt the user
// to upgrade, and on approval we download the tarball and atomically swap the
// running binary.
package updater

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"
)

const (
	repo     = "harshalgajjar/Reminal"
	cacheTTL = 24 * time.Hour
	// httpTimeoutBackground caps the on-start version check so a slow
	// network never blocks the agent banner for more than this. Cache hits
	// don't go through this; only the first launch / 24h refresh does.
	httpTimeoutBackground = 2 * time.Second
	// httpTimeoutInteractive applies to explicit `reminal upgrade` — the
	// user is at the keyboard waiting, so a longer ceiling is appropriate
	// (mobile networks regularly take 5-10s for the GitHub API to respond).
	httpTimeoutInteractive = 15 * time.Second
)

type cacheEntry struct {
	CheckedAt time.Time `json:"checked_at"`
	LatestTag string    `json:"latest_tag"`
	AssetURL  string    `json:"asset_url"`
}

// (release / asset structs removed in favour of fetchLatestTag — see
// the comment on that function for the rationale.)

// CheckAndPromptOnStart runs the full check + prompt + (optional) apply flow.
// It is safe to call at the very start of `reminal` or `reminal --connect`;
// it never returns a fatal error — network/cache/permission problems are
// silently swallowed so they can't break the user's primary action.
func CheckAndPromptOnStart(currentVersion string) {
	if !shouldCheck(currentVersion) {
		return
	}

	latestTag, assetURL, err := check(currentVersion, httpTimeoutBackground)
	if err != nil || latestTag == "" {
		return
	}

	if !isInteractive() {
		fmt.Fprintf(os.Stderr, "\nA new version of reminal is available: %s (current: v%s)\n",
			latestTag, currentVersion)
		fmt.Fprintln(os.Stderr, "Run `reminal upgrade` to install.")
		return
	}

	if !promptYesDefault(fmt.Sprintf("New version %s available (current: v%s). Upgrade now? (Y/n) ",
		latestTag, currentVersion)) {
		return
	}

	if err := apply(assetURL); err != nil {
		fmt.Fprintf(os.Stderr, "Upgrade failed: %v\n", err)
		return
	}
	fmt.Fprintf(os.Stderr, "Upgraded to %s. Restart reminal to use the new version.\n", latestTag)
	os.Exit(0)
}

// Upgrade runs the explicit `reminal upgrade` subcommand: forces a fresh
// version check and applies the upgrade if one is available. Returns an
// error so the caller can set a nonzero exit code on failure.
func Upgrade(currentVersion string) error {
	// Bypass the cache so explicit `reminal upgrade` always hits the network.
	clearCache()
	latestTag, assetURL, err := check(currentVersion, httpTimeoutInteractive)
	if err != nil {
		return fmt.Errorf("check for updates: %w", err)
	}
	if latestTag == "" {
		fmt.Printf("reminal is already up to date (v%s).\n", currentVersion)
		return nil
	}
	fmt.Printf("Upgrading from v%s to %s...\n", currentVersion, latestTag)
	if err := apply(assetURL); err != nil {
		return err
	}
	fmt.Printf("Upgraded to %s. Restart reminal to use the new version.\n", latestTag)
	return nil
}

// shouldCheck reports whether the version-check is meaningful for this build.
// Dev builds and unknown versions skip the check entirely.
func shouldCheck(currentVersion string) bool {
	if currentVersion == "" || currentVersion == "dev" || currentVersion == "0.0.0" {
		return false
	}
	bin, err := os.Executable()
	if err != nil {
		return false
	}
	// Brew-managed installs should be upgraded via `brew upgrade reminal`,
	// not by replacing the file in the Cellar; skip the prompt for them.
	if real, err := filepath.EvalSymlinks(bin); err == nil && strings.Contains(real, "/Cellar/") {
		return false
	}
	return true
}

// check returns the latest release tag and the asset download URL for this
// OS/arch, or ("", "", nil) if the running version is already current.
// Result is cached at ~/.reminal/version-check.json for cacheTTL. The
// timeout caps how long the network fetch can take — short for background
// on-start checks, long for explicit `reminal upgrade`.
func check(currentVersion string, timeout time.Duration) (latestTag, assetURL string, err error) {
	if entry, ok := readCache(); ok && time.Since(entry.CheckedAt) < cacheTTL {
		if newer(currentVersion, entry.LatestTag) {
			return entry.LatestTag, entry.AssetURL, nil
		}
		return "", "", nil
	}

	tag, err := fetchLatestTag(timeout)
	if err != nil {
		return "", "", err
	}
	url := assetURLFor(tag, runtime.GOOS, runtime.GOARCH)

	// Always cache the latest tag so we don't refetch within the TTL, even
	// if no matching asset exists for this platform.
	writeCache(cacheEntry{CheckedAt: time.Now(), LatestTag: tag, AssetURL: url})

	if url == "" || !newer(currentVersion, tag) {
		return "", "", nil
	}
	return tag, url, nil
}

// fetchLatestTag returns the latest release tag (e.g. "v0.8.3") by
// reading the Location header on a request to the public release URL
// — github.com/<repo>/releases/latest redirects to /releases/tag/<tag>.
// This deliberately avoids api.github.com, which rate-limits
// unauthenticated requests at 60/hour per IP and was tripping users
// during the day with a "403 Forbidden" instead of a clean upgrade.
// The web route has separate, much higher anonymous limits and
// returns the redirect regardless.
func fetchLatestTag(timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET",
		"https://github.com/"+repo+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	// Don't follow the redirect — the Location header IS the answer.
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Timeout: timeout,
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	// 302 is what github currently returns; tolerate the other
	// redirect codes in case they change.
	switch resp.StatusCode {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
	default:
		return "", fmt.Errorf("github web: expected redirect, got %s", resp.Status)
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", fmt.Errorf("github web: no Location header")
	}
	// Pull "v0.8.3" out of ".../releases/tag/v0.8.3".
	idx := strings.LastIndex(loc, "/tag/")
	if idx < 0 {
		return "", fmt.Errorf("github web: unexpected redirect target %q", loc)
	}
	tag := loc[idx+len("/tag/"):]
	// Strip any query string / fragment / trailing slash GitHub may
	// add — assetURLFor pastes tag straight into a URL and we don't
	// want "v0.8.3?utm=foo" turning into a 404.
	for _, sep := range []string{"?", "#", "/"} {
		if i := strings.Index(tag, sep); i >= 0 {
			tag = tag[:i]
		}
	}
	if tag == "" {
		return "", fmt.Errorf("github web: empty tag in redirect target %q", loc)
	}
	return tag, nil
}

// assetURLFor builds the direct binary-download URL for the given
// tag + platform. Pattern matches the release-workflow's archive
// naming (reminal_<ver>_<os>_<arch>.tar.gz). No API call needed —
// github resolves /releases/download/<tag>/<file> to the CDN-backed
// asset URL on its own.
func assetURLFor(tag, goos, goarch string) string {
	ver := strings.TrimPrefix(tag, "v")
	return fmt.Sprintf("https://github.com/%s/releases/download/%s/reminal_%s_%s_%s.tar.gz",
		repo, tag, ver, goos, goarch)
}

// apply downloads the tarball at url, extracts the reminal binary, and
// atomically swaps the running binary with the new one.
func apply(url string) error {
	bin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate self: %w", err)
	}
	// Resolve symlinks so we replace the real file, not the link.
	if real, err := filepath.EvalSymlinks(bin); err == nil {
		bin = real
	}

	// 10-minute ceiling. Big enough that even a slow phone-hotspot
	// download of a ~10 MB binary completes; small enough that a
	// hung connection doesn't tie up the user's terminal until they
	// notice and Ctrl-C. GitHub's CDN is well-behaved, so the
	// common case is sub-30s.
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download: %s (url: %s)", resp.Status, url)
	}

	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	var reader io.Reader
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar: %w", err)
		}
		if filepath.Base(hdr.Name) == "reminal" {
			reader = tr
			break
		}
	}
	if reader == nil {
		return errors.New("reminal binary not found in archive")
	}

	// Write to a sibling temp file on the same filesystem so the rename is
	// atomic. On Unix the rename works even while the old binary is running
	// because the kernel keeps the old inode alive for executing processes.
	dir := filepath.Dir(bin)
	tmp, err := os.CreateTemp(dir, ".reminal.new-*")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer func() {
		// If we never renamed, clean up the temp file.
		if _, err := os.Stat(tmpName); err == nil {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := io.Copy(tmp, reader); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmpName, bin); err != nil {
		return fmt.Errorf("install (need write access to %s): %w", dir, err)
	}
	return nil
}

// newer returns true if latest > current. Versions are dotted ints with an
// optional leading "v"; non-numeric suffixes (-rc1 etc.) are ignored on the
// part that contains them.
func newer(current, latestTag string) bool {
	cur := parseVer(current)
	lat := parseVer(latestTag)
	for i := 0; i < 3; i++ {
		if lat[i] > cur[i] {
			return true
		}
		if lat[i] < cur[i] {
			return false
		}
	}
	return false
}

func parseVer(v string) [3]int {
	v = strings.TrimPrefix(v, "v")
	parts := strings.Split(v, ".")
	var out [3]int
	for i := 0; i < 3 && i < len(parts); i++ {
		// Drop anything after a `-` so "1.2.0-rc1" parses as 1.2.0.
		p := strings.SplitN(parts[i], "-", 2)[0]
		n, _ := strconv.Atoi(strings.TrimSpace(p))
		out[i] = n
	}
	return out
}

func cachePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".reminal", "version-check.json"), nil
}

func readCache() (cacheEntry, bool) {
	path, err := cachePath()
	if err != nil {
		return cacheEntry{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cacheEntry{}, false
	}
	var e cacheEntry
	if err := json.Unmarshal(data, &e); err != nil {
		return cacheEntry{}, false
	}
	return e, true
}

func writeCache(e cacheEntry) {
	path, err := cachePath()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o644)
}

func clearCache() {
	if path, err := cachePath(); err == nil {
		_ = os.Remove(path)
	}
}

func isInteractive() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stderr.Fd()))
}

// promptYesDefault prints msg and reads one line from stdin. Returns true for
// y/yes/empty (default), false for n/no.
func promptYesDefault(msg string) bool {
	fmt.Fprint(os.Stderr, msg)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	resp := strings.ToLower(strings.TrimSpace(line))
	return resp == "" || resp == "y" || resp == "yes"
}
