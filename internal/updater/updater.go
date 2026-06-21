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
	// httpTimeout caps the synchronous version check so a slow network never
	// blocks the agent banner for more than this.
	httpTimeout = 2 * time.Second
)

type cacheEntry struct {
	CheckedAt time.Time `json:"checked_at"`
	LatestTag string    `json:"latest_tag"`
	AssetURL  string    `json:"asset_url"`
}

type release struct {
	TagName string  `json:"tag_name"`
	Assets  []asset `json:"assets"`
}

type asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

// CheckAndPromptOnStart runs the full check + prompt + (optional) apply flow.
// It is safe to call at the very start of `reminal` or `reminal --connect`;
// it never returns a fatal error — network/cache/permission problems are
// silently swallowed so they can't break the user's primary action.
func CheckAndPromptOnStart(currentVersion string) {
	if !shouldCheck(currentVersion) {
		return
	}

	latestTag, assetURL, err := check(currentVersion)
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
	latestTag, assetURL, err := check(currentVersion)
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
// Result is cached at ~/.reminal/version-check.json for cacheTTL.
func check(currentVersion string) (latestTag, assetURL string, err error) {
	if entry, ok := readCache(); ok && time.Since(entry.CheckedAt) < cacheTTL {
		if newer(currentVersion, entry.LatestTag) {
			return entry.LatestTag, entry.AssetURL, nil
		}
		return "", "", nil
	}

	rel, err := fetchLatestRelease()
	if err != nil {
		return "", "", err
	}
	url := assetURLFor(rel, runtime.GOOS, runtime.GOARCH)

	// Always cache the latest tag so we don't refetch within the TTL, even
	// if no matching asset exists for this platform.
	writeCache(cacheEntry{CheckedAt: time.Now(), LatestTag: rel.TagName, AssetURL: url})

	if url == "" || !newer(currentVersion, rel.TagName) {
		return "", "", nil
	}
	return rel.TagName, url, nil
}

func fetchLatestRelease() (*release, error) {
	ctx, cancel := context.WithTimeout(context.Background(), httpTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET",
		"https://api.github.com/repos/"+repo+"/releases/latest", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github api: %s", resp.Status)
	}
	var rel release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

func assetURLFor(rel *release, goos, goarch string) string {
	// archive naming matches the CI workflow: reminal_<ver>_<os>_<arch>.tar.gz
	suffix := fmt.Sprintf("_%s_%s.tar.gz", goos, goarch)
	for _, a := range rel.Assets {
		if strings.HasSuffix(a.Name, suffix) {
			return a.URL
		}
	}
	return ""
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

	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download: %s", resp.Status)
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
