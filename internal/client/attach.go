// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"net"
	"net/http"
	"os"
	"path/filepath"

	"github.com/gorilla/websocket"
)

// Local attach: a session serves the SAME session protocol on a per-session
// unix socket as it does over the relay, so a same-machine viewer can attach
// with no relay at all — when the machine is offline, or just more directly.
// It's WebSocket-over-the-socket, so the entire wire protocol, crypto box, and
// PIN/owner handshake run unchanged (see serveConn) — only the transport
// differs, which is what makes a local attach feel exactly like a relay one.
//
// The socket is owner-only (0600), like the control socket: a peer that can
// open it already has the user's own filesystem access. AF_UNIX works on
// macOS, Linux, and Windows alike (the control/mirror/notes sockets already
// rely on it), so this needs no per-platform transport.

// attachSockPath is the local attach socket for a session, under ~/.reminal.
func attachSockPath(sessionID string) (string, error) {
	dir, err := reminalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "attach-"+sessionID+".sock"), nil
}

// attachUpgrader turns an accepted socket connection into a WebSocket. The peer
// is a same-machine process reaching a 0600 socket, not a browser, so there is
// no cross-origin surface to guard.
var attachUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(*http.Request) bool { return true },
}

// serveAttach listens on the session's local socket for the life of the session
// and serves each connection with the same handler as the relay path
// (serveConn, local=true). Best-effort: failing to open the socket costs the
// local-attach convenience, never the session itself. A no-op for the headless
// machine channel, which has no shell to attach to.
func (a *Agent) serveAttach(shellExit <-chan struct{}) {
	if a.machine {
		return
	}
	path, err := attachSockPath(a.sessionID)
	if err != nil {
		return
	}
	// A crashed prior run can leave a stale socket file that blocks the bind.
	// Removing the path is safe: a live peer holds the inode, not the name, and
	// the session registry (not this file) is the source of truth.
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return
	}
	_ = os.Chmod(path, 0o600)
	go func() {
		<-shellExit
		_ = ln.Close() // unblocks Serve so this returns at shell exit
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/attach", func(w http.ResponseWriter, r *http.Request) {
		conn, err := attachUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		_ = a.serveConn(conn, shellExit, true)
	})
	_ = (&http.Server{Handler: mux}).Serve(ln)
	_ = os.Remove(path)
}
