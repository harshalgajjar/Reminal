package config

import (
	"fmt"
	"os"
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
