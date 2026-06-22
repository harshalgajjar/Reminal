package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
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
	box, err := crypto.NewBox(sessionID, pin)
	if err != nil {
		return nil, err
	}
	return &Viewer{sessionID: sessionID, pin: pin, box: box}, nil
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
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	dialTime := time.Since(dialStart)
	defer conn.Close()

	if err := v.authenticate(conn); err != nil {
		// Auth failures are fatal — wrong PIN, locked out, mismatched session.
		return &fatalErr{err: err}
	}

	// One-time "Connected …" line on the first successful connect. Trust
	// signal (encryption named explicitly), diagnostic (handshake time so
	// users know if the relay is sluggish), and UX (Ctrl-] hint repeated).
	v.helloOnce.Do(func() {
		v.connectTime = time.Now()
		v.notify(fmt.Sprintf("Connected to %s · handshake %v · AES-256-GCM end-to-end · Ctrl-] to disconnect",
			v.sessionID, dialTime.Round(time.Millisecond)))
	})

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

func (v *Viewer) authenticate(conn *websocket.Conn) error {
	if err := v.writeMsg(conn, protocol.Message{Type: protocol.TypeAuth, Pin: v.pin}); err != nil {
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
			if msg.Seq > 0 {
				skip := false
				for {
					cur := atomic.LoadUint64(&v.lastSeq)
					if msg.Seq <= cur {
						skip = true
						break
					}
					if atomic.CompareAndSwapUint64(&v.lastSeq, cur, msg.Seq) {
						break
					}
				}
				if skip {
					continue
				}
			}
			if _, err := os.Stdout.Write(data); err != nil {
				return err
			}
			atomic.AddUint64(&v.bytesReceived, uint64(len(data)))
		case protocol.TypeConnected, protocol.TypeAgentOnline:
			if !agentLive.Swap(true) {
				v.notify("Agent reconnected.")
				if n := atomic.SwapUint64(&v.droppedChunks, 0); n > 0 {
					v.notify(fmt.Sprintf("%d input chunk(s) dropped while the agent was offline — retype if needed.", n))
				}
			}
			// Re-sync after agent reattach.
			_ = v.sendResume(conn)
			v.sendResizeNow(conn)
		case protocol.TypeAgentOffline:
			if agentLive.Swap(false) {
				v.notify("Agent offline — waiting…")
			}
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
