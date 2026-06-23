package client

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/reminal/reminal/internal/protocol"
)

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
	case strings.HasPrefix(line, "notify "):
		msg := strings.TrimSpace(strings.TrimPrefix(line, "notify "))
		if err := a.broadcastNotify(msg); err != nil {
			_, _ = fmt.Fprintln(conn, "error:", err)
			return
		}
		_, _ = fmt.Fprintln(conn, "ok")
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
				// Exec failed — we're still alive. Surface the
				// failure to viewers + host so the user knows
				// they need to manually restart.
				a.broadcastNotice("restart failed: " + err.Error())
			}
		}()
		return
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
	a.currentConnMu.Lock()
	conn := a.currentConn
	a.currentConnMu.Unlock()
	if conn == nil {
		return errors.New("not connected to relay (is the agent paused?)")
	}
	if err := a.writeMsg(conn, protocol.Message{Type: protocol.TypeNotify, Data: enc}); err != nil {
		return err
	}
	a.broadcastNotice("notified viewers: " + message)
	return nil
}

// broadcastFile reads the file at path and ships it to every connected viewer
// as a TypeDownload message. Encryption uses the session's crypto box so the
// relay never sees plaintext content.
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
	const maxBytes = 25 * 1024 * 1024
	if info.Size() > maxBytes {
		return fmt.Errorf("file too large (%d bytes; max %d)", info.Size(), maxBytes)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(struct {
		Name    string `json:"name"`
		Content string `json:"content"`
		Size    int    `json:"size"`
	}{
		Name:    filepath.Base(path),
		Content: base64.StdEncoding.EncodeToString(raw),
		Size:    len(raw),
	})
	if err != nil {
		return err
	}
	enc, err := a.box.Encrypt(payload)
	if err != nil {
		return err
	}
	a.currentConnMu.Lock()
	conn := a.currentConn
	a.currentConnMu.Unlock()
	if conn == nil {
		return errors.New("not connected to relay (is the agent paused?)")
	}
	if err := a.writeMsg(conn, protocol.Message{Type: protocol.TypeDownload, Data: enc}); err != nil {
		return err
	}
	a.broadcastNotice(fmt.Sprintf("sent %s (%s) to viewers", filepath.Base(path), humanByteSize(len(raw))))
	return nil
}
