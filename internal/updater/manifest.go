// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// A manifest describes one channel's releases in a single file: which channel
// it is, every release with its notes, the build for each platform with its
// SHA-256, and the version below which an upgrade is forced. It is how a
// channel published outside GitHub releases is read — by a build that follows
// it, and by a machine asked to switch to it.
type Manifest struct {
	Channel     string            `json:"channel"`
	CriticalMin string            `json:"critical_min,omitempty"`
	Items       []manifestRelease `json:"releases"`

	base *url.URL // where the manifest came from
}

type manifestRelease struct {
	Version   string `json:"version"` // "1.0.0", no leading v
	Published string `json:"published,omitempty"`
	Notes     string `json:"notes,omitempty"`
	// Builds is keyed "<goos>_<goarch>", the same pair the archive names carry.
	Builds map[string]struct {
		URL    string `json:"url"` // absolute, or relative to the manifest
		SHA256 string `json:"sha256"`
	} `json:"builds"`
}

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// FetchManifest reads a manifest. It does not judge which channel it is; the
// caller, who knows what it asked for, does.
func FetchManifest(ctx context.Context, feed string) (*Manifest, error) {
	base, err := url.Parse(feed)
	if err != nil {
		return nil, err
	}
	if base.Scheme != "https" && !(base.Scheme == "http" && isLoopback(base.Hostname())) {
		return nil, fmt.Errorf("a release manifest is read over https, not from %s", feed)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feed, nil)
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
		return nil, fmt.Errorf("release manifest: HTTP %d", resp.StatusCode)
	}
	var m Manifest
	// Bounded read: a confused or hostile endpoint must not hand us an
	// unbounded body to buffer.
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&m); err != nil {
		return nil, fmt.Errorf("release manifest: %w", err)
	}
	m.base = base
	return &m, nil
}

func isLoopback(host string) bool {
	return host == "127.0.0.1" || host == "::1" || host == "localhost"
}

// Releases are the manifest's releases, newest first.
func (m *Manifest) Releases() []Release {
	var out []Release
	for _, r := range m.Items {
		v := strings.TrimPrefix(strings.TrimSpace(r.Version), "v")
		if v == "" {
			continue
		}
		out = append(out, Release{Version: v, Published: strings.TrimSpace(r.Published), Notes: r.Notes})
	}
	sortReleases(out)
	return out
}

// Build is one release's build for a platform. A manifest's build always
// comes with its digest, and always from where the manifest itself came from:
// a manifest cannot send a download anywhere else, and a build it gives no
// digest for is not one to install.
func (m *Manifest) Build(tag, goos, goarch string) (Build, error) {
	want := strings.TrimPrefix(strings.TrimSpace(tag), "v")
	for _, r := range m.Items {
		if strings.TrimPrefix(strings.TrimSpace(r.Version), "v") != want {
			continue
		}
		b, ok := r.Builds[goos+"_"+goarch]
		if !ok || strings.TrimSpace(b.URL) == "" {
			return Build{}, errNoAssetForPlatform
		}
		u, err := m.base.Parse(strings.TrimSpace(b.URL))
		if err != nil {
			return Build{}, fmt.Errorf("release manifest: build URL: %w", err)
		}
		if u.Scheme != m.base.Scheme || u.Host != m.base.Host {
			return Build{}, fmt.Errorf("release manifest points a build at %s, not at %s — refusing it", u.Host, m.base.Host)
		}
		sum := strings.ToLower(strings.TrimSpace(b.SHA256))
		if !sha256Hex.MatchString(sum) {
			return Build{}, fmt.Errorf("release manifest gives no SHA-256 for the %s_%s build of %s — refusing it", goos, goarch, want)
		}
		return Build{URL: u.String(), SHA256: sum}, nil
	}
	return Build{}, errNoAssetForPlatform
}
