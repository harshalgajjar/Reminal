package client

// CLI entry points for `reminal copy` / `reminal paste`, plus the WebSocket
// transport and the human-friendly transfer code. The cryptographic core
// lives in rendezvous.go (transport-agnostic, unit-tested); this file only
// wires it to a relay WebSocket and the command line.

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/reminal/reminal/internal/config"
	"github.com/reminal/reminal/internal/protocol"
)

// DefaultCopyTTL bounds how long a standalone `reminal copy` waits for a
// paste before giving up. The relay enforces its own hard cap independently.
const DefaultCopyTTL = time.Hour

// codeAlphabet excludes visually ambiguous characters (0/O, 1/I/L, U) so a
// code is safe to read aloud or retype. 31 symbols × 8 chars ≈ 40 bits.
const codeAlphabet = "ABCDEFGHJKLMNPQRSTVWXYZ23456789"
const codeLen = 8

// generateCode returns a fresh canonical (uppercase, dash-free) transfer
// code with unbiased symbol selection.
func generateCode() (string, error) {
	out := make([]byte, codeLen)
	// Rejection-sample to avoid modulo bias: 248 is the largest multiple of
	// 31 (len(codeAlphabet)) ≤ 256.
	const limit = 248
	buf := make([]byte, 1)
	for i := 0; i < codeLen; {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		if buf[0] >= limit {
			continue
		}
		out[i] = codeAlphabet[int(buf[0])%len(codeAlphabet)]
		i++
	}
	return string(out), nil
}

// displayCode groups the canonical code with a dash for readability
// (ABCDEFGH → ABCD-EFGH).
func displayCode(code string) string {
	if len(code) != codeLen {
		return code
	}
	return code[:codeLen/2] + "-" + code[codeLen/2:]
}

// normalizeCode canonicalizes user input: uppercase, dashes/spaces stripped.
func normalizeCode(in string) string {
	in = strings.ToUpper(strings.TrimSpace(in))
	var b strings.Builder
	for _, r := range in {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// wsFrameConn adapts a gorilla WebSocket to the frameConn interface used by
// the rendezvous handshake. Writes are mutex-guarded; the handshake is
// strictly request/response so reads need no locking.
type wsFrameConn struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func (w *wsFrameConn) send(msg protocol.Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.conn.WriteMessage(websocket.TextMessage, data)
}

func (w *wsFrameConn) recv() (protocol.Message, error) {
	for {
		_, data, err := w.conn.ReadMessage()
		if err != nil {
			return protocol.Message{}, err
		}
		var msg protocol.Message
		if err := json.Unmarshal(data, &msg); err != nil {
			continue // skip anything that isn't a protocol frame
		}
		return msg, nil
	}
}

// RunCopy is the standalone (no-session) source path: it dials the relay,
// prints the code, and blocks streaming the file to the first paste that
// proves the code — or exits when ttl elapses. Used when REMINAL_SESSION is
// absent; the in-session path hands off to the agent instead.
func RunCopy(path string, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = DefaultCopyTTL
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return errors.New("directories aren't supported (try tar first)")
	}
	if info.Size() > downloadMaxBytes {
		return fmt.Errorf("file too large (%s; max %s) — tar+split it first",
			humanByteSize(int(info.Size())), humanByteSize(downloadMaxBytes))
	}

	code, err := generateCode()
	if err != nil {
		return err
	}
	url := config.RendezvousWS(code, "source")
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		return fmt.Errorf("connect to relay: %w", err)
	}
	defer conn.Close()
	fc := &wsFrameConn{conn: conn}

	fmt.Printf("Copy code: %s\n", displayCode(code))
	fmt.Printf("On the other machine, run:  reminal paste %s\n", displayCode(code))
	fmt.Printf("Waiting for paste (expires in %s, Ctrl-C to cancel)…\n", ttl)

	done := make(chan error, 1)
	go func() { done <- runSource(fc, code, path) }()

	select {
	case err := <-done:
		if err != nil {
			return err
		}
		fmt.Println("Sent.")
		return nil
	case <-time.After(ttl):
		conn.Close() // unblocks the goroutine's pending read
		return fmt.Errorf("code expired after %s — no paste arrived", ttl)
	}
}

// RunPaste is the paste path on any terminal: dial, run the handshake with
// the code, receive the file, write it to dest (default "."). A mistyped,
// expired, consumed, or never-existed code all surface as the same
// deliberately-merged message.
func RunPaste(codeInput, dest string) error {
	code := normalizeCode(codeInput)
	if code == "" {
		return errors.New("usage: reminal paste <code> [destination]")
	}
	if dest == "" {
		dest = "."
	}
	url := config.RendezvousWS(code, "paste")
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		return fmt.Errorf("connect to relay: %w", err)
	}
	defer conn.Close()
	fc := &wsFrameConn{conn: conn}

	path, err := runPaste(fc, code, dest)
	if err != nil {
		// errWrongCode and errCodeNotLive both collapse to the merged
		// message: don't reveal whether the code was ever real.
		if errors.Is(err, errWrongCode) || errors.Is(err, errCodeNotLive) {
			return errors.New("code is either too old or invalid")
		}
		return err
	}
	fmt.Printf("Saved %s\n", path)
	return nil
}
