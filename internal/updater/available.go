// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Release is one published release as the Host panel shows it: the version,
// when it shipped, and the notes a maintainer wrote in changelog/<v>.md (which
// the release workflow publishes as the release body).
type Release struct {
	Version   string `json:"version"`             // "3.5.7", no leading v
	Published string `json:"published,omitempty"` // RFC3339
	Notes     string `json:"notes,omitempty"`
}

// availableTTL memoizes the answer. No network is involved, but the call does
// an os.Executable, an EvalSymlinks and a JSON file read — and host_info is
// polled every 1.5s while the Host panel is open, so an uncached Available
// turned "is there an update" into steady syscall traffic for a value the
// underlying check only refreshes once a day.
const availableTTL = 30 * time.Second

var (
	availMu   sync.Mutex
	availFor  string // the version the memo was computed for
	availVal  string
	availRead time.Time
)

// Available reports the newest release the last check saw, or "" when we are
// up to date or have never checked. Read from the on-disk cache the periodic
// update check maintains: no network, no blocking, safe to call from a
// request handler.
func Available(currentVersion string) string {
	availMu.Lock()
	defer availMu.Unlock()
	if availFor == currentVersion && !availRead.IsZero() && time.Since(availRead) < availableTTL {
		return availVal
	}
	availFor, availRead, availVal = currentVersion, time.Now(), ""
	if !shouldCheck(currentVersion) {
		return ""
	}
	entry, ok := readCache()
	if !ok || entry.LatestTag == "" {
		return ""
	}
	if !newer(currentVersion, entry.LatestTag) {
		return ""
	}
	availVal = strings.TrimPrefix(entry.LatestTag, "v")
	return availVal
}

// availableRefreshInterval is how often a long-lived agent re-asks. cacheTTL
// still gates the network, so this costs at most one request a day; the loop
// exists so a host that stays up for weeks does not go on reporting the version
// that was current the day it started.
const availableRefreshInterval = 6 * time.Hour

// RefreshAvailable updates the cached answer to "is there a newer release"
// without prompting and without installing anything.
//
// The interactive path (CheckAndPromptOnStart) deliberately does not run for a
// detached background session — a child with no terminal has nobody to prompt,
// and must not replace itself behind the user's back. But the Host panel's
// upgrade offer reads the cache that check() writes, so a machine whose
// sessions are ALL background ones never learned that a new version existed:
// the button never appeared on exactly the always-on hosts it is meant for.
//
// This writes the cache and stops there. Deciding to upgrade stays with the
// person looking at the panel.
func RefreshAvailable(currentVersion string) {
	if !shouldCheck(currentVersion) {
		return
	}
	_, _, _, _ = check(currentVersion, httpTimeoutBackground)
}

// StartAvailableRefresh keeps that answer fresh for as long as this process
// lives. Non-blocking; the first check runs on its own goroutine so a slow or
// unreachable GitHub never delays a session coming up.
func StartAvailableRefresh(currentVersion string) {
	go func() {
		for {
			RefreshAvailable(currentVersion)
			time.Sleep(availableRefreshInterval)
		}
	}()
}

// UpgradeBlockedReason explains why this build must not be upgraded in place,
// or "" when it may be. Available() already hides the offer for these, but the
// handler behind the button must not trust the UI: the request is a message on
// a channel, and a build that shouldNOT be replaced must refuse regardless of
// what asked. A dev binary silently becoming a release mid-debug is the
// specific accident this prevents.
func UpgradeBlockedReason(currentVersion string) string {
	if currentVersion == "" || currentVersion == "dev" || currentVersion == "0.0.0" {
		return "this is a development build — upgrade it by rebuilding, not from here"
	}
	if !shouldCheck(currentVersion) {
		// The remaining case shouldCheck rejects is a Homebrew install, where
		// replacing the file inside the Cellar is something brew would undo.
		return "this install is managed elsewhere (Homebrew); upgrading here would not stick"
	}
	return ""
}

// releasesURL is the list endpoint. The single-release endpoint would need one
// request per version, and someone several versions behind is exactly who this
// is for.
const releasesURL = "https://api.github.com/repos/harshalgajjar/Reminal/releases?per_page=30"

// releaseNotesTTL caches the fetched list. The list changes when a release is
// published — rarely — and the fetch is triggered by anyone who opens the Host
// panel's "What's new". Uncached, every viewer on every session cost one
// GitHub call, and the unauthenticated limit is 60 an hour per IP: a viewer
// reopening the sheet could exhaust the host's quota and leave the owner
// unable to read anything. The cache makes that a non-issue rather than
// something to police.
const releaseNotesTTL = 10 * time.Minute

var (
	relMu    sync.Mutex
	relFor   string
	relVal   []Release
	relRead  time.Time
	relErrAt time.Time // failures are cached briefly too, so a broken network
	relErr   error     // cannot be turned into a request loop either
)

// releaseErrTTL is how long a failure is remembered. Short enough that a
// transient outage clears on the next try, long enough that a viewer holding
// the button down cannot hammer the API.
const releaseErrTTL = 30 * time.Second

// ReleasesSince returns every published release newer than currentVersion,
// newest first, with the notes the release body carries.
//
// Notes for a version you do not have cannot come from your own binary — a
// host on 3.5.4 has no 3.5.6 file — so they are fetched. One request covers
// every intermediate version, which is the case that matters: someone three
// releases behind has no other way to learn what landed in the middle two.
func ReleasesSince(ctx context.Context, currentVersion string, limit int) ([]Release, error) {
	// An unversioned build compares as 0.0.0, so every release looks newer and
	// the panel would claim "20 releases behind". Say what is actually true.
	if currentVersion == "" || currentVersion == "dev" || currentVersion == "0.0.0" {
		return nil, fmt.Errorf("this is a development build, so there is nothing to compare against")
	}

	relMu.Lock()
	if relFor == currentVersion {
		if !relRead.IsZero() && time.Since(relRead) < releaseNotesTTL {
			out := append([]Release(nil), relVal...)
			relMu.Unlock()
			return capReleases(out, limit), nil
		}
		if relErr != nil && time.Since(relErrAt) < releaseErrTTL {
			err := relErr
			relMu.Unlock()
			return nil, err
		}
	}
	relMu.Unlock()

	fresh, err := fetchReleases(ctx, currentVersion)

	relMu.Lock()
	relFor = currentVersion
	if err != nil {
		relErr, relErrAt = err, time.Now()
		relMu.Unlock()
		return nil, err
	}
	relVal, relRead, relErr = fresh, time.Now(), nil
	out := append([]Release(nil), relVal...)
	relMu.Unlock()
	return capReleases(out, limit), nil
}

func capReleases(rs []Release, limit int) []Release {
	if limit > 0 && len(rs) > limit {
		return rs[:limit]
	}
	return rs
}

func fetchReleases(ctx context.Context, currentVersion string) ([]Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releasesURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "reminal")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Rate limiting is the common one (60/hour unauthenticated), and it is
		// worth saying so rather than "failed".
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
			return nil, fmt.Errorf("GitHub rate-limited this machine; notes will be readable again shortly")
		}
		return nil, fmt.Errorf("release list: HTTP %d", resp.StatusCode)
	}
	// Bounded read: a compromised or confused endpoint must not be able to hand
	// us an unbounded body to buffer.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	var raw []struct {
		TagName     string `json:"tag_name"`
		Body        string `json:"body"`
		Draft       bool   `json:"draft"`
		Prerelease  bool   `json:"prerelease"`
		PublishedAt string `json:"published_at"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	var out []Release
	for _, r := range raw {
		if r.Draft || r.Prerelease {
			continue // release candidates are not what an upgrade lands on
		}
		if !newer(currentVersion, r.TagName) {
			continue
		}
		out = append(out, Release{
			Version:   strings.TrimPrefix(r.TagName, "v"),
			Published: r.PublishedAt,
			Notes:     strings.TrimSpace(r.Body),
		})
	}
	// The API returns newest-first already, but it is not documented to, and
	// the panel's "next / skipped / you are here" ordering depends on it.
	sortReleases(out)
	return out, nil
}

// sortReleases puts the newest first — the order the panel reads as
// "next", then the skipped middle. Separate so it is testable without a
// network round trip.
func sortReleases(rs []Release) {
	sort.Slice(rs, func(i, j int) bool { return newer(rs[j].Version, "v"+rs[i].Version) })
}

// ReleaseNotesTimeout bounds the fetch: the user is waiting with a sheet open.
const ReleaseNotesTimeout = 10 * time.Second
