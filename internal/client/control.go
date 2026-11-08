// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reminal/internal/atrest"
	"reminal/internal/session"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"reminal/internal/protocol"
)

// randomID returns a 16-hex-char correlation ID for a chunked transfer
// (e.g. a `reminal send` download). crypto/rand so it can't collide with a
// concurrent transfer; uniqueness, not secrecy, is what matters here.
func randomID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// controlSockPath returns the per-agent Unix socket path where the running
// agent listens for control commands like `reminal send <file>`. PID is
// embedded so multiple agents on the same machine don't collide.
func controlSockPath(pid int) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".reminal", fmt.Sprintf("agent-%d.sock", pid)), nil
}

// sendControlTo dials the control socket of the local agent with the given pid
// and sends one newline-delimited command, returning the payload after "ok "
// (or an error). Lets the directory host drive another local session on a remote
// owner's behalf — e.g. renaming a session shown in the Machines panel.
func sendControlTo(pid int, cmd string) (string, error) {
	return sendControlToDeadline(pid, cmd, 0)
}

// sendControlToDeadline is sendControlTo with an optional exchange deadline.
// Zero means no deadline (large `send` transfers). Search / listing verbs
// pass a short one so a hung agent cannot stall the whole fleet query.
func sendControlToDeadline(pid int, cmd string, d time.Duration) (string, error) {
	sock, err := controlSockPath(pid)
	if err != nil {
		return "", err
	}
	var conn net.Conn
	if d > 0 {
		conn, err = net.DialTimeout("unix", sock, d)
	} else {
		conn, err = net.Dial("unix", sock)
	}
	if err != nil {
		return "", fmt.Errorf("connect to agent %d: %w", pid, err)
	}
	defer conn.Close()
	if d > 0 {
		_ = conn.SetDeadline(time.Now().Add(d))
	}
	if _, err := fmt.Fprintln(conn, cmd); err != nil {
		return "", err
	}
	reply, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return "", err
	}
	reply = strings.TrimRight(reply, "\r\n")
	switch {
	case reply == "ok":
		return "", nil
	case strings.HasPrefix(reply, "ok "):
		return strings.TrimPrefix(reply, "ok "), nil
	default:
		return "", fmt.Errorf("agent: %s", strings.TrimSpace(strings.TrimPrefix(reply, "error:")))
	}
}

// listenControl starts a Unix-socket listener that accepts simple
// newline-delimited commands and dispatches them to handlers on the agent.
// Returns a cleanup func that removes the socket file.
func (a *Agent) listenControl() (cleanup func()) {
	path, err := controlSockPath(os.Getpid())
	if err != nil {
		return func() {}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return func() {}
	}
	// Remove any stale socket from a crashed previous agent with our PID
	// (rare — PIDs recycle).
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return func() {}
	}
	_ = os.Chmod(path, 0o600)

	var stopOnce sync.Once
	stop := make(chan struct{})
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				select {
				case <-stop:
					return
				default:
				}
				// Transient error (not a stop): back off so a persistent one (e.g.
				// EMFILE under fd pressure) can't busy-loop this goroutine at 100%
				// CPU. Matches serveMirror's accept loop.
				time.Sleep(50 * time.Millisecond)
				continue
			}
			go a.handleControlConn(conn)
		}
	}()

	return func() {
		stopOnce.Do(func() { close(stop) })
		_ = ln.Close()
		_ = os.Remove(path)
	}
}

// handleControlConn reads one command line and dispatches.
// Protocol is intentionally trivial — one connection per command:
//
//	send <absolute-path>      # broadcast file as TypeDownload to viewers
func (a *Agent) handleControlConn(conn net.Conn) {
	defer conn.Close()
	// Spawned per connection (go a.handleControlConn); a panic here would crash the
	// agent and kill the shell session. Contain it — the command just fails.
	defer func() {
		if rec := recover(); rec != nil {
			recoverLog("handleControlConn", rec)
		}
	}()
	r := bufio.NewReader(conn)
	line, err := r.ReadString('\n')
	if err != nil {
		return
	}
	line = strings.TrimSpace(line)
	switch {
	case strings.HasPrefix(line, "send "):
		path := strings.TrimSpace(strings.TrimPrefix(line, "send "))
		if err := a.broadcastFile(path); err != nil {
			_, _ = fmt.Fprintln(conn, "error:", err)
			return
		}
		_, _ = fmt.Fprintln(conn, "ok")
	case strings.HasPrefix(line, "rename "):
		// Update the live session's display name and re-persist the active
		// record so `reminal list` reflects it right away. An empty name
		// clears it back to unnamed.
		name := strings.TrimSpace(strings.TrimPrefix(line, "rename "))
		a.setName(name)
		_, _ = fmt.Fprintln(conn, "ok")
	case line == "attn":
		// `reminal hook` wrote this session's state: read it now, not when
		// the screen next moves.
		a.kickAttention()
		// An agent's hook, and its restore record has no conversation yet
		// (it has only just started): saved now, so a restore resumes it.
		go a.saveRestoreIfNoConv()
		_, _ = fmt.Fprintln(conn, "ok")
	case line == "note-acts":
		// Drained by `reminal mcp` so viewer-side dismissals reach it.
		_, _ = fmt.Fprintln(conn, "ok", a.recentActs())
	case strings.HasPrefix(line, "notes "):
		// Window annotations published by `reminal mcp`. Whole-state, not deltas.
		raw := strings.TrimSpace(strings.TrimPrefix(line, "notes "))
		if err := a.setNotesJSON(raw); err != nil {
			_, _ = fmt.Fprintln(conn, "error:", err)
			return
		}
		_, _ = fmt.Fprintln(conn, "ok")
	case strings.HasPrefix(line, "keys "):
		if err := a.handleKeysControl(strings.TrimPrefix(line, "keys ")); err != nil {
			_, _ = fmt.Fprintln(conn, "error:", err)
			return
		}
		_, _ = fmt.Fprintln(conn, "ok")
	case line == "transcript":
		body, err := a.handleTranscriptControl()
		if err != nil {
			_, _ = fmt.Fprintln(conn, "error:", err)
			return
		}
		_, _ = fmt.Fprintln(conn, "ok", body)
	case strings.HasPrefix(line, "search "):
		// Regex over this session's live scrollback (stripped of ANSI).
		// Used by MCP search_sessions and by a directory host answering
		// an owner query that carried a pattern.
		body, err := a.handleSearchControl(strings.TrimPrefix(line, "search "))
		if err != nil {
			_, _ = fmt.Fprintln(conn, "error:", err)
			return
		}
		_, _ = fmt.Fprintln(conn, "ok", body)
	case strings.HasPrefix(line, "notify "):
		msg := strings.TrimSpace(strings.TrimPrefix(line, "notify "))
		if err := a.broadcastNotify(msg); err != nil {
			_, _ = fmt.Fprintln(conn, "error:", err)
			return
		}
		_, _ = fmt.Fprintln(conn, "ok")
	case line == "rekey":
		// The owner set may have changed (`reminal owners revoke`, a
		// self-revoke: RekeySessions). Every viewer shares this process's
		// session key, so a device revoked after it handshook keeps it; a
		// restart in place — same PTY and programs, same PIN — makes a new
		// one, and the revoked device's fresh handshake is refused. Only if
		// the owner set is not what this process began with: a burst of
		// revokes already covered by one restart does not restart again.
		//
		// With a new PIN, as repin: a device that owned this machine could
		// have learned the PIN as it was (an owner's Share menu shows it),
		// and a key change alone would let it back in with that. Sent only
		// on a revoke, so any change to the owner stores since this process
		// began is one — even a device added and revoked again since, which
		// leaves the set as it was but had its chance to connect.
		if !ownersChangedSince(a.ownersAt) {
			_, _ = fmt.Fprintln(conn, "ok unchanged")
			return
		}
		if !a.rekeying.CompareAndSwap(false, true) {
			_, _ = fmt.Fprintln(conn, "ok restarting")
			return
		}
		pin, err := session.NewPIN(session.PINLength)
		if err != nil {
			a.rekeying.Store(false)
			_, _ = fmt.Fprintln(conn, "error:", err)
			return
		}
		hash, err := session.HashPIN(pin)
		if err != nil {
			a.rekeying.Store(false)
			_, _ = fmt.Fprintln(conn, "error:", err)
			return
		}
		a.nextPIN, a.nextPinHash = pin, hash
		fallthrough
	case line == "restart":
		// Hot-restart: ack the CLI first (so it doesn't see the WS
		// vanish before getting a response), then schedule the Exec
		// in a goroutine so this handler can return cleanly.
		_, _ = fmt.Fprintln(conn, "ok")
		_ = conn.Close()
		go func() {
			// Tiny delay so the CLI's "ok" line is flushed to the
			// pipe before we replace our process image.
			time.Sleep(50 * time.Millisecond)
			if err := a.executeRestart(); err != nil {
				// Exec failed, so this process is still here — but the
				// tear-down before it already closed the control socket,
				// to free the path for a successor that never arrived.
				// Left that way the session keeps streaming and stops
				// answering the CLI altogether: restart, stop and kill
				// all reach a session through that socket, so the advice
				// below would have been impossible to follow and the
				// session could only be ended by finding its pid. Re-arm
				// it before saying anything.
				a.stopControlFn = a.listenControl()
				a.rekeying.Store(false)
				a.broadcastNotice("restart failed (" + err.Error() +
					") — still running the previous version; try again")
			}
		}()
		return
	case line == "atrest-key":
		// `reminal doctor --repair-key`: the at-rest key this process saves
		// with, so a key file deleted from under running sessions can be
		// put back. Same-user socket; "" when this process has none that
		// matches atrest.json.
		_, _ = fmt.Fprintln(conn, "ok", atrest.CurrentKeyHex())
	case line == "pin":
		// The PIN of this running session, for `reminal info` / list when
		// the copy in its record could not be opened (the keystore locked
		// and no key file to fall back to). The socket is this user's only.
		_, _ = fmt.Fprintln(conn, "ok", a.pin)
	case line == "repin":
		// `reminal repin`: a hot-restart that hands the successor a fresh PIN
		// instead of the current one. The restart already gives the session a
		// new encryption key and makes every viewer handshake again, so after
		// it the old PIN opens nothing; owner devices reconnect on their own.
		pin, err := session.NewPIN(session.PINLength)
		if err != nil {
			_, _ = fmt.Fprintln(conn, "error:", err)
			return
		}
		hash, err := session.HashPIN(pin)
		if err != nil {
			_, _ = fmt.Fprintln(conn, "error:", err)
			return
		}
		a.nextPIN, a.nextPinHash = pin, hash
		_, _ = fmt.Fprintln(conn, "ok")
		_ = conn.Close()
		go func() {
			time.Sleep(50 * time.Millisecond)
			if err := a.executeRestart(); err != nil {
				// Still the old process, so still the old PIN.
				a.nextPIN, a.nextPinHash = "", ""
				a.stopControlFn = a.listenControl()
				a.broadcastNotice("changing the PIN failed (" + err.Error() +
					") — the PIN is unchanged; try again")
			}
		}()
		return
	case line == "quit":
		// `reminal kill`, asked politely. Killing the agent outright is a
		// TerminateProcess on Windows — no signal, no handler, no defers — so
		// the host terminal is never handed back: raw mode stays on, and any
		// mode the session left set keeps mangling every keystroke at the
		// user's prompt. Unwinding Run() instead ends the shell through exactly
		// the path a clean exit takes, restoring the terminal on the way out.
		// The CLI still escalates to a hard kill if we don't die.
		_, _ = fmt.Fprintln(conn, "ok")
		_ = conn.Close()
		a.requestShutdown()
		return
	case line == "sleep" || line == "sleep anyway":
		// `reminal sleep`: end this session's processes the way a shutdown
		// does, its record kept, to wake as itself (sleep.go). The same on
		// every OS: no signal is needed, so Windows sleeps as Unix does.
		if err := a.sleepNow(line == "sleep anyway"); err != nil {
			var lose ErrWouldLose
			if errors.As(err, &lose) {
				js, _ := json.Marshal(lose.Losses)
				_, _ = fmt.Fprintln(conn, "lose:", string(js))
			} else {
				_, _ = fmt.Fprintln(conn, "error:", err)
			}
			return
		}
		_, _ = fmt.Fprintln(conn, "ok")
		_ = conn.Close()
		a.stopSignal.Store(true) // settleRestore keeps the record, as for a shutdown
		// Terminals attached here (reminal connect, attach) are told it sleeps,
		// and wait for it to wake; their sockets are closed now, not left to
		// hold the way out open.
		a.currentConnMu.Lock()
		for c := range a.localConns {
			closeRelayConn(c, "asleep")
		}
		a.currentConnMu.Unlock()
		a.requestShutdown()
		return
	case line == "pause":
		// `reminal stop` on Windows: no SIGUSR1 there, so the pause-broadcast
		// request rides the control socket instead. Same effect as the Unix
		// signal — stop broadcasting, keep the local shell alive.
		a.pause()
		_, _ = fmt.Fprintln(conn, "ok")
	case line == "connections":
		// Return the live viewer connect-timestamp list as JSON on a
		// single line. CLI side prints it as a human-readable table.
		snap := a.snapshotViewers()
		js, err := json.Marshal(snap)
		if err != nil {
			_, _ = fmt.Fprintln(conn, "error:", err)
			return
		}
		_, _ = fmt.Fprintf(conn, "ok %s\n", js)
	case line == "stayunlock on":
		// `reminal settings` toggling "always unlocked" applies live: hold the
		// display-sleep inhibitor now so the host stops idle-locking.
		a.setStayUnlocked(true)
		_, _ = fmt.Fprintln(conn, "ok")
	case line == "stayunlock off":
		a.setStayUnlocked(false)
		_, _ = fmt.Fprintln(conn, "ok")
	default:
		_, _ = fmt.Fprintln(conn, "error: unknown command")
	}
}

// broadcastNotify sends an encrypted notification message to every viewer.
// Web clients fire a browser Notification (with permission); the terminal
// viewer prints the message and rings the bell.
func (a *Agent) broadcastNotify(message string) error {
	if message == "" {
		return errors.New("message required")
	}
	payload, err := json.Marshal(struct {
		Message string `json:"message"`
	}{Message: message})
	if err != nil {
		return err
	}
	enc, err := a.box.Encrypt(payload)
	if err != nil {
		return err
	}
	if err := a.toViewers(protocol.Message{Type: protocol.TypeNotify, Data: enc}); err != nil {
		return err
	}
	a.broadcastNotice("notified viewers: " + message)
	return nil
}

// errNoViewerConn: neither the relay nor a viewer on this machine to write to.
var errNoViewerConn = errors.New("not connected to relay (is the agent paused?)")

// viewerConns is every connection a viewer is reached on: the relay's, and
// each attached on this machine (a `reminal connect` here attaches over the
// local socket, never through the relay).
func (a *Agent) viewerConns() []*websocket.Conn {
	a.currentConnMu.Lock()
	defer a.currentConnMu.Unlock()
	var out []*websocket.Conn
	if a.currentConn != nil {
		out = append(out, a.currentConn)
	}
	for c := range a.localConns {
		out = append(out, c)
	}
	return out
}

func (a *Agent) addLocalConn(c *websocket.Conn) {
	a.currentConnMu.Lock()
	defer a.currentConnMu.Unlock()
	if a.localConns == nil {
		a.localConns = map[*websocket.Conn]struct{}{}
	}
	a.localConns[c] = struct{}{}
}

func (a *Agent) removeLocalConn(c *websocket.Conn) {
	a.currentConnMu.Lock()
	defer a.currentConnMu.Unlock()
	delete(a.localConns, c)
}

// toViewers writes msg to every viewer connection (viewerConns). An error
// only when none took it: one viewer gone is not the others'.
func (a *Agent) toViewers(msg protocol.Message) error {
	conns := a.viewerConns()
	if len(conns) == 0 {
		return errNoViewerConn
	}
	var last error
	sent := 0
	for _, c := range conns {
		if err := a.writeMsg(c, msg); err != nil {
			last = err
			continue
		}
		sent++
	}
	if sent == 0 {
		return last
	}
	return nil
}

// downloadChunkBytes is the raw (pre-base64) payload size of each
// TypeDownload chunk. Cloudflare Durable Objects cap each WS frame at
// 1 MiB on the wire; 256 KB raw leaves ample room after base64 (≈4/3×),
// the AES-GCM nonce/tag, and the JSON envelope. Matches the web client's
// UPLOAD_CHUNK_BYTES so both directions frame identically.
const downloadChunkBytes = 256 * 1024

// downloadMaxBytes caps a single `reminal send`. The agent streams the
// file chunk-by-chunk (peak memory ≈ one chunk), but every viewer
// reassembles the whole file in memory — a browser tab on a phone most
// of all — so we keep a generous-but-finite ceiling rather than letting
// a stray `reminal send /dev/sda` OOM a viewer. Adjust if real transfers
// need more headroom.
const downloadMaxBytes = 100 * 1024 * 1024

// broadcastFile streams the file at path to every connected viewer as a
// sequence of TypeDownload chunks sharing one download_id. Each chunk is
// encrypted with the session crypto box so the relay never sees plaintext.
// Viewers buffer chunks by download_id and reassemble once all arrive
// (mirrors the chunked-upload path in handleUpload).
func (a *Agent) broadcastFile(path string) error {
	if path == "" {
		return errors.New("path required")
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return errors.New("directories aren't supported (try tar first)")
	}
	if info.Size() > downloadMaxBytes {
		return fmt.Errorf("file too large (%s; max %s) — tar+split it, or upload from the viewer side instead",
			humanByteSize(int(info.Size())), humanByteSize(downloadMaxBytes))
	}

	if len(a.viewerConns()) == 0 {
		return errNoViewerConn
	}

	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	name := filepath.Base(path)
	size := int(info.Size())
	// At least one chunk even for an empty file, so the viewer always
	// gets a (name, total=1) frame it can finalize.
	total := (size + downloadChunkBytes - 1) / downloadChunkBytes
	if total == 0 {
		total = 1
	}
	downloadID, err := randomID()
	if err != nil {
		return err
	}

	buf := make([]byte, downloadChunkBytes)
	for index := 0; index < total; index++ {
		n, rerr := io.ReadFull(f, buf)
		if rerr == io.ErrUnexpectedEOF || rerr == io.EOF {
			// Final (short) chunk, or an empty file's single chunk.
			rerr = nil
		}
		if rerr != nil {
			return rerr
		}
		payload, err := json.Marshal(struct {
			DownloadID string `json:"download_id"`
			Index      int    `json:"index"`
			Total      int    `json:"total"`
			Name       string `json:"name"`
			Content    string `json:"content"` // base64 of this chunk
			Size       int    `json:"size"`    // total file size (informational)
		}{
			DownloadID: downloadID,
			Index:      index,
			Total:      total,
			Name:       name,
			Content:    base64.StdEncoding.EncodeToString(buf[:n]),
			Size:       size,
		})
		if err != nil {
			return err
		}
		enc, err := a.box.Encrypt(payload)
		if err != nil {
			return err
		}
		if err := a.toViewers(protocol.Message{Type: protocol.TypeDownload, Data: enc}); err != nil {
			return err
		}
	}

	a.broadcastNotice(fmt.Sprintf("sent %s (%s) to viewers", name, humanByteSize(size)))
	return nil
}

// carriedPIN is the PIN a hot-restart hands to its successor: the current one,
// or the replacement when the restart is a `reminal repin`.
func (a *Agent) carriedPIN() string {
	if a.nextPIN != "" {
		return a.nextPIN
	}
	return a.pin
}

// carriedPinHash pairs with carriedPIN. A session still waiting to move off
// its original relay credential keeps that credential, or the relay would not
// recognise it after the restart.
func (a *Agent) carriedPinHash() string {
	if a.nextPinHash != "" && !a.sendPinHash {
		return a.nextPinHash
	}
	return a.pinHash
}

// localViewerConns are the viewers attached on this machine, not the relay.
func (a *Agent) localViewerConns() []*websocket.Conn {
	a.currentConnMu.Lock()
	defer a.currentConnMu.Unlock()
	out := make([]*websocket.Conn, 0, len(a.localConns))
	for c := range a.localConns {
		out = append(out, c)
	}
	return out
}

// tellLocalViewers tells the viewers attached on this machine the PTY size
// beside the stream, as every viewer was told before the size went in the
// stream. Not the relay's: through the relay a size beside the stream can
// overtake output queued for that socket (applyEffectiveSize).
func (a *Agent) tellLocalViewers(cols, rows uint16) {
	msg, ok := a.sizeMessage(cols, rows)
	if !ok {
		return
	}
	for _, c := range a.localViewerConns() {
		_ = a.writeMsg(c, msg)
	}
}
