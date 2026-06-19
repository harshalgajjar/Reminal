package client

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/gorilla/websocket"
	"github.com/reminal/reminal/internal/config"
	"github.com/reminal/reminal/internal/crypto"
	"github.com/reminal/reminal/internal/protocol"
	"github.com/reminal/reminal/internal/pty"
	"github.com/reminal/reminal/internal/session"
)

type Agent struct {
	sessionID string
	pin       string
	pinHash   string
	webURL    string
	shell     string
	box       *crypto.Box
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
	}, nil
}

func (a *Agent) Run() error {
	fmt.Println()
	fmt.Println("  reminal — remote terminal")
	fmt.Println()
	fmt.Printf("  Session:  %s\n", a.sessionID)
	fmt.Printf("  PIN:      %s\n", a.pin)
	fmt.Printf("  Open:     %s/?s=%s\n", a.webURL, a.sessionID)
	fmt.Printf("  Connect:  reminal --connect %s --pin %s\n", a.sessionID, a.pin)
	fmt.Println()
	fmt.Println("  Waiting for connection... (Ctrl+C to stop)")
	fmt.Println()

	wsURL := config.SessionWS(a.sessionID, string(protocol.RoleAgent))
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		return fmt.Errorf("connect to relay: %w\n\n  Check your internet connection, or run locally with REMINAL_LOCAL=1", err)
	}
	defer conn.Close()

	if err := a.authenticate(conn); err != nil {
		return err
	}

	term, err := pty.Start(a.shell)
	if err != nil {
		return fmt.Errorf("start shell: %w", err)
	}
	defer term.Close()

	pty.HandleSignals()

	errCh := make(chan error, 2)
	go func() {
		errCh <- a.readRelay(conn, term)
	}()
	go func() {
		errCh <- a.readPTY(conn, term)
	}()

	go a.ping(conn)

	select {
	case err := <-errCh:
		return err
	case <-a.waitShell(term):
		return nil
	}
}

func (a *Agent) authenticate(conn *websocket.Conn) error {
	if err := a.send(conn, protocol.Message{
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
		return fmt.Errorf("unexpected auth response")
	}
	return nil
}

func (a *Agent) waitShell(term *pty.Session) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		_ = term.Wait()
		close(done)
	}()
	return done
}

func (a *Agent) readRelay(conn *websocket.Conn, term *pty.Session) error {
	for {
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
			if _, err := term.Write(data); err != nil {
				return err
			}
		case protocol.TypeResize:
			plaintext, err := a.box.Decrypt(msg.Data)
			if err != nil {
				continue
			}
			var resize protocol.Message
			if json.Unmarshal(plaintext, &resize) == nil {
				_ = term.Resize(resize.Cols, resize.Rows)
			}
		case protocol.TypeConnected:
			fmt.Println("  ✓ Remote viewer connected")
		case protocol.TypeClosed:
			fmt.Println("  Viewer disconnected")
		case protocol.TypeError:
			return fmt.Errorf("%s", msg.Error)
		case protocol.TypePing:
			_ = a.send(conn, protocol.Message{Type: protocol.TypePong})
		}
	}
}

func (a *Agent) readPTY(conn *websocket.Conn, term *pty.Session) error {
	buf := make([]byte, 4096)
	for {
		n, err := term.Read(buf)
		if n > 0 {
			enc, err := a.box.Encrypt(buf[:n])
			if err != nil {
				return err
			}
			if err := a.send(conn, protocol.Message{
				Type: protocol.TypeData,
				Data: enc,
			}); err != nil {
				return err
			}
		}
		if err != nil {
			return err
		}
	}
}

func (a *Agent) send(conn *websocket.Conn, msg protocol.Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return conn.WriteMessage(websocket.TextMessage, data)
}

func (a *Agent) ping(conn *websocket.Conn) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		if err := a.send(conn, protocol.Message{Type: protocol.TypePing}); err != nil {
			return
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
