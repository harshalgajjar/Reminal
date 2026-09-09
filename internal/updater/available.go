// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package updater

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
)

// Release is one published release as the Host panel shows it: the version,
// when it shipped, and the notes a maintainer wrote in changelog/<v>.md (which
// the release workflow publishes as the release body).
type Release struct {
	Version   string `json:"version"`             // "3.5.7", no leading v
	Published string `json:"published,omitempty"` // RFC3339
	Notes     string `json:"notes,omitempty"`
	Current   bool   `json:"current,omitempty"` // the version this host is running
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
//
// Deliberately NOT tightened to chase a release published minutes ago. Polling
// harder is the wrong lever for that: the Host panel's button checks for real
// when it is pressed, so noticing a release yourself and forcing the upgrade
// does not depend on how recently the machine happened to look.
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

// releasesURL is the one place this package learns what has been released.
// The list endpoint, not the "latest" redirect: someone several versions
// behind needs the middle ones too, and — the point — everything that asks
// "what is newest" reads the same answer. A variable only so tests can aim it
// at a stub; nothing in the program reassigns it.
// releasesURL is GitHub's per-repo releases Atom feed — the WEB route
// (github.com), NOT api.github.com. It carries every release's notes yet shares
// the redirect's much higher anonymous limits, so reading "what's new" never
// trips the API's 60/hour-per-IP rate limit either. A var so tests point it at
// a local server.
var releasesURL = "https://github.com/" + repo + "/releases.atom"

// One feed, one memo, every consumer.
//
// There used to be two ways to learn what was newest: the upgrade offer read
// the "latest release" redirect, the notes read the release list, and each
// had its own cache with its own age. They disagreed — the panel said "up to
// date" beside a "What's new · 2" that plainly listed a newer version. Now the
// offer in host_info, the notes, and the daily check all derive from this one
// list, so they cannot disagree: the offer IS the top of the notes.
//
// feedTTL bounds the request rate. Opening the Host panel asks; a viewer
// opening it in a loop, or a PIN guest sending the message by hand, gets the
// memo. Sixty seconds keeps even a panel opened continuously under the
// unauthenticated limit of sixty an hour. Failures are remembered for
// feedErrTTL so a dead network cannot become a request loop either, and the
// previous good answer stays in place — an open panel must not lose an update
// it already knew about because of a momentary network problem.
const (
	feedTTL    = 60 * time.Second
	feedErrTTL = 30 * time.Second
)

var (
	feedMu    sync.Mutex
	feedVal   []Release // newest first, stable releases only
	feedRead  time.Time
	feedErr   error
	feedErrAt time.Time
)

// releaseFeed returns every published stable release, newest first, from the
// memo when fresh and from the network otherwise. A successful fetch also
// records the newest as the machine's cached "latest", which is what Available
// and the daily check read, so a fresh fetch anywhere updates the offer
// everywhere.
func releaseFeed(ctx context.Context) ([]Release, error) {
	feedMu.Lock()
	if !feedRead.IsZero() && time.Since(feedRead) < feedTTL {
		out := append([]Release(nil), feedVal...)
		feedMu.Unlock()
		return out, nil
	}
	if feedErr != nil && time.Since(feedErrAt) < feedErrTTL {
		err := feedErr
		feedMu.Unlock()
		return nil, err
	}
	feedMu.Unlock()

	fresh, err := fetchReleases(ctx)

	feedMu.Lock()
	defer feedMu.Unlock()
	if err != nil {
		feedErr, feedErrAt = err, time.Now()
		return nil, err
	}
	feedVal, feedRead, feedErr = fresh, time.Now(), nil
	if len(fresh) > 0 {
		recordLatest("v" + fresh[0].Version)
	}
	return append([]Release(nil), feedVal...), nil
}

// recordLatest is the single writer of the on-disk "latest release" cache.
// Available reads it for host_info; the startup prompt reads it to decide
// whether to ask. The criticality beacon rides along, because a check that
// reached the network is the moment to ask about it.
func recordLatest(tag string) {
	writeCache(cacheEntry{
		CheckedAt:   time.Now(),
		LatestTag:   tag,
		AssetURL:    assetURLFor(tag, runtime.GOOS, runtime.GOARCH),
		CriticalMin: fetchCriticalMin(httpTimeoutBackground),
	})
	// Otherwise the memo keeps reporting the previous answer for up to
	// availableTTL after the cache changed underneath it.
	availMu.Lock()
	availRead = time.Time{}
	availMu.Unlock()
}

// ReleasesSince returns the release currentVersion is itself running plus
// every published release newer than it, newest first, with the notes the
// release body carries.
//
// Inclusive of the current version because "what's new" is also asked by
// someone who has just upgraded and wants to read what they got. Filtering it
// out left the panel empty at exactly the moment it was up to date, which
// reads as broken rather than as finished. The current release is flagged
// Current so the panel marks it instead of counting it as something to
// upgrade to.
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
	all, err := releaseFeed(ctx)
	if err != nil {
		return nil, err
	}
	var out []Release
	for _, r := range all {
		if !atOrNewer(currentVersion, "v"+r.Version) {
			continue
		}
		r.Current = sameVersion(currentVersion, "v"+r.Version)
		out = append(out, r)
	}
	return capReleases(out, limit), nil
}

func capReleases(rs []Release, limit int) []Release {
	if limit > 0 && len(rs) > limit {
		return rs[:limit]
	}
	return rs
}

// fetchReleases reads the list endpoint: every stable release, newest first.
// fetchReleases reads GitHub's releases Atom feed and returns every stable
// release, newest first, with its notes. The Atom route is not rate-limited the
// way the JSON API is (see releasesURL), so a busy machine still gets "what's
// new". The notes ride the feed as GitHub-rendered HTML; atomNotesToText turns
// them back into the plain heading/bullet text the viewer renders.
func fetchReleases(ctx context.Context) ([]Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releasesURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "reminal")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
			return nil, fmt.Errorf("GitHub rate-limited this machine; try again shortly")
		}
		return nil, fmt.Errorf("release feed: HTTP %d", resp.StatusCode)
	}
	// Bounded read: a confused or hostile endpoint must not hand us an unbounded
	// body to buffer.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	var feed struct {
		Entries []struct {
			Title   string `xml:"title"`
			Updated string `xml:"updated"`
			Content string `xml:"content"`
		} `xml:"entry"`
	}
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, err
	}
	var out []Release
	for _, e := range feed.Entries {
		ver := strings.TrimPrefix(strings.TrimSpace(e.Title), "v")
		if ver == "" {
			continue
		}
		out = append(out, Release{
			Version:   ver,
			Published: strings.TrimSpace(e.Updated),
			Notes:     atomNotesToText(e.Content),
		})
	}
	sortReleases(out)
	return out, nil
}

var blankRuns = regexp.MustCompile(`\n{3,}`)

// atomNotesToText turns a release's GitHub-rendered HTML body (what the Atom
// feed carries) back into the plain heading / bullet / subheading text the
// viewer's note renderer reads. A real HTML walk, not tag-stripping, so nested
// or unexpected markup degrades to its text instead of leaking angle brackets.
func atomNotesToText(fragment string) string {
	fragment = strings.TrimSpace(fragment)
	if fragment == "" {
		return ""
	}
	doc, err := html.Parse(strings.NewReader(fragment))
	if err != nil {
		return fragment
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "h1", "h2", "h3", "h4", "h5", "h6":
				if t := nodeText(n); t != "" {
					b.WriteString("# " + t + "\n\n")
				}
				return
			case "li":
				if t := nodeText(n); t != "" {
					b.WriteString("- " + t + "\n")
				}
				return
			case "p":
				if t := nodeText(n); t != "" {
					b.WriteString(t + "\n\n")
				}
				return
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return strings.TrimSpace(blankRuns.ReplaceAllString(b.String(), "\n\n"))
}

// nodeText collects an element's descendant text with runs of whitespace
// collapsed to single spaces.
func nodeText(n *html.Node) string {
	var b strings.Builder
	var f func(*html.Node)
	f = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			f(c)
		}
	}
	f(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

// sortReleases puts the newest first. Separate so it is testable without a
// network round trip.
func sortReleases(rs []Release) {
	sort.Slice(rs, func(i, j int) bool { return newer(rs[j].Version, "v"+rs[i].Version) })
}

// atOrNewer reports whether tag names the version currentVersion is running,
// or a newer one. Available() deliberately goes on using newer(): the upgrade
// offer must still mean "there is somewhere to move to". Only the notes panel
// starts at the version you are on.
func atOrNewer(currentVersion, tag string) bool {
	return newer(currentVersion, tag) || sameVersion(currentVersion, tag)
}

// sameVersion compares on the parsed triple, so "3.6.0" and a "v3.6.0" tag are
// one release rather than two.
func sameVersion(a, b string) bool { return parseVer(a) == parseVer(b) }

// ReleaseNotesTimeout bounds the fetch: the user is waiting with a sheet open.
const ReleaseNotesTimeout = 10 * time.Second
