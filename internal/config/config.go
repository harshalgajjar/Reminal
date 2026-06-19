package config

import (
	"fmt"
	"os"
	"strings"
)

const (
	DefaultPort  = "8080"
	DefaultShell = "/bin/zsh"

	DefaultLocalRelay = "ws://localhost:8080/ws"
	DefaultLocalWeb   = "http://localhost:8080"
)

// Set at build time: -ldflags "-X github.com/reminal/reminal/internal/config.DefaultCloudRelay=wss://..."
var (
	DefaultCloudRelay = "wss://reminal-relay.reminal.workers.dev/ws"
	DefaultCloudWeb   = "https://reminal-relay.reminal.workers.dev"
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

func Shell() string {
	if v := os.Getenv("SHELL"); v != "" {
		return v
	}
	return DefaultShell
}
