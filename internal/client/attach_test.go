// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/reminal/reminal/internal/session"
)

// shortTempHome is a ~/.reminal home short enough that a unix socket path under
// it fits the OS sun_path limit (~104 bytes) — the default macOS temp dir
// (/var/folders/…) does not, which is a test-only concern: a real ~/.reminal is
// short, and serveAttach is best-effort (a bind failure just falls back to the
// relay).
func shortTempHome(t *testing.T) string {
	t.Helper()
	base := ""
	if runtime.GOOS != "windows" {
		base = "/tmp"
	}
	home, err := os.MkdirTemp(base, "rla")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".reminal"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	return home
}

// TestLocalAttachSocketGate pins when a viewer takes the local (no-relay) path:
// only when the target session is in THIS machine's registry AND its attach
// socket is actually up. A session known but with no live socket, or an unknown
// id, must fall through to the relay.
func TestLocalAttachSocketGate(t *testing.T) {
	home := shortTempHome(t)
	t.Setenv("HOME", home)
	t.Setenv("REMINAL_OWNERS_DIR", filepath.Join(home, "etc"))
	const id = "TESTSESS"

	// Unknown session → not local.
	if p, ok := localAttachSocket(id); ok {
		t.Fatalf("unknown session resolved to local socket %q", p)
	}

	// In the registry but no socket yet → still not local (agent not serving).
	// PID is this test process so ReadActiveByID sees a live session (a dead
	// PID is treated as a stale record and pruned).
	if err := session.WriteActive(session.Active{ID: id, PIN: "424242", PID: os.Getpid()}); err != nil {
		t.Fatal(err)
	}
	if p, ok := localAttachSocket(id); ok {
		t.Fatalf("registry-only session resolved to local socket %q (no listener up)", p)
	}

	// Bind the socket the agent would serve → now local.
	want, err := attachSockPath(id)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", want)
	if err != nil {
		t.Skipf("cannot bind attach socket at %q (path-length limit?): %v", want, err)
	}
	defer ln.Close()

	got, ok := localAttachSocket(id)
	if !ok || got != want {
		t.Fatalf("registry + live socket: got (%q, %v), want (%q, true)", got, ok, want)
	}

	// A different, unregistered id must not ride this socket.
	if p, ok := localAttachSocket("OTHERSESS"); ok {
		t.Fatalf("unregistered id resolved to local socket %q", p)
	}
}

// TestLocalAttachWSOverUnix proves the transport local attach rides on:
// WebSocket-over-AF_UNIX, upgraded by attachUpgrader and dialed exactly the way
// the viewer dials (websocket.Dialer with a unix NetDial). This is the
// platform-specific layer under a local attach — AF_UNIX behaves differently on
// Windows than on macOS/Linux — so running this via a cross-compiled test binary
// on each OS confirms the transport works there before the handshake even runs.
func TestLocalAttachWSOverUnix(t *testing.T) {
	home := shortTempHome(t)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir uses this on Windows

	path, err := attachSockPath("WSTEST")
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Skipf("cannot bind attach socket at %q: %v", path, err)
	}
	defer ln.Close()

	// Server side: the same upgrade attach.go uses, echoing one frame back.
	mux := http.NewServeMux()
	mux.HandleFunc("/attach", func(w http.ResponseWriter, r *http.Request) {
		c, err := attachUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		mt, msg, err := c.ReadMessage()
		if err != nil {
			return
		}
		_ = c.WriteMessage(mt, append([]byte("echo:"), msg...))
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	// Client side: the viewer's exact dial shape — ws:// URL, unix NetDial.
	d := websocket.Dialer{
		HandshakeTimeout: 3 * time.Second,
		NetDial:          func(_, _ string) (net.Conn, error) { return net.Dial("unix", path) },
	}
	conn, _, err := d.Dial("ws://local/attach", nil)
	if err != nil {
		t.Fatalf("dial ws-over-unix: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteMessage(websocket.TextMessage, []byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, got, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "echo:ping" {
		t.Fatalf("roundtrip got %q, want %q", got, "echo:ping")
	}
}
