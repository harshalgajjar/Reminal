// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/reminal/reminal/internal/config"
	"github.com/reminal/reminal/internal/crypto"
	"github.com/reminal/reminal/internal/protocol"
	"github.com/reminal/reminal/internal/session"
	"golang.org/x/term"
)

// escapeKey is the byte that disconnects the viewer when typed at the
// keyboard. 0x1d (Ctrl-]) is telnet's traditional escape character and is
// almost never bound in modern shells, so it virtually never collides with
// what the remote shell wants to receive.
const escapeKey = 0x1d

// readDeadline bounds how long a WebSocket read can sit idle before we give
// up and trigger a reconnect. With pings every 30s under normal conditions
// we'll always see traffic well inside the window; a stuck read means the
// TCP connection is half-open (silently dropped by a middlebox) and the OS
// hasn't noticed yet. Mirrors ssh's ServerAliveInterval/CountMax behaviour.
const readDeadline = 60 * time.Second

type Viewer struct {
	// Atomics first for 64-bit alignment on 32-bit architectures.
	lastSeq       uint64
	droppedChunks uint64
	bytesSent     uint64 // plaintext stdin bytes forwarded to the agent
	bytesReceived uint64 // plaintext shell output received from the agent

	sessionID string
	pin       string
	box       *crypto.Box

	// pendingDownloads buffers in-flight chunked downloads (from a host
	// `reminal send`) keyed by download_id, until every chunk arrives.
	// Guarded by downloadsMu. Mirrors the agent's pendingUploads.
	downloadsMu      sync.Mutex
	pendingDownloads map[string]*pendingDownload

	writeMu   sync.Mutex
	helloOnce sync.Once // first-connect "Connected …" line; suppressed on reconnect
	// connectTime is set inside helloOnce; zero value means we never
	// actually connected (auth failed immediately, etc.), so we skip the
	// disconnect summary.
	connectTime time.Time
}

func NewViewer(sessionID, pin string) (*Viewer, error) {
	sessionID = strings.ToUpper(strings.TrimSpace(sessionID))
	pin = strings.TrimSpace(pin)
	if sessionID == "" {
		return nil, fmt.Errorf("session ID required")
	}
	if err := session.ValidatePIN(pin); err != nil {
		return nil, err
	}
	// box is established per WebSocket connection via the EKE
	// handshake in negotiateSessionKey (see runConnection).
	return &Viewer{
		sessionID:        sessionID,
		pin:              pin,
		pendingDownloads: make(map[string]*pendingDownload),
	}, nil
}

func Connect(sessionID, pin string) error {
	v, err := NewViewer(sessionID, pin)
	if err != nil {
		return err
	}
	return v.Run()
}

func (v *Viewer) Run() error {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return fmt.Errorf("stdin is not a terminal")
	}

	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("enable raw mode: %w", err)
	}
	defer term.Restore(fd, oldState)

	setRemoteIndicator(v.sessionID)
	defer clearRemoteIndicator()
	defer v.printDisconnectSummary()

	// One stdin reader for the lifetime of the viewer; per-connection
	// goroutines drain from this channel. A bounded buffer means we drop
	// rather than block during disconnects — blocking would let a user's
	// `rm -rf` typed mid-blackout replay on reconnect long after they
	// walked away. The drop count is surfaced on the next successful
	// connection so it's never silent (see notifyDropped).
	stdinCh := make(chan []byte, 256)
	stdinDone := make(chan struct{})
	// escapeCh fires when the user presses Ctrl-] (telnet's escape key,
	// almost never bound in modern shells). In raw mode local Ctrl+C is
	// forwarded to the remote shell, so this is the only way to cleanly
	// quit the viewer without killing the terminal window.
	escapeCh := make(chan struct{})
	go func() {
		defer close(stdinDone)
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				if i := bytes.IndexByte(buf[:n], escapeKey); i >= 0 {
					// Flush any bytes typed before the escape so the user's
					// last keystrokes aren't lost. Drop anything after; the
					// user's intent is to leave.
					if i > 0 {
						chunk := make([]byte, i)
						copy(chunk, buf[:i])
						select {
						case stdinCh <- chunk:
						default:
							atomic.AddUint64(&v.droppedChunks, 1)
						}
					}
					close(escapeCh)
					return
				}
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				select {
				case stdinCh <- chunk:
				default:
					atomic.AddUint64(&v.droppedChunks, 1)
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// SIGWINCH → resize notifications.
	winCh := make(chan os.Signal, 1)
	signal.Notify(winCh, syscall.SIGWINCH)
	defer signal.Stop(winCh)

	// Ctrl+C / SIGTERM → clean exit.
	intCh := make(chan os.Signal, 1)
	signal.Notify(intCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(intCh)

	backoff := initialBackoff
	first := true
	for {
		select {
		case <-intCh:
			fmt.Fprint(os.Stderr, "\r\n")
			return nil
		case <-escapeCh:
			return nil
		case <-stdinDone:
			return io.EOF
		default:
		}

		if first {
			v.notify("Connecting…  (press Ctrl-] to disconnect)")
		} else {
			v.notify(fmt.Sprintf("Reconnecting…"))
		}

		start := time.Now()
		err := v.runConnection(stdinCh, winCh, intCh, escapeCh)
		select {
		case <-intCh:
			fmt.Fprint(os.Stderr, "\r\n")
			return nil
		case <-escapeCh:
			return nil
		default:
		}

		// Fatal errors should propagate up; transient errors trigger reconnect.
		var fatal *fatalErr
		if errors.As(err, &fatal) {
			return fatal.err
		}

		if time.Since(start) > stableThresh {
			backoff = initialBackoff
		}

		// Rate-limited path: switch to a long pause so reconnect spam
		// doesn't extend Cloudflare's throttle window. Same handling
		// as the agent.
		var rl *rateLimitedError
		if errors.As(err, &rl) {
			wait := rl.retryAfter
			if wait < rateLimitMinWait {
				wait = rateLimitMinWait
			}
			v.notify(humanize(err))
			select {
			case <-intCh:
				fmt.Fprint(os.Stderr, "\r\n")
				return nil
			case <-escapeCh:
				return nil
			case <-time.After(wait):
			}
			first = false
			backoff = initialBackoff
			continue
		}

		v.notify(fmt.Sprintf("%s Reconnecting in %v…", humanize(err), backoff))

		select {
		case <-intCh:
			fmt.Fprint(os.Stderr, "\r\n")
			return nil
		case <-escapeCh:
			return nil
		case <-time.After(backoff):
		}
		first = false
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// fatalErr wraps an error that should not trigger reconnect (e.g., bad PIN,
// session expired, lockout). The viewer exits when one is returned.
type fatalErr struct{ err error }

func (e *fatalErr) Error() string { return e.err.Error() }
func (e *fatalErr) Unwrap() error { return e.err }

func (v *Viewer) runConnection(stdinCh <-chan []byte, winCh <-chan os.Signal, intCh <-chan os.Signal, escapeCh <-chan struct{}) error {
	wsURL := config.SessionWS(v.sessionID, string(protocol.RoleViewer))
	dialStart := time.Now()
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		if resp != nil && resp.StatusCode == 429 {
			return &rateLimitedError{retryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
		}
		return fmt.Errorf("dial: %w", err)
	}
	dialTime := time.Since(dialStart)
	defer conn.Close()

	if err := v.authenticate(conn); err != nil {
		// Auth failures are fatal — wrong PIN, locked out, mismatched session.
		return &fatalErr{err: err}
	}

	if err := v.negotiateSessionKey(conn); err != nil {
		// EKE failures are fatal when the cause is "wrong PIN" (the
		// AES-GCM unwrap tag rejects). For network/transient failures
		// we'd ideally reconnect, but since we can't tell them apart
		// from the wrap error alone, surface as fatal so the user
		// sees the right message; the auto-reconnect loop in Run()
		// kicks in for genuinely-transient cases (the conn would
		// close before we reach this point).
		return &fatalErr{err: err}
	}

	// One-time "Connected …" line on the first successful connect. Trust
	// signal (encryption named explicitly), diagnostic (handshake time so
	// users know if the relay is sluggish), and UX (Ctrl-] hint repeated).
	// On reconnect we print a SHORTER but UNMISSABLE confirmation so the
	// user doesn't have to guess "is it back?" — without this they'd see
	// only the reconnect-attempt notices and have to type-and-hope.
	isReconnect := !v.connectTime.IsZero()
	v.helloOnce.Do(func() {
		v.connectTime = time.Now()
		v.notify(fmt.Sprintf("Connected to %s · handshake %v · AES-256-GCM end-to-end · Ctrl-] to disconnect",
			v.sessionID, dialTime.Round(time.Millisecond)))
	})
	if isReconnect {
		v.notify(fmt.Sprintf("Reconnected to %s — back online. Press Enter to refresh the prompt.",
			v.sessionID))
	}

	// If we dropped any stdin bytes while reconnecting, surface that now —
	// the user needs to know to retype anything important. Reset on every
	// successful authenticate so the notification reflects the most recent
	// blackout, not lifetime totals.
	if n := atomic.SwapUint64(&v.droppedChunks, 0); n > 0 {
		v.notify(fmt.Sprintf("%d input chunk(s) dropped during the disconnect — retype anything you typed while offline.", n))
	}

	// agentLive tracks whether the relay says the agent is currently connected.
	// We start optimistic; the relay corrects us with agent_offline if needed.
	var agentLive atomic.Bool
	agentLive.Store(true)

	// On (re)connect, resync the terminal size FIRST so any SIGWINCH-
	// triggered redraw on the agent is included in the scrollback replay
	// the agent is about to send. Then request the replay.
	v.sendResizeNow(conn)
	if err := v.sendResume(conn); err != nil {
		return err
	}

	readerDone := make(chan error, 1)
	go func() {
		readerDone <- v.runReader(conn, &agentLive)
	}()

	pingTicker := time.NewTicker(30 * time.Second)
	defer pingTicker.Stop()

	for {
		select {
		case <-intCh:
			return &fatalErr{err: errors.New("interrupted")}
		case <-escapeCh:
			return &fatalErr{err: errors.New("disconnected")}
		case err := <-readerDone:
			if err == nil {
				err = errors.New("relay closed")
			}
			return err
		case data, ok := <-stdinCh:
			if !ok {
				return &fatalErr{err: io.EOF}
			}
			if !agentLive.Load() {
				// Drop input when the agent is offline; otherwise it would
				// be silently consumed by the relay. Count so we can warn
				// the user on the next agent_online transition.
				atomic.AddUint64(&v.droppedChunks, 1)
				continue
			}
			enc, err := v.box.Encrypt(data)
			if err != nil {
				return err
			}
			if err := v.writeMsg(conn, protocol.Message{Type: protocol.TypeData, Data: enc}); err != nil {
				return err
			}
			atomic.AddUint64(&v.bytesSent, uint64(len(data)))
		case <-winCh:
			v.sendResizeNow(conn)
		case <-pingTicker.C:
			if err := v.writeMsg(conn, protocol.Message{Type: protocol.TypePing}); err != nil {
				return err
			}
		}
	}
}

// kexTimeout bounds the EKE handshake. Generous enough to survive a
// slow agent reply over a sluggish relay, short enough that a stuck
// handshake doesn't pin a user-visible terminal forever.
const kexTimeout = 15 * time.Second

// negotiateSessionKey runs the v2 PIN-authenticated X25519 handshake.
// Replaces the deterministic deriveKey(sessionID, pin) used in v1,
// which gave a relay-recorded ciphertext frame only ~20 bits of
// secrecy against offline brute force. See internal/crypto/kex.go.
func (v *Viewer) negotiateSessionKey(conn *websocket.Conn) error {
	eph, err := crypto.NewEphemeralKey()
	if err != nil {
		return fmt.Errorf("kex: keygen: %w", err)
	}
	exIDHex, exID, err := crypto.NewExID()
	if err != nil {
		return fmt.Errorf("kex: ex_id: %w", err)
	}
	blinded, err := crypto.BlindPub(eph.PublicKey().Bytes(), v.pin)
	if err != nil {
		return fmt.Errorf("kex: blind: %w", err)
	}
	if err := v.writeMsg(conn, protocol.Message{
		Type: protocol.TypeKexInit,
		ExID: exIDHex,
		Data: base64.StdEncoding.EncodeToString(blinded),
	}); err != nil {
		return fmt.Errorf("kex: send: %w", err)
	}

	deadline := time.Now().Add(kexTimeout)
	for {
		_ = conn.SetReadDeadline(deadline)
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("kex: %w", err)
		}
		var msg protocol.Message
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		switch msg.Type {
		case protocol.TypeError:
			return fmt.Errorf("%s", msg.Error)
		case protocol.TypeKexResp:
			if msg.ExID != exIDHex {
				// Broadcast for some other viewer's handshake; ignore.
				continue
			}
			blindedAgent, err := base64.StdEncoding.DecodeString(msg.Data)
			if err != nil || len(blindedAgent) != crypto.PubKeyBytes {
				return fmt.Errorf("kex: bad agent key encoding")
			}
			agentPub, err := crypto.UnblindPub(blindedAgent, v.pin)
			if err != nil {
				return fmt.Errorf("kex: unblind: %w", err)
			}
			peerKey, err := crypto.PeerPublicKey(agentPub)
			if err != nil {
				return fmt.Errorf("kex: invalid agent key")
			}
			shared, err := eph.ECDH(peerKey)
			if err != nil {
				return fmt.Errorf("kex: ecdh: %w", err)
			}
			wrapped, err := base64.StdEncoding.DecodeString(msg.Wrap)
			if err != nil {
				return fmt.Errorf("kex: bad wrap encoding")
			}
			sessionKey, err := crypto.UnwrapSessionKey(shared, exID, wrapped)
			if err != nil {
				// AES-GCM tag mismatch — the agent and we derived
				// different wrap keys. With ECDH-shared agreed on,
				// the only place that can diverge is the PIN
				// blinding step, so this means PIN mismatch (either
				// user typo, or an active relay MITM that guessed
				// the PIN wrong on this attempt).
				return fmt.Errorf("handshake failed: PIN mismatch or relay tampering")
			}
			box, err := crypto.NewBox(sessionKey)
			if err != nil {
				return fmt.Errorf("kex: box: %w", err)
			}
			v.box = box
			// Clear the deadline so the normal runReader's own
			// per-read deadline takes over.
			_ = conn.SetReadDeadline(time.Time{})
			return nil
		default:
			// TypeConnected / TypeAgentOnline / TypePing etc. can
			// arrive in this window. The reader loop hasn't started
			// yet, so we'd otherwise lose them — but they're idempotent
			// signals the post-EKE setup re-derives (sendResume +
			// sendResizeNow re-publish viewport, agentLive starts
			// optimistically), so dropping them here is safe.
		}
	}
}

func (v *Viewer) authenticate(conn *websocket.Conn) error {
	// The PIN is deliberately NOT sent to the relay — it authenticates
	// end-to-end via the EKE (negotiateSessionKey), so an untrusted relay never
	// sees it and can't MITM the handshake. The relay auths a viewer on the
	// session being live; a wrong PIN fails the EKE unwrap, which
	// negotiateSessionKey surfaces as a fatal "PIN mismatch".
	if err := v.writeMsg(conn, protocol.Message{Type: protocol.TypeAuth}); err != nil {
		return err
	}

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("auth: %w", err)
		}

		var msg protocol.Message
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}

		switch msg.Type {
		case protocol.TypeError:
			return fmt.Errorf("%s", msg.Error)
		case protocol.TypeAuthOK:
			return nil
		}
	}
}

func (v *Viewer) runReader(conn *websocket.Conn, agentLive *atomic.Bool) error {
	for {
		_ = conn.SetReadDeadline(time.Now().Add(readDeadline))
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return err
		}

		var msg protocol.Message
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}

		switch msg.Type {
		case protocol.TypeData:
			data, err := v.box.Decrypt(msg.Data)
			if err != nil {
				continue
			}
			// With multiple viewers, a late-joiner's resume request can
			// cause the agent to replay scrollback that we've already
			// seen. Skip anything with a seq <= our high-water mark.
			// Exception: if seq goes dramatically backward (or hits 1)
			// the source agent restarted in place and its seq counter
			// reset; reset our high-water mark so we don't silently
			// filter the new stream forever.
			if msg.Seq > 0 {
				for {
					cur := atomic.LoadUint64(&v.lastSeq)
					if msg.Seq == 1 || msg.Seq+32 < cur {
						// Restart-detected: reset and accept this msg.
						if atomic.CompareAndSwapUint64(&v.lastSeq, cur, msg.Seq) {
							break
						}
						continue
					}
					if msg.Seq <= cur {
						msg.Seq = 0 // signal "skip" below
						break
					}
					if atomic.CompareAndSwapUint64(&v.lastSeq, cur, msg.Seq) {
						break
					}
				}
				if msg.Seq == 0 {
					continue
				}
			}
			if _, err := os.Stdout.Write(data); err != nil {
				return err
			}
			atomic.AddUint64(&v.bytesReceived, uint64(len(data)))
		case protocol.TypeConnected, protocol.TypeAgentOnline:
			if !agentLive.Swap(true) {
				// Agent reattached. The new process image (after a
				// `reminal restart` hot-exec) keeps the session ID
				// and PIN but generates a fresh sessionKey — our
				// cached AES box is now decrypting against a dead
				// key, so every subsequent data frame fails silently
				// and the terminal appears frozen. Closing the WS
				// here makes runConnection's outer reconnect loop
				// dial a fresh socket, which re-runs the EKE and
				// picks up the new sessionKey transparently.
				v.notify("Agent reconnected — re-keying.")
				if n := atomic.SwapUint64(&v.droppedChunks, 0); n > 0 {
					v.notify(fmt.Sprintf("%d input chunk(s) dropped while the agent was offline — retype if needed.", n))
				}
				return nil
			}
			// Same-agent reattach (no offline→online edge): just
			// re-sync resume position + viewport.
			_ = v.sendResume(conn)
			v.sendResizeNow(conn)
		case protocol.TypeAgentOffline:
			if agentLive.Swap(false) {
				v.notify("Agent offline — waiting…")
			}
		case protocol.TypeResize:
			// Agent telling us the PTY's effective size, OR sending a
			// (0,0) sentinel to ask us to re-publish our own size.
			// The latter happens when another viewer disconnects and
			// the agent needs the remaining viewers' dimensions to
			// decide whether to grow the PTY back.
			plaintext, err := v.box.Decrypt(msg.Data)
			if err != nil {
				continue
			}
			var rs protocol.Message
			if json.Unmarshal(plaintext, &rs) == nil && rs.Cols == 0 && rs.Rows == 0 {
				v.sendResizeNow(conn)
			}
			// Non-zero broadcasts of the agent's authoritative size
			// are informational for the Go viewer (it always renders
			// at the local terminal's actual cols/rows, which the
			// SIGWINCH handler keeps in sync); no action needed.
		case protocol.TypeClosed:
			text := msg.Error
			if text == "" {
				text = "session ended"
			}
			return &fatalErr{err: fmt.Errorf("%s", text)}
		case protocol.TypeError:
			return &fatalErr{err: fmt.Errorf("%s", msg.Error)}
		case protocol.TypePing:
			_ = v.writeMsg(conn, protocol.Message{Type: protocol.TypePong})
		case protocol.TypeDownload:
			plaintext, err := v.box.Decrypt(msg.Data)
			if err != nil {
				v.notify(fmt.Sprintf("download failed: decrypt: %v", err))
				continue
			}
			v.handleDownload(plaintext)
		case protocol.TypeNotify:
			plaintext, err := v.box.Decrypt(msg.Data)
			if err != nil {
				continue
			}
			var n struct {
				Message string `json:"message"`
			}
			if json.Unmarshal(plaintext, &n) == nil && n.Message != "" {
				// Bell + dim line so the user gets both an audible cue
				// and visible context. xterm.js and most terminals ring
				// the bell on \x07.
				v.notify(fmt.Sprintf("\x07notify: %s", n.Message))
			}
		}
	}
}

func (v *Viewer) sendResume(conn *websocket.Conn) error {
	return v.writeMsg(conn, protocol.Message{
		Type:    protocol.TypeResume,
		FromSeq: atomic.LoadUint64(&v.lastSeq),
	})
}

func (v *Viewer) sendResizeNow(conn *websocket.Conn) {
	cols, rows, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		return
	}
	enc, err := encryptResize(v.box, uint16(cols), uint16(rows))
	if err != nil {
		return
	}
	_ = v.writeMsg(conn, protocol.Message{Type: protocol.TypeResize, Data: enc})
}

func (v *Viewer) writeMsg(conn *websocket.Conn, msg protocol.Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	v.writeMu.Lock()
	defer v.writeMu.Unlock()
	return conn.WriteMessage(websocket.TextMessage, data)
}

// notify writes a one-line dim status to stderr. It uses \r and ANSI dim to
// minimize disruption to the live terminal output below.
func (v *Viewer) notify(text string) {
	fmt.Fprintf(os.Stderr, "\r\n\x1b[2m[reminal] %s\x1b[0m\r\n", text)
}

// pendingDownload buffers in-flight chunks for a single chunked download
// (a host `reminal send`). Reassembled once len(chunks) == total, or
// dropped if no chunk arrives within uploadStaleTimeout.
type pendingDownload struct {
	name       string
	total      int
	chunks     map[int][]byte
	staleTimer *time.Timer
}

// handleDownload decrypts a TypeDownload payload (already decrypted at the
// call site), parses it, and either writes a single-shot file straight to
// disk or routes a chunk into the pendingDownloads buffer. The chunked path
// mirrors the agent-side handleUpload semantics so files move both ways
// without surprises.
func (v *Viewer) handleDownload(plaintext []byte) {
	var payload struct {
		DownloadID string `json:"download_id"`
		Index      int    `json:"index"`
		Total      int    `json:"total"`
		Name       string `json:"name"`
		Content    string `json:"content"` // base64 of this chunk
		Size       int    `json:"size"`
	}
	if err := json.Unmarshal(plaintext, &payload); err != nil {
		v.notify(fmt.Sprintf("download failed: parse: %v", err))
		return
	}
	if payload.Name == "" {
		v.notify("download failed: missing filename")
		return
	}
	safe := filepath.Base(payload.Name)
	chunk, err := base64.StdEncoding.DecodeString(payload.Content)
	if err != nil {
		v.notify(fmt.Sprintf("download failed: bad base64: %v", err))
		return
	}

	// Single-shot path: legacy agents (no download_id) and small files
	// that fit in one chunk. Write immediately, bypassing the buffer.
	if payload.DownloadID == "" || payload.Total <= 1 {
		v.writeIncoming(safe, chunk)
		return
	}
	if payload.Index < 0 || payload.Index >= payload.Total {
		v.notify(fmt.Sprintf("download failed: bad chunk index %d/%d", payload.Index, payload.Total))
		return
	}

	v.downloadsMu.Lock()
	dl, ok := v.pendingDownloads[payload.DownloadID]
	if !ok {
		dl = &pendingDownload{
			name:   safe,
			total:  payload.Total,
			chunks: make(map[int][]byte, payload.Total),
		}
		id := payload.DownloadID
		dl.staleTimer = time.AfterFunc(uploadStaleTimeout, func() {
			v.downloadsMu.Lock()
			stale, still := v.pendingDownloads[id]
			if still {
				delete(v.pendingDownloads, id)
			}
			v.downloadsMu.Unlock()
			if still {
				v.notify(fmt.Sprintf("download %q timed out after %s (%d/%d chunks)",
					stale.name, uploadStaleTimeout, len(stale.chunks), stale.total))
			}
		})
		v.pendingDownloads[payload.DownloadID] = dl
		v.notify(fmt.Sprintf("receiving %s (%d chunks)…", safe, payload.Total))
	}
	// Late chunks reset the stale timer. Duplicates are ignored.
	if _, dup := dl.chunks[payload.Index]; !dup {
		dl.chunks[payload.Index] = chunk
	}
	dl.staleTimer.Reset(uploadStaleTimeout)
	complete := len(dl.chunks) == dl.total
	if complete {
		dl.staleTimer.Stop()
		delete(v.pendingDownloads, payload.DownloadID)
	}
	v.downloadsMu.Unlock()

	if !complete {
		return
	}

	// Assemble in index order.
	totalBytes := 0
	for _, c := range dl.chunks {
		totalBytes += len(c)
	}
	assembled := make([]byte, 0, totalBytes)
	for i := 0; i < dl.total; i++ {
		assembled = append(assembled, dl.chunks[i]...)
	}
	v.writeIncoming(dl.name, assembled)
}

// writeIncoming saves a fully-received download to ~/Downloads/reminal-incoming/,
// deduplicating the filename. Shared by the single-shot and chunked paths.
func (v *Viewer) writeIncoming(safe string, raw []byte) {
	home, err := os.UserHomeDir()
	if err != nil {
		v.notify(fmt.Sprintf("download failed: %v", err))
		return
	}
	dir := filepath.Join(home, "Downloads", "reminal-incoming")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		v.notify(fmt.Sprintf("download failed: mkdir: %v", err))
		return
	}
	path := uniquePath(filepath.Join(dir, safe))
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		v.notify(fmt.Sprintf("download failed: write: %v", err))
		return
	}
	v.notify(fmt.Sprintf("downloaded %s (%d bytes)", path, len(raw)))
}

// printDisconnectSummary prints a one-line wrap-up on viewer exit. Skipped if
// we never actually connected (auth failure, network never came up) so a
// rapid-fail run doesn't pollute the user's terminal with bogus stats.
func (v *Viewer) printDisconnectSummary() {
	if v.connectTime.IsZero() {
		// Never actually connected (auth failure, network never came up,
		// or user Ctrl-]'d during the Connecting… line). Still acknowledge
		// the exit so the prompt doesn't appear out of nowhere.
		fmt.Fprint(os.Stderr, "\r\n\x1b[2m[reminal] Disconnected.\x1b[0m\r\n")
		return
	}
	dur := time.Since(v.connectTime).Round(time.Second)
	sent := humanBytes(atomic.LoadUint64(&v.bytesSent))
	recv := humanBytes(atomic.LoadUint64(&v.bytesReceived))
	fmt.Fprintf(os.Stderr, "\x1b[2m[reminal] Disconnected from %s after %v · sent %s · received %s\x1b[0m\r\n",
		v.sessionID, dur, sent, recv)
}

// humanBytes renders byte counts with three significant digits in the largest
// unit that still keeps the integer part to ≤4 digits: 999 B, 1.23 KB, 47 MB.
// Powers of 1024 (KiB-style) would be more accurate but most users read 1KB
// as 1000 bytes; matches `du -h` / `ls -h` convention.
func humanBytes(n uint64) string {
	const (
		kb = 1000
		mb = 1000 * kb
		gb = 1000 * mb
	)
	switch {
	case n < kb:
		return fmt.Sprintf("%d B", n)
	case n < mb:
		return fmt.Sprintf("%.2f KB", float64(n)/kb)
	case n < gb:
		return fmt.Sprintf("%.2f MB", float64(n)/mb)
	default:
		return fmt.Sprintf("%.2f GB", float64(n)/gb)
	}
}
