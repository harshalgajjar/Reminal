package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/mdp/qrterminal/v3"
	"github.com/reminal/reminal/internal/config"
	"github.com/reminal/reminal/internal/crypto"
	"github.com/reminal/reminal/internal/protocol"
	"github.com/reminal/reminal/internal/pty"
	"github.com/reminal/reminal/internal/session"
	xterm "golang.org/x/term"
)

// scrollbackBytes caps the in-memory replay buffer. 2 MiB is enough for a
// full screen of escape codes plus thousands of lines of plain output.
const scrollbackBytes = 2 * 1024 * 1024

// reconnect timing
const (
	initialBackoff = 1 * time.Second
	maxBackoff     = 30 * time.Second
	stableThresh   = 30 * time.Second
	// readDeadlineAgent bounds how long an agent-side WS read can sit idle
	// before we declare the connection dead and reconnect. With viewer
	// pings every 30s under normal operation this is well above the noise
	// floor; it mainly catches half-open TCP that the OS hasn't noticed.
	readDeadlineAgent = 60 * time.Second
)

type Agent struct {
	sessionID string
	pin       string
	pinHash   string
	webURL    string
	shell     string
	version   string // running binary's version, shown in banner + exit summary
	box       *crypto.Box
	buf       *scrollback
	term      *pty.Session

	writeMu sync.Mutex // serializes WS writes; safe across sender/reader goroutines

	// localActive gates whether pumpPTY echoes shell output to the host's
	// stdout. Set when Run() puts the local terminal into raw-attached mode
	// so the user can drive the shell from the agent's terminal directly.
	localActive bool
	// hostEscape closes when the host user presses Ctrl-]; the main loop
	// treats it the same as a graceful shutdown signal so defers still run.
	hostEscape chan struct{}

	// startedAt is set once in Run() and used to keep session.Active's
	// started_at stable when we rewrite the file on viewer-count changes.
	startedAt time.Time
	// paused is set by `reminal stop` (via SIGUSR1). When set, the main
	// reconnect loop stops trying to reach the relay and the local shell
	// keeps running on the host terminal as a plain interactive session.
	paused atomic.Bool
	// currentConnMu guards currentConn so the SIGUSR1 handler can close
	// the live WS the moment a pause is requested, instead of waiting up
	// to readDeadline (60s) for the read to time out.
	currentConnMu sync.Mutex
	currentConn   *websocket.Conn
}

func NewAgent(version string) (*Agent, error) {
	id, err := session.NewID(8)
	if err != nil {
		return nil, err
	}
	pin, err := session.NewPIN(6)
	if err != nil {
		return nil, err
	}
	box, err := crypto.NewBox(id, pin)
	if err != nil {
		return nil, err
	}
	pinHash, err := session.HashPIN(pin)
	if err != nil {
		return nil, err
	}

	return &Agent{
		sessionID:  id,
		pin:        pin,
		pinHash:    pinHash,
		webURL:     config.WebURL(),
		shell:      config.Shell(),
		version:    version,
		box:        box,
		buf:        newScrollback(scrollbackBytes),
		hostEscape: make(chan struct{}),
	}, nil
}

func (a *Agent) Run() error {
	fmt.Println()
	fmt.Printf("  reminal — remote terminal · v%s\n", a.version)
	fmt.Println()
	fmt.Printf("  Session:  %s\n", a.sessionID)
	fmt.Printf("  PIN:      %s\n", a.pin)
	fmt.Printf("  Open:     %s/?s=%s\n", a.webURL, a.sessionID)
	fmt.Printf("  Connect:  reminal connect %s %s\n", a.sessionID, a.pin)
	fmt.Println()
	a.printQR()
	fmt.Println("  This terminal IS the shared shell — type away. Remote viewers can join in parallel.")
	fmt.Println("  Press Ctrl-] to stop reminal · `reminal info` shows the join details again")
	fmt.Println()

	// Record this session for `reminal info`. Best-effort: failures here
	// shouldn't break agent startup. startedAt is stored on the Agent so
	// later viewer-count rewrites keep the same value.
	a.startedAt = time.Now()
	_ = session.WriteActive(a.activeRecord(0))
	defer func() { _ = session.ClearActive() }()

	// Pass REMINAL_SESSION into the spawned shell so `reminal info` run
	// from inside this session can show THIS session's details (rather
	// than fall back to ~/.reminal/active.json, which gets ambiguous with
	// multiple agents or attach-vs-source contexts).
	term, err := pty.Start(a.shell, "REMINAL_SESSION="+a.sessionID)
	if err != nil {
		return fmt.Errorf("start shell: %w", err)
	}
	defer term.Close()
	a.term = term

	pty.HandleSignals()

	// Attach the host's local terminal to the PTY: raw mode, mirror PTY
	// output to stdout, pump host stdin to the PTY, follow SIGWINCH. This
	// turns `reminal` from a "display only" host into the same kind of
	// interactive shell `reminal connect` provides — no second terminal
	// needed, and remote viewers still join the same PTY in parallel.
	if xterm.IsTerminal(int(os.Stdin.Fd())) {
		oldState, terr := xterm.MakeRaw(int(os.Stdin.Fd()))
		if terr == nil {
			defer xterm.Restore(int(os.Stdin.Fd()), oldState)
			setHostIndicator(a.sessionID)
			defer clearHostIndicator()
			a.localActive = true
			a.syncSizeToPTY()
			go a.pumpHostStdin()
			winCh := make(chan os.Signal, 1)
			signal.Notify(winCh, syscall.SIGWINCH)
			defer signal.Stop(winCh)
			go func() {
				for range winCh {
					a.syncSizeToPTY()
				}
			}()
		}
	}

	sessionStart := time.Now()
	// Deferred so it runs on every clean exit path — shell exit, agent
	// errors, signal-driven shutdown. Neutral wording ("session ended")
	// covers both shell-typed-exit and user-killed-reminal cases.
	defer func() {
		agentNotify("\n  [%s] Session ended (v%s) · ran for %v\n  Run `reminal` again to start a new session.\n",
			time.Now().Format("15:04:05"),
			a.version,
			time.Since(sessionStart).Round(time.Second))
	}()

	shellExit := make(chan struct{})
	go func() {
		a.pumpPTY()
		close(shellExit)
	}()

	// Trap SIGINT/SIGTERM so the process exits via the normal return path
	// (defers fire: ClearActive, exit summary, keepawake stop). Default Go
	// behavior is to die immediately on these signals, skipping defers and
	// leaving stale ~/.reminal/active.json + orphaned caffeinate.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGUSR1)
	defer signal.Stop(sigCh)
	go func() {
		first := true
		for sig := range sigCh {
			if sig == syscall.SIGUSR1 {
				// `reminal stop` — stop broadcasting, keep the local
				// shell running. Doesn't count against the force-exit
				// budget; SIGINT/SIGTERM still need a double-tap.
				a.pause()
				continue
			}
			if !first {
				fmt.Fprintln(os.Stderr, "\n  Force exit.")
				os.Exit(130)
			}
			first = false
			agentNotify("\n  [%s] %s received, shutting down… (press again to force exit)\n",
				time.Now().Format("15:04:05"), sig)
			_ = term.Close()
		}
	}()

	backoff := initialBackoff
	for {
		select {
		case <-shellExit:
			return nil
		case <-a.hostEscape:
			return nil
		default:
		}

		// Paused via `reminal stop`: don't touch the relay; just sit and
		// let the PTY pumps continue serving the host terminal as a
		// plain local shell until shell exit / Ctrl-].
		if a.paused.Load() {
			select {
			case <-shellExit:
				return nil
			case <-a.hostEscape:
				return nil
			}
		}

		start := time.Now()
		err := a.runConnection(shellExit)
		select {
		case <-shellExit:
			return nil
		case <-a.hostEscape:
			return nil
		default:
		}

		// runConnection returned because pause() closed the WS — don't
		// log "Reconnecting…" or burn through backoff cycles.
		if a.paused.Load() {
			continue
		}

		if err == nil {
			err = errors.New("connection closed")
		}
		if time.Since(start) > stableThresh {
			backoff = initialBackoff
		}
		agentNotify("  reminal: %s Reconnecting in %v…\n", humanize(err), backoff)

		select {
		case <-shellExit:
			return nil
		case <-a.hostEscape:
			return nil
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// syncSizeToPTY copies the host terminal's current size into the PTY so the
// shell sees the correct cols/rows. Called on startup and on every SIGWINCH.
func (a *Agent) syncSizeToPTY() {
	cols, rows, err := xterm.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		return
	}
	_ = a.term.Resize(uint16(cols), uint16(rows))
}

// activeRecord builds the session.Active value the agent writes to
// ~/.reminal/active.json. Used at startup and on every viewer-count
// transition so `reminal info` and the attach-to-existing prompt see a
// live count.
func (a *Agent) activeRecord(viewers int) session.Active {
	return session.Active{
		ID:        a.sessionID,
		PIN:       a.pin,
		OpenURL:   fmt.Sprintf("%s/?s=%s", a.webURL, a.sessionID),
		PID:       os.Getpid(),
		StartedAt: a.startedAt,
		Viewers:   viewers,
	}
}

// updateActiveViewers refreshes the on-disk viewer count. No-op when paused
// (active.json is intentionally absent during pause).
func (a *Agent) updateActiveViewers(viewers int) {
	if a.paused.Load() {
		return
	}
	_ = session.WriteActive(a.activeRecord(viewers))
}

// pause is invoked from the SIGUSR1 handler when the user runs
// `reminal stop`. It closes the live WS (if any) so the relay sees us go
// offline immediately, clears the on-disk session record so attach can't
// reach a non-broadcasting agent, and flips the paused flag so the main
// reconnect loop stops trying. The PTY pumps keep running, so the host
// terminal continues working as a plain local shell.
func (a *Agent) pause() {
	if !a.paused.CompareAndSwap(false, true) {
		return // already paused
	}
	_ = session.ClearActive()
	a.currentConnMu.Lock()
	if a.currentConn != nil {
		_ = a.currentConn.Close()
	}
	a.currentConnMu.Unlock()
	agentNotify("\n  [%s] Sharing stopped. Local shell continues — type `exit` or Ctrl-] to quit.\n",
		time.Now().Format("15:04:05"))
}

// agentNotify writes a one-line dim status to stdout. Used for operational
// events (viewer connect/disconnect, reconnect retries, shutdown signals,
// exit summary) so they read as background metadata against the shell's own
// output rather than competing content. Initial banner stays at full
// brightness because that's the info the user actively needs to see.
func agentNotify(format string, args ...interface{}) {
	fmt.Printf("\x1b[2m"+format+"\x1b[0m", args...)
}

// printQR renders a scannable QR for the join URL with the PIN in the URL
// fragment (#p=...). The fragment never leaves the phone — it's read by the
// page's JS to autofill the PIN field, giving a one-tap join from mobile.
func (a *Agent) printQR() {
	joinURL := fmt.Sprintf("%s/?s=%s#p=%s", a.webURL, a.sessionID, a.pin)
	fmt.Println("  Scan to join from your phone:")
	fmt.Println()
	qrterminal.GenerateHalfBlock(joinURL, qrterminal.L, os.Stdout)
	fmt.Println()
}

// pumpPTY reads the PTY forever, encrypts each chunk and stores it in the
// scrollback. The sender goroutine drains the scrollback over the websocket.
func (a *Agent) pumpPTY() {
	buf := make([]byte, 4096)
	for {
		n, err := a.term.Read(buf)
		if n > 0 {
			// Mirror to the host's stdout so the user typing at the agent
			// terminal sees the shell's output as if they had run it
			// directly. The same bytes go into scrollback for remote viewers.
			if a.localActive {
				_, _ = os.Stdout.Write(buf[:n])
			}
			enc, encErr := a.box.Encrypt(buf[:n])
			if encErr == nil {
				a.buf.Append(enc)
			}
		}
		if err != nil {
			return
		}
	}
}

// pumpHostStdin reads the agent's local terminal stdin and writes each chunk
// straight into the PTY — making the host terminal an interactive shell, same
// as a `reminal connect` session would be. Ctrl-] (telnet's traditional
// escape) signals shutdown via hostEscape; the byte itself is not forwarded.
func (a *Agent) pumpHostStdin() {
	buf := make([]byte, 4096)
	for {
		n, err := os.Stdin.Read(buf)
		if n > 0 {
			if i := bytes.IndexByte(buf[:n], escapeKey); i >= 0 {
				if i > 0 {
					_, _ = a.term.Write(buf[:i])
				}
				select {
				case <-a.hostEscape:
					// already signalled
				default:
					close(a.hostEscape)
				}
				return
			}
			_, _ = a.term.Write(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

func (a *Agent) runConnection(shellExit <-chan struct{}) error {
	wsURL := config.SessionWS(a.sessionID, string(protocol.RoleAgent))
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		return fmt.Errorf("dial relay: %w", err)
	}
	defer conn.Close()
	// Track the live conn so `reminal stop` (SIGUSR1) can close it
	// immediately rather than waiting for the next read deadline.
	a.currentConnMu.Lock()
	a.currentConn = conn
	a.currentConnMu.Unlock()
	defer func() {
		a.currentConnMu.Lock()
		a.currentConn = nil
		a.currentConnMu.Unlock()
	}()

	if err := a.authenticate(conn); err != nil {
		return err
	}

	cursorCh := make(chan uint64, 4)
	stop := make(chan struct{})
	var stopOnce sync.Once
	closeStop := func() { stopOnce.Do(func() { close(stop) }) }

	readerDone := make(chan error, 1)
	go func() { readerDone <- a.runReader(conn, cursorCh) }()

	senderDone := make(chan error, 1)
	go func() { senderDone <- a.runSender(conn, cursorCh, stop) }()

	pingStop := make(chan struct{})
	var pingOnce sync.Once
	closePing := func() { pingOnce.Do(func() { close(pingStop) }) }
	go a.runPing(conn, pingStop)

	defer closeStop()
	defer closePing()

	select {
	case <-shellExit:
		return nil
	case <-a.hostEscape:
		// Host pressed Ctrl-]; close the live conn so the reader goroutine
		// returns immediately rather than blocking on its next read until
		// readDeadline fires (which would otherwise freeze pumpHostStdin
		// has already exited, and the user's local terminal would feel
		// dead for up to 60s).
		_ = conn.Close()
		return nil
	case err := <-readerDone:
		return err
	case err := <-senderDone:
		return err
	}
}

func (a *Agent) authenticate(conn *websocket.Conn) error {
	if err := a.writeMsg(conn, protocol.Message{
		Type:    protocol.TypeAuth,
		PinHash: a.pinHash,
	}); err != nil {
		return err
	}

	_, raw, err := conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("auth: %w", err)
	}

	var msg protocol.Message
	if err := json.Unmarshal(raw, &msg); err != nil {
		return fmt.Errorf("auth response: %w", err)
	}
	if msg.Type == protocol.TypeError {
		return fmt.Errorf("%s", msg.Error)
	}
	if msg.Type != protocol.TypeAuthOK {
		return fmt.Errorf("unexpected auth response: %s", msg.Type)
	}
	return nil
}

// runReader processes messages from the relay: viewer input, resize, resume
// requests, and connection notifications.
func (a *Agent) runReader(conn *websocket.Conn, cursorCh chan uint64) error {
	for {
		_ = conn.SetReadDeadline(time.Now().Add(readDeadlineAgent))
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
			data, err := a.box.Decrypt(msg.Data)
			if err != nil {
				continue
			}
			if _, err := a.term.Write(data); err != nil {
				return err
			}
		case protocol.TypeResize:
			plaintext, err := a.box.Decrypt(msg.Data)
			if err != nil {
				continue
			}
			var rs protocol.Message
			if json.Unmarshal(plaintext, &rs) == nil {
				_ = a.term.Resize(rs.Cols, rs.Rows)
			}
		case protocol.TypeResume:
			// FromSeq is the highest seq the viewer has received. We replay
			// everything with Seq > FromSeq, so the next seq to send is FromSeq+1.
			pushCursor(cursorCh, msg.FromSeq+1)
		case protocol.TypeConnected:
			if msg.Count > 0 {
				agentNotify("  [%s] Viewer connected (%d active)\n",
					time.Now().Format("15:04:05"), msg.Count)
			} else {
				agentNotify("  [%s] Remote viewer connected\n",
					time.Now().Format("15:04:05"))
			}
			a.updateActiveViewers(msg.Count)
		case protocol.TypeClosed:
			if msg.Count > 0 {
				agentNotify("  [%s] Viewer disconnected (%d still active)\n",
					time.Now().Format("15:04:05"), msg.Count)
			} else {
				agentNotify("  [%s] Last viewer disconnected\n",
					time.Now().Format("15:04:05"))
			}
			a.updateActiveViewers(msg.Count)
		case protocol.TypeAgentOffline, protocol.TypeAgentOnline:
			// Informational only on the agent side.
		case protocol.TypeError:
			return fmt.Errorf("%s", msg.Error)
		case protocol.TypePing:
			_ = a.writeMsg(conn, protocol.Message{Type: protocol.TypePong})
		}
	}
}

// runSender waits for a resume from the viewer, then streams buffered output
// from that point. It keeps streaming new chunks as they're appended.
func (a *Agent) runSender(conn *websocket.Conn, cursorCh <-chan uint64, stop <-chan struct{}) error {
	notify := a.buf.Notify()
	var cursor uint64
	sending := false

	for {
		if !sending {
			select {
			case <-stop:
				return nil
			case c := <-cursorCh:
				cursor = c
				sending = true
			}
		}

		entries := a.buf.From(cursor)
		for _, e := range entries {
			if err := a.writeMsg(conn, protocol.Message{
				Type: protocol.TypeData,
				Data: e.Data,
				Seq:  e.Seq,
			}); err != nil {
				return err
			}
			cursor = e.Seq + 1
		}

		select {
		case <-stop:
			return nil
		case c := <-cursorCh:
			cursor = c
		case <-notify:
		}
	}
}

func (a *Agent) writeMsg(conn *websocket.Conn, msg protocol.Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	return conn.WriteMessage(websocket.TextMessage, data)
}

func (a *Agent) runPing(conn *websocket.Conn, stop <-chan struct{}) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			if err := a.writeMsg(conn, protocol.Message{Type: protocol.TypePing}); err != nil {
				return
			}
		}
	}
}

// pushCursor non-blockingly delivers the latest from_seq to the sender,
// replacing any older queued value so the sender always reacts to the newest
// resume request.
func pushCursor(ch chan uint64, seq uint64) {
	for {
		select {
		case ch <- seq:
			return
		default:
			// Drain the oldest entry to make room, then try again.
			select {
			case <-ch:
			default:
				return
			}
		}
	}
}

func encryptResize(box *crypto.Box, cols, rows uint16) (string, error) {
	payload, err := json.Marshal(protocol.Message{Cols: cols, Rows: rows})
	if err != nil {
		return "", err
	}
	return box.Encrypt(payload)
}
