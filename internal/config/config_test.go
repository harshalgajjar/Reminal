package config

import (
	"strings"
	"testing"
)

func withCloudDefaults(t *testing.T, relay, web string) {
	t.Helper()
	oldRelay, oldWeb := DefaultCloudRelay, DefaultCloudWeb
	DefaultCloudRelay, DefaultCloudWeb = relay, web
	t.Cleanup(func() {
		DefaultCloudRelay, DefaultCloudWeb = oldRelay, oldWeb
	})
	t.Setenv("REMINAL_RELAY", "")
	t.Setenv("REMINAL_WEB", "")
	t.Setenv("REMINAL_LOCAL", "")
}

func TestCompiledDefaultsAreLiveReminalApp(t *testing.T) {
	if DefaultCloudRelay != "wss://live.reminal.app/ws" {
		t.Fatalf("DefaultCloudRelay = %q", DefaultCloudRelay)
	}
	if DefaultCloudWeb != "https://live.reminal.app" {
		t.Fatalf("DefaultCloudWeb = %q", DefaultCloudWeb)
	}
	t.Setenv("REMINAL_RELAY", "")
	t.Setenv("REMINAL_WEB", "")
	t.Setenv("REMINAL_LOCAL", "")
	if got := RelayWS(); got != "wss://live.reminal.app/ws" {
		t.Fatalf("RelayWS() = %q", got)
	}
	if got := WebURL(); got != "https://live.reminal.app" {
		t.Fatalf("WebURL() = %q", got)
	}
}

func TestWorkersDevAliasStillNormalizes(t *testing.T) {
	withCloudDefaults(t, "wss://live.reminal.app/ws", "https://live.reminal.app")
	t.Setenv("REMINAL_WEB", "https://reminal-relay.futuristic.workers.dev")
	if got := WebURL(); got != "https://reminal-relay.futuristic.workers.dev" {
		t.Fatalf("WebURL() = %q", got)
	}
	if got := RelayWS(); got != "wss://reminal-relay.futuristic.workers.dev/ws" {
		t.Fatalf("RelayWS() = %q", got)
	}
}

func TestRuntimeRelayDerivesWebURL(t *testing.T) {
	withCloudDefaults(t, "wss://compiled.example/ws", "https://compiled.example")
	t.Setenv("REMINAL_RELAY", "wss://mine.example/prefix/ws/")
	if got := RelayWS(); got != "wss://mine.example/prefix/ws" {
		t.Fatalf("RelayWS() = %q", got)
	}
	if got := WebURL(); got != "https://mine.example/prefix" {
		t.Fatalf("WebURL() = %q, want runtime relay counterpart", got)
	}
}

func TestRuntimeWebDerivesRelayURL(t *testing.T) {
	withCloudDefaults(t, "wss://compiled.example/ws", "https://compiled.example")
	t.Setenv("REMINAL_WEB", "http://127.0.0.1:8787/base/")
	if got := WebURL(); got != "http://127.0.0.1:8787/base" {
		t.Fatalf("WebURL() = %q", got)
	}
	if got := RelayWS(); got != "ws://127.0.0.1:8787/base/ws" {
		t.Fatalf("RelayWS() = %q, want runtime web counterpart", got)
	}
}

func TestSingleBuildDefaultDerivesCounterpart(t *testing.T) {
	t.Run("relay", func(t *testing.T) {
		withCloudDefaults(t, "wss://relay.example/ws", "")
		if got := WebURL(); got != "https://relay.example" {
			t.Fatalf("WebURL() = %q", got)
		}
	})
	t.Run("web", func(t *testing.T) {
		withCloudDefaults(t, "", "https://relay.example")
		if got := RelayWS(); got != "wss://relay.example/ws" {
			t.Fatalf("RelayWS() = %q", got)
		}
	})
}

func TestLocalRelay(t *testing.T) {
	withCloudDefaults(t, "wss://compiled.example/ws", "https://compiled.example")
	t.Setenv("REMINAL_LOCAL", "1")
	if got := RelayWS(); got != DefaultLocalRelay {
		t.Fatalf("RelayWS() = %q, want %q", got, DefaultLocalRelay)
	}
	if got := WebURL(); got != DefaultLocalWeb {
		t.Fatalf("WebURL() = %q, want %q", got, DefaultLocalWeb)
	}
}

func TestInvalidRuntimeURLsFailValidationAndDoNotLeakEmptyValues(t *testing.T) {
	t.Run("web URL", func(t *testing.T) {
		withCloudDefaults(t, "wss://compiled.example/ws", "https://compiled.example")
		t.Setenv("REMINAL_WEB", "example.com")
		if err := ValidateRelayURLs(); err == nil || !strings.Contains(err.Error(), "REMINAL_WEB") {
			t.Fatalf("ValidateRelayURLs() = %v, want REMINAL_WEB error", err)
		}
		if got := RelayWS(); got != "wss://compiled.example/ws" {
			t.Fatalf("RelayWS() = %q, want safe compiled fallback", got)
		}
	})

	t.Run("relay scheme", func(t *testing.T) {
		withCloudDefaults(t, "wss://compiled.example/ws", "https://compiled.example")
		t.Setenv("REMINAL_RELAY", "ftp://relay.example/ws")
		if err := ValidateRelayURLs(); err == nil || !strings.Contains(err.Error(), "REMINAL_RELAY") {
			t.Fatalf("ValidateRelayURLs() = %v, want REMINAL_RELAY error", err)
		}
		if got := WebURL(); got != "https://compiled.example" {
			t.Fatalf("WebURL() = %q, want safe compiled fallback", got)
		}
	})
}

func TestLocalModeDoesNotRequireCloudDefaults(t *testing.T) {
	withCloudDefaults(t, "", "")
	t.Setenv("REMINAL_LOCAL", "1")
	if err := ValidateRelayURLs(); err != nil {
		t.Fatalf("ValidateRelayURLs() = %v in local mode", err)
	}
}
