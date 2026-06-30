package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	DefaultPort = "8080"

	DefaultLocalRelay = "ws://localhost:8080/ws"
	DefaultLocalWeb   = "http://localhost:8080"
)

// shellCandidates is consulted in order when $SHELL is unset. Tries the
// common interactive shells in roughly Mac→Linux order; /bin/sh is the
// last-resort POSIX fallback that exists on every Unix.
var shellCandidates = []string{"/bin/zsh", "/bin/bash", "/bin/sh"}

// Set at build time: -ldflags "-X github.com/reminal/reminal/internal/config.DefaultCloudRelay=wss://..."
// Pointed at the futuristic.workers.dev subdomain (harshalg98@gmail.com's
// Cloudflare account) since v0.6.3 — the original reminal.workers.dev
// subdomain on the other account got persistently throttled by
// Cloudflare's workers.dev edge anti-abuse rules.
var (
	DefaultCloudRelay = "wss://reminal-relay.futuristic.workers.dev/ws"
	DefaultCloudWeb   = "https://reminal-relay.futuristic.workers.dev"
)

func RelayWS() string {
	if v := os.Getenv("REMINAL_RELAY"); v != "" {
		return strings.TrimRight(v, "/")
	}
	if os.Getenv("REMINAL_LOCAL") == "1" {
		return DefaultLocalRelay
	}
	return DefaultCloudRelay
}

func WebURL() string {
	if v := os.Getenv("REMINAL_WEB"); v != "" {
		return strings.TrimRight(v, "/")
	}
	if os.Getenv("REMINAL_LOCAL") == "1" {
		return DefaultLocalWeb
	}
	return DefaultCloudWeb
}

func SessionWS(sessionID, role string) string {
	base := RelayWS()
	sessionID = strings.ToUpper(strings.TrimSpace(sessionID))
	return fmt.Sprintf("%s/%s/%s", base, sessionID, role)
}

// RendezvousWS builds the WebSocket URL for a `reminal copy`/`paste`
// rendezvous. RelayWS() ends in "/ws" (the shell-session prefix); the
// rendezvous lives under "/rv" on the same host, so we swap the suffix.
// role is "source" or "paste"; code is the canonical (uppercase,
// dash-free) transfer code that keys the relay's RendezvousRoom.
func RendezvousWS(code, role string) string {
	base := strings.TrimSuffix(RelayWS(), "/ws")
	return fmt.Sprintf("%s/rv/%s/%s", base, strings.ToUpper(code), role)
}

func Shell() string {
	if v := os.Getenv("SHELL"); v != "" {
		return v
	}
	// $SHELL unset (rare on interactive terminals, common in cron / systemd
	// service contexts). Probe the candidate list and return the first that
	// exists; falling back to /bin/sh which is POSIX-required.
	for _, candidate := range shellCandidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return "/bin/sh"
}

// DefaultSnapshotScrollbackLines is how many lines of scrollback history a
// fresh-attach snapshot includes by default. Generous enough to scroll back
// through a long session, bounded so the snapshot (and the agent's emulator
// memory) stay reasonable. Override with REMINAL_SCROLLBACK_LINES.
const DefaultSnapshotScrollbackLines = 10000

// SnapshotScrollbackLines returns how many scrollback lines to include in the
// attach snapshot. REMINAL_SCROLLBACK_LINES overrides the default; 0 means
// "screen only, no history"; negative or unparseable falls back to the default.
func SnapshotScrollbackLines() int {
	v := os.Getenv("REMINAL_SCROLLBACK_LINES")
	if v == "" {
		return DefaultSnapshotScrollbackLines
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 0 {
		return DefaultSnapshotScrollbackLines
	}
	return n
}

// DefaultSnapshotScrollbackBytes caps the rendered size of the scrollback
// portion of an attach snapshot, so a wide/colorful history can't balloon the
// payload even within the line limit. The visible screen is always included on
// top of this.
const DefaultSnapshotScrollbackBytes = 2 << 20 // 2 MiB

// SnapshotScrollbackBytes returns the byte cap for snapshot scrollback.
// REMINAL_SCROLLBACK_BYTES overrides the default; 0 means "no byte cap" (the
// line cap still applies); negative or unparseable falls back to the default.
func SnapshotScrollbackBytes() int {
	v := os.Getenv("REMINAL_SCROLLBACK_BYTES")
	if v == "" {
		return DefaultSnapshotScrollbackBytes
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 0 {
		return DefaultSnapshotScrollbackBytes
	}
	return n
}
