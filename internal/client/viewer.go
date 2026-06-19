package client

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/reminal/reminal/internal/config"
	"github.com/reminal/reminal/internal/crypto"
	"github.com/reminal/reminal/internal/protocol"
	"github.com/reminal/reminal/internal/session"
	"golang.org/x/term"
)

type Viewer struct {
	sessionID string
	pin       string
	box       *crypto.Box
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
	wsURL := config.SessionWS(v.sessionID, string(protocol.RoleViewer))
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		return fmt.Errorf("connect to relay: %w", err)
	}
	defer conn.Close()

	if err := v.authenticate(conn); err != nil {
		return err
	}

	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return fmt.Errorf("stdin is not a terminal")
	}

	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("enable raw mode: %w", err)
	}
	defer term.Restore(fd, oldState)

	errCh := make(chan error, 2)
	go func() { errCh <- v.readRelay(conn) }()
	go func() { errCh <- v.readStdin(conn) }()
	go v.ping(conn)
	go v.watchResize(conn, fd)

	v.sendResize(conn)
	return <-errCh
}

func (v *Viewer) authenticate(conn *websocket.Conn) error {
	if err := v.send(conn, protocol.Message{
		Type: protocol.TypeAuth,
		Pin:  v.pin,
	}); err != nil {
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
		case protocol.TypeAuthOK, protocol.TypeConnected:
			return nil
		}
	}
}

func (v *Viewer) readRelay(conn *websocket.Conn) error {
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
			data, err := v.box.Decrypt(msg.Data)
			if err != nil {
				continue
			}
			if _, err := os.Stdout.Write(data); err != nil {
				return err
			}
		case protocol.TypeResize:
			// resize from agent not expected on viewer stdout path
		case protocol.TypeClosed:
			return fmt.Errorf("%s", msg.Error)
		case protocol.TypeError:
			return fmt.Errorf("%s", msg.Error)
		case protocol.TypePing:
			_ = v.send(conn, protocol.Message{Type: protocol.TypePong})
		}
	}
}

func (v *Viewer) readStdin(conn *websocket.Conn) error {
	buf := make([]byte, 4096)
	for {
		n, err := os.Stdin.Read(buf)
		if n > 0 {
			enc, err := v.box.Encrypt(buf[:n])
			if err != nil {
				return err
			}
			if err := v.send(conn, protocol.Message{
				Type: protocol.TypeData,
				Data: enc,
			}); err != nil {
				return err
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

func (v *Viewer) watchResize(conn *websocket.Conn, fd int) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	for range ch {
		v.sendResize(conn)
	}
}

func (v *Viewer) sendResize(conn *websocket.Conn) {
	cols, rows, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		return
	}
	enc, err := encryptResize(v.box, uint16(cols), uint16(rows))
	if err != nil {
		return
	}
	_ = v.send(conn, protocol.Message{
		Type: protocol.TypeResize,
		Data: enc,
	})
}

func (v *Viewer) send(conn *websocket.Conn, msg protocol.Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return conn.WriteMessage(websocket.TextMessage, data)
}

func (v *Viewer) ping(conn *websocket.Conn) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		if err := v.send(conn, protocol.Message{Type: protocol.TypePing}); err != nil {
			return
		}
	}
}
