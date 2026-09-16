// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

// backendTarget probes the local backend once to learn (a) whether it speaks
// plain HTTP or HTTPS and (b) which loopback family it answers on, caching the
// answer. These tests lock in the behaviour of that probe — most importantly
// the IPv6-loopback fallback (6cfb86a): a dev server started with
// `--host localhost` on a Mac often listens only on [::1], and before the fix
// backendTarget only ever tried 127.0.0.1, so every visitor got
// "local server unreachable" while the same page loaded in the owner's browser.

// TestBackendTargetCacheShortCircuit: once scheme+addr are known, backendTarget
// returns them verbatim without touching the network (port is deliberately one
// nothing listens on, so a probe would resolve differently).
func TestBackendTargetCacheShortCircuit(t *testing.T) {
	tun := &Tunnel{port: 1, scheme: "https", addr: "[::1]:65535"}
	scheme, addr := tun.backendTarget()
	if scheme != "https" || addr != "[::1]:65535" {
		t.Fatalf("cached probe not returned: got (%q,%q), want (https,[::1]:65535)", scheme, addr)
	}
}

// TestBackendTargetHTTPv4: a plain-HTTP backend on 127.0.0.1 is detected as
// http at the IPv4 loopback, and the result is cached.
func TestBackendTargetHTTPv4(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	port := mustPort(t, srv.URL)

	tun := &Tunnel{port: port}
	scheme, addr := tun.backendTarget()
	want := fmt.Sprintf("127.0.0.1:%d", port)
	if scheme != "http" || addr != want {
		t.Fatalf("plain HTTP backend: got (%q,%q), want (http,%s)", scheme, addr, want)
	}
	if tun.scheme != "http" || tun.addr != want {
		t.Fatalf("result not cached: scheme=%q addr=%q", tun.scheme, tun.addr)
	}
}

// TestBackendTargetHTTPS: a TLS backend on 127.0.0.1 is detected as https via a
// successful handshake (the unambiguous HTTPS signal), so `reminal expose 8443`
// transparently reaches an HTTPS admin UI.
func TestBackendTargetHTTPS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	port := mustPort(t, srv.URL)

	tun := &Tunnel{port: port}
	scheme, addr := tun.backendTarget()
	want := fmt.Sprintf("127.0.0.1:%d", port)
	if scheme != "https" || addr != want {
		t.Fatalf("TLS backend: got (%q,%q), want (https,%s)", scheme, addr, want)
	}
}

// TestBackendTargetIPv6Fallback is the regression guard for 6cfb86a: a backend
// listening ONLY on [::1] (not on 127.0.0.1) must still be found. Pre-fix this
// returned the unreachable IPv4 address and every visitor saw a 502.
func TestBackendTargetIPv6Fallback(t *testing.T) {
	ln, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 loopback unavailable on this host: %v", err)
	}
	defer ln.Close()
	go acceptAndClose(ln)

	port := ln.Addr().(*net.TCPAddr).Port
	// Guard against the (rare) case that something is already on 127.0.0.1:port,
	// which would make backendTarget legitimately prefer v4 and void the test.
	if c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
		c.Close()
		t.Skipf("127.0.0.1:%d unexpectedly in use; cannot isolate IPv6 path", port)
	}

	tun := &Tunnel{port: port}
	scheme, addr := tun.backendTarget()
	want := fmt.Sprintf("[::1]:%d", port)
	if scheme != "http" || addr != want {
		t.Fatalf("IPv6-only backend not found: got (%q,%q), want (http,%s)", scheme, addr, want)
	}
}

// TestBackendTargetBackendDown: with nothing listening on either family,
// backendTarget returns a plain-http IPv4 target for a clean 502 but leaves the
// cache empty so the next request re-probes (the backend may come up).
func TestBackendTargetBackendDown(t *testing.T) {
	// Reserve then release a port so it is (almost certainly) closed.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	tun := &Tunnel{port: port}
	scheme, addr := tun.backendTarget()
	want := fmt.Sprintf("127.0.0.1:%d", port)
	if scheme != "http" || addr != want {
		t.Fatalf("backend-down fallback: got (%q,%q), want (http,%s)", scheme, addr, want)
	}
	if tun.scheme != "" || tun.addr != "" {
		t.Fatalf("cache must stay empty when backend is down: scheme=%q addr=%q", tun.scheme, tun.addr)
	}
}

// acceptAndClose accepts connections and closes them immediately: enough for a
// TCP-open probe to succeed while the earlier TLS handshake probe fails fast
// (EOF) rather than blocking to the schemeProbeTimeout.
func acceptAndClose(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		c.Close()
	}
}

func mustPort(t *testing.T, rawURL string) int {
	t.Helper()
	_, portStr, err := net.SplitHostPort(hostFromURL(t, rawURL))
	if err != nil {
		t.Fatalf("split host/port from %q: %v", rawURL, err)
	}
	var p int
	if _, err := fmt.Sscanf(portStr, "%d", &p); err != nil {
		t.Fatalf("parse port %q: %v", portStr, err)
	}
	return p
}

func hostFromURL(t *testing.T, rawURL string) string {
	t.Helper()
	// httptest URLs are always http(s)://host:port with no path.
	const httpsPrefix, httpPrefix = "https://", "http://"
	switch {
	case len(rawURL) > len(httpsPrefix) && rawURL[:len(httpsPrefix)] == httpsPrefix:
		return rawURL[len(httpsPrefix):]
	case len(rawURL) > len(httpPrefix) && rawURL[:len(httpPrefix)] == httpPrefix:
		return rawURL[len(httpPrefix):]
	default:
		t.Fatalf("unexpected URL scheme: %q", rawURL)
		return ""
	}
}
