package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/mdp/qrterminal/v3"
	"github.com/reminal/reminal/internal/config"
	"github.com/reminal/reminal/internal/crypto"
	"github.com/reminal/reminal/internal/protocol"
	"github.com/reminal/reminal/internal/pty"
	"github.com/reminal/reminal/internal/session"
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
	box       *crypto.Box
	buf       *scrollback
	term      *pty.Session

	writeMu sync.Mutex // serializes WS writes; safe across sender/reader goroutines
}

func NewAgent() (*Agent, error) {
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
		sessionID: id,
		pin:       pin,
		pinHash:   pinHash,
		webURL:    config.WebURL(),
		shell:     config.Shell(),
		box:       box,
		buf:       newScrollback(scrollbackBytes),
	}, nil
}

func (a *Agent) Run() error {
	fmt.Println()
	fmt.Println("  reminal — remote terminal")
	fmt.Println()
	fmt.Printf("  Session:  %s\n", a.sessionID)
	fmt.Printf("  PIN:      %s\n", a.pin)
	fmt.Printf("  Open:     %s/?s=%s\n", a.webURL, a.sessionID)
	fmt.Printf("  Connect:  reminal connect %s %s\n", a.sessionID, a.pin)
	fmt.Println()
	a.printQR()
	fmt.Println("  Waiting for connection... (Ctrl+C to stop)")
	fmt.Println("  (Lost track of the PIN? Run `reminal info` in another terminal.)")
	fmt.Println()

	// Record this session for `reminal info`. Best-effort: failures here
	// shouldn't break agent startup.
	_ = session.WriteActive(session.Active{
		ID:        a.sessionID,
		PIN:       a.pin,
		OpenURL:   fmt.Sprintf("%s/?s=%s", a.webURL, a.sessionID),
		PID:       os.Getpid(),
		StartedAt: time.Now(),
	})
	defer func() { _ = session.ClearActive() }()

	term, err := pty.Start(a.shell)
	if err != nil {
		return fmt.Errorf("start shell: %w", err)
	}
	defer term.Close()
	a.term = term

	pty.HandleSignals()

	shellExit := make(chan struct{})
	go func() {
		a.pumpPTY()
		close(shellExit)
	}()

	backoff := initialBackoff
	for {
		select {
		case <-shellExit:
			return nil
		default:
		}

		start := time.Now()
		err := a.runConnection(shellExit)
		select {
		case <-shellExit:
			return nil
		default:
		}

		if err == nil {
			err = errors.New("connection closed")
		}
		if time.Since(start) > stableThresh {
			backoff = initialBackoff
		}
		fmt.Printf("  reminal: %s Reconnecting in %v…\n", humanize(err), backoff)

		select {
		case <-shellExit:
			return nil
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
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

func (a *Agent) runConnection(shellExit <-chan struct{}) error {
	wsURL := config.SessionWS(a.sessionID, string(protocol.RoleAgent))
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		return fmt.Errorf("dial relay: %w", err)
	}
	defer conn.Close()

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
			fmt.Printf("  [%s] Remote viewer connected\n", time.Now().Format("15:04:05"))
		case protocol.TypeClosed:
			fmt.Printf("  [%s] Viewer disconnected\n", time.Now().Format("15:04:05"))
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
