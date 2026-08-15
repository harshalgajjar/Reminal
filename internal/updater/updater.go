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
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/reminal/reminal/internal/config"
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
	// CriticalMin is the maintainer-set version below which an upgrade is FORCED
	// (a security/critical fix), fetched online from the relay's /version beacon
	// so a fix can be pushed out without users doing anything. Empty = nothing
	// forced. Cached with the rest so criticality is re-checked within cacheTTL.
	CriticalMin string `json:"critical_min,omitempty"`
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

	latestTag, assetURL, critical, err := check(currentVersion, httpTimeoutBackground)
	if err != nil || latestTag == "" {
		return
	}

	// Critical (e.g. security) update: the maintainer flagged it online via the
	// relay's /version beacon, so we don't wait for a Y/n — install it now, even
	// non-interactively. Users never have to run `--force`. The binary still
	// comes from the signed GitHub release, so a bad beacon can at worst push
	// everyone onto the latest real release.
	if critical {
		fmt.Fprintf(os.Stderr, "\n\x1b[1;31m⚠ Critical update %s — installing now (current v%s)\x1b[0m\n",
			latestTag, currentVersion)
		if err := apply(assetURL); err != nil {
			fmt.Fprintf(os.Stderr, "Critical upgrade failed: %v — run `reminal upgrade` manually.\n", err)
			return
		}
		fmt.Fprintf(os.Stderr, "Upgraded to %s. Restart reminal to use it.\n", latestTag)
		os.Exit(0)
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
// Upgrade performs an interactive upgrade. It reports whether the binary was
// actually replaced (false when already on the latest release) so the caller can
// skip post-upgrade work — like refreshing the background host — on a no-op.
func Upgrade(currentVersion string) (updated bool, err error) {
	// Bypass the cache so explicit `reminal upgrade` always hits the network.
	clearCache()
	latestTag, assetURL, _, err := check(currentVersion, httpTimeoutInteractive)
	if err != nil {
		return false, fmt.Errorf("check for updates: %w", err)
	}
	if latestTag == "" {
		fmt.Printf("reminal is already up to date (v%s).\n", currentVersion)
		return false, nil
	}
	fmt.Printf("Upgrading from v%s to %s...\n", currentVersion, latestTag)
	if err := apply(assetURL); err != nil {
		return false, err
	}
	fmt.Printf("Upgraded to %s. Restart reminal to use the new version.\n", latestTag)
	return true, nil
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
func check(currentVersion string, timeout time.Duration) (latestTag, assetURL string, critical bool, err error) {
	if entry, ok := readCache(); ok && time.Since(entry.CheckedAt) < cacheTTL {
		critical = entry.CriticalMin != "" && newer(currentVersion, entry.CriticalMin)
		if critical || newer(currentVersion, entry.LatestTag) {
			return entry.LatestTag, entry.AssetURL, critical, nil
		}
		return "", "", false, nil
	}

	tag, err := fetchLatestTag(timeout)
	if err != nil {
		return "", "", false, err
	}
	url := assetURLFor(tag, runtime.GOOS, runtime.GOARCH)
	// Online criticality signal (best-effort — a fetch failure just means no
	// forced upgrade this round). critical_min <= latest, so being below it
	// implies a newer release exists to upgrade to.
	criticalMin := fetchCriticalMin(timeout)

	// Always cache so we don't refetch within the TTL, even if no matching asset
	// exists for this platform.
	writeCache(cacheEntry{CheckedAt: time.Now(), LatestTag: tag, AssetURL: url, CriticalMin: criticalMin})

	critical = criticalMin != "" && newer(currentVersion, criticalMin)
	if url == "" || (!critical && !newer(currentVersion, tag)) {
		return "", "", false, nil
	}
	return tag, url, critical, nil
}

// fetchCriticalMin reads the relay's /version beacon and returns its
// critical_min ("" on any error or if unset). This is the ONLINE, maintainer-
// controlled switch that forces an upgrade — set it to the version below which
// clients must upgrade (e.g. after shipping a security fix). Best-effort: never
// fails the caller, so a beacon outage can't block or force anything.
func fetchCriticalMin(timeout time.Duration) string {
	base := strings.TrimRight(config.WebURL(), "/")
	if base == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", base+"/version", nil)
	if err != nil {
		return ""
	}
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var body struct {
		CriticalMin string `json:"critical_min"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&body) != nil {
		return ""
	}
	return strings.TrimSpace(body.CriticalMin)
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

	// macOS ships a signed reminal.app. Swap the WHOLE bundle so its code
	// signature — and thus the user's Screen Recording grant, which is anchored to
	// the bundle's Designated Requirement — stays intact. (Replacing just the
	// inner binary would break the seal.)
	if root := bundleRoot(bin); root != "" {
		return applyBundle(tr, root)
	}

	// State-based (NOT version-based) migration: a loose macOS binary — a pre-bundle
	// `curl … install.sh` install — upgrading to a modern darwin release, which is a
	// signed reminal.app. Install the bundle and repoint this CLI at it. Extracting
	// the inner binary loose (the fallthrough below) would strip the code identity the
	// daemon's Screen Recording / Accessibility grants are anchored to, silently
	// disabling the always-on capture daemon. Keyed on "am I a bare binary?", so it
	// self-repairs on any bare→bundle transition regardless of the version numbers.
	if runtime.GOOS == "darwin" {
		return migrateBareToBundle(tr, bin)
	}

	// The (linux, or legacy darwin) archives carry the reminal binary AND the
	// reminal-capture native window-capture helper. Install both, each atomically.
	// The helper is a best-effort sidecar: a failure to place it doesn't fail the
	// upgrade (the window mirror just falls back to screencapture).
	dir := filepath.Dir(bin)
	installedBin := false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar: %w", err)
		}
		switch filepath.Base(hdr.Name) {
		case "reminal", "reminal.exe":
			if err := installFileAtomic(bin, tr, dir); err != nil {
				return err
			}
			installedBin = true
		case "reminal-capture":
			_ = installFileAtomic(filepath.Join(dir, "reminal-capture"), tr, dir)
		}
	}
	if !installedBin {
		return errors.New("reminal binary not found in archive")
	}
	return nil
}

// EnsureBundleInstalled is the darwin self-heal for `reminal upgrade` run from an
// OLDER version. That old updater, not knowing about the .app, extracts a new
// release's inner binary LOOSE — stripping the sh.reminal code identity the daemon's
// TCC grants are anchored to (no bundle ⇒ no always-on capture daemon). On the next
// run this detects "I'm a loose release binary that should be a bundle," downloads
// THIS EXACT version's signed .app (by version, NOT "latest", so prereleases heal
// too), migrates to it, and returns the bundle's CLI path so the caller can re-exec
// from it. Correctness-based (keyed on install shape, not version numbers) and
// one-shot: once bundled, bundleRoot != "" and it no-ops. Best-effort — any failure
// leaves the loose binary working (degraded) and it retries next run. Returns
// ("", false) when there's nothing to do or on any error.
func EnsureBundleInstalled(currentVersion string) (bundleCLI string, healed bool) {
	if runtime.GOOS != "darwin" {
		return "", false
	}
	if currentVersion == "" || currentVersion == "dev" || currentVersion == "0.0.0" {
		return "", false // dev / manual build — never touch it
	}
	bin, err := os.Executable()
	if err != nil {
		return "", false
	}
	if real, rerr := filepath.EvalSymlinks(bin); rerr == nil {
		bin = real
	}
	if bundleRoot(bin) != "" {
		return "", false // already a bundle — the common, cheap path
	}
	// Loose darwin release binary → re-materialize the bundle from this exact
	// version's asset.
	url := assetURLFor("v"+strings.TrimPrefix(currentVersion, "v"), runtime.GOOS, runtime.GOARCH)
	resp, err := (&http.Client{Timeout: 10 * time.Minute}).Get(url)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return "", false
	}
	defer gz.Close()
	if err := migrateBareToBundle(tar.NewReader(gz), bin); err != nil {
		return "", false
	}
	return filepath.Join(appDir(), "reminal.app", "Contents", "MacOS", "reminal"), true
}

// bundleRoot returns the path to the enclosing reminal.app if bin lives inside a
// macOS bundle (…/reminal.app/Contents/MacOS/reminal), else "". Used to decide
// whether a self-update should swap a whole .app or a loose binary.
func bundleRoot(bin string) string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	macos := filepath.Dir(bin)      // …/Contents/MacOS
	contents := filepath.Dir(macos) // …/Contents
	app := filepath.Dir(contents)   // …/reminal.app
	if filepath.Base(macos) == "MacOS" && filepath.Base(contents) == "Contents" &&
		strings.HasSuffix(app, ".app") {
		return app
	}
	return ""
}

// applyBundle installs a new reminal.app from the tar stream: extract it to a
// staging dir beside the current bundle, then swap directories. The running
// process keeps its old binary inode until it hot-restarts from the same bundle
// path, so this is safe to do live. Rolls back the swap on failure.
func applyBundle(tr *tar.Reader, appRoot string) error {
	parent := filepath.Dir(appRoot)
	staging, err := os.MkdirTemp(parent, ".reminal-app.new-*")
	if err != nil {
		return fmt.Errorf("staging dir: %w", err)
	}
	defer os.RemoveAll(staging)

	const root = "reminal.app"
	seen := false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar: %w", err)
		}
		name := strings.TrimPrefix(filepath.Clean(hdr.Name), "./")
		if name != root && !strings.HasPrefix(name, root+string(filepath.Separator)) {
			continue // ignore anything outside the bundle
		}
		seen = true
		target := filepath.Join(staging, name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeSymlink:
			// Reject symlinks that escape the staging tree. An absolute or
			// ..-climbing Linkname, followed by a regular-file entry written
			// UNDER that link, would make os.OpenFile follow the link and write
			// through it to an arbitrary path (symlink-based tar slip). Legit
			// macOS .app bundles only carry relative, in-bundle symlinks, so this
			// never rejects a genuine release.
			resolved := hdr.Linkname
			if !filepath.IsAbs(resolved) {
				resolved = filepath.Join(filepath.Dir(target), resolved)
			}
			resolved = filepath.Clean(resolved)
			if resolved != staging && !strings.HasPrefix(resolved, staging+string(filepath.Separator)) {
				continue // link points outside staging — drop it
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			_ = os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode&0o777))
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil { //nolint:gosec // release asset, sized by CDN
				_ = f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		}
	}
	if !seen {
		return errors.New("reminal.app not found in archive")
	}
	newApp := filepath.Join(staging, root)

	// Swap: move the old bundle aside (if one exists — a bare→bundle migration
	// installs fresh into ~/Applications with nothing to replace), move the new one
	// in, drop the old. The window between renames is tiny; roll back if the second
	// rename fails.
	backup := appRoot + ".old"
	_ = os.RemoveAll(backup)
	hadOld := false
	if _, statErr := os.Stat(appRoot); statErr == nil {
		if err := os.Rename(appRoot, backup); err != nil {
			return fmt.Errorf("move old bundle: %w", err)
		}
		hadOld = true
	}
	if err := os.Rename(newApp, appRoot); err != nil {
		if hadOld {
			_ = os.Rename(backup, appRoot) // roll back
		}
		return fmt.Errorf("install new bundle: %w", err)
	}
	if hadOld {
		_ = os.RemoveAll(backup)
	}
	return nil
}

// migrateBareToBundle upgrades a loose macOS binary install to the signed
// reminal.app: installs the .app under ~/Applications (honoring REMINAL_APP_DIR,
// matching install.sh), repoints the on-PATH CLI at the bundle's binary via a
// symlink, drops the now-stale loose capture helper, and registers the app with
// LaunchServices. The daemon is (re)installed separately by the caller's idempotent
// correctness check. Idempotent: safe to run on any version→bundle transition.
func migrateBareToBundle(tr *tar.Reader, bareBin string) error {
	appRoot := filepath.Join(appDir(), "reminal.app")
	if err := os.MkdirAll(filepath.Dir(appRoot), 0o755); err != nil {
		return fmt.Errorf("create app dir: %w", err)
	}
	if err := applyBundle(tr, appRoot); err != nil {
		if strings.Contains(err.Error(), "not found in archive") {
			return fmt.Errorf("%w — re-run the install script: "+
				"curl -fsSL https://raw.githubusercontent.com/%s/main/install.sh | sh", err, repo)
		}
		return err
	}
	// Repoint the PATH entry (…/.local/bin/reminal) at the bundle so `reminal` keeps
	// resolving after the migration, and the daemon (which EvalSymlinks's it) runs
	// the sh.reminal bundle identity.
	inner := filepath.Join(appRoot, "Contents", "MacOS", "reminal")
	_ = os.Remove(bareBin)
	if err := os.Symlink(inner, bareBin); err != nil {
		return fmt.Errorf("symlink CLI at %s: %w", bareBin, err)
	}
	// The bundle carries its own ScreenCaptureKit helper; remove the loose sidecar
	// from the old bare install so it can't shadow the bundle's copy.
	_ = os.Remove(filepath.Join(filepath.Dir(bareBin), "reminal-capture"))
	// Best-effort: register icon/identity with LaunchServices, strip quarantine.
	lsregister(appRoot)
	_ = exec.Command("/usr/bin/xattr", "-dr", "com.apple.quarantine", appRoot).Run()
	fmt.Fprintln(os.Stderr,
		"Installed reminal.app. Run `reminal permissions` once to grant screen recording, accessibility, and automation.")
	return nil
}

// appDir is where reminal.app is installed — REMINAL_APP_DIR or ~/Applications
// (matching install.sh).
func appDir() string {
	if d := strings.TrimSpace(os.Getenv("REMINAL_APP_DIR")); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "Applications"
	}
	return filepath.Join(home, "Applications")
}

// lsregister registers the bundle with LaunchServices so Finder/Settings show its
// icon and identity. Best-effort.
func lsregister(app string) {
	const lsr = "/System/Library/Frameworks/CoreServices.framework/Frameworks/" +
		"LaunchServices.framework/Support/lsregister"
	if _, err := os.Stat(lsr); err == nil {
		_ = exec.Command(lsr, "-f", app).Run()
	}
}

// installFileAtomic writes r to dest by streaming into a sibling temp file on
// the same filesystem and renaming it over dest — atomic, and safe to do to a
// currently-running binary (the kernel keeps the old inode alive for running
// processes). dir must be dest's directory so the rename stays on one FS.
func installFileAtomic(dest string, r io.Reader, dir string) error {
	tmp, err := os.CreateTemp(dir, ".reminal.new-*")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer func() {
		if _, err := os.Stat(tmpName); err == nil {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := io.Copy(tmp, r); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmpName, dest); err != nil {
		// Windows can't replace a RUNNING executable in place — but it can
		// RENAME one. Shuffle the live file aside and slot the new one in;
		// the orphaned .old is cleaned up on the next upgrade (below) or by
		// the installer.
		if runtime.GOOS == "windows" {
			old := dest + ".old"
			_ = os.Remove(old) // a leftover from the previous upgrade, if any
			if rerr := os.Rename(dest, old); rerr == nil {
				if err2 := os.Rename(tmpName, dest); err2 == nil {
					return nil
				}
				// Put the original back so the install isn't left headless.
				_ = os.Rename(old, dest)
			}
		}
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
