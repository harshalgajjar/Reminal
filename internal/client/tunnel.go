package client

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/mdp/qrterminal/v3"
	"github.com/reminal/reminal/internal/config"
	"github.com/reminal/reminal/internal/protocol"
	"github.com/reminal/reminal/internal/session"
)

// maxTunnelBody bounds a single proxied response. Cloudflare DOs cap
// each WS message at 1 MiB; with base64 expansion (4/3x) + headers JSON
// + envelope, ~700 KB of raw bytes is the safe ceiling. Larger
// responses get truncated for now — streaming-through-tunnel is a v2
// feature.
const maxTunnelBody = 700 * 1024

// TunnelOptions configures a port-forward agent.
type TunnelOptions struct {
	Port int
	// Public registers the tunnel with no PIN gate — anyone who knows
	// the URL can reach the port. Off by default; opt-in via
	// `reminal expose <port> --public`.
	Public bool
	// HandshakeFD mirrors AgentOptions — when non-zero, the tunnel
	// writes credentials JSON to this fd once it's connected so the
	// parent `reminal expose` process can print + exit.
	HandshakeFD int
	// Version stamps the active record + banner.
	Version string
}

// Tunnel is the running state of a port-forward. Mirrors Agent in shape
// but skips PTY / scrollback / viewer-list machinery — port-forwards
// only proxy HTTP, they don't broadcast anything.
type Tunnel struct {
	sessionID string
	pin       string
	pinHash   string
	webURL    string
	port      int
	public    bool
	version   string
	startedAt time.Time

	writeMu     sync.Mutex // serialises WS writes across the per-request goroutines
	httpClient  *http.Client
	handshakeFD int

	// connMu guards conn so the signal handler can close the live WS
	// the moment stop fires, instead of waiting up to 60s for the read
	// deadline to expire.
	connMu sync.Mutex
	conn   *websocket.Conn

	paused atomic.Bool
}

// NewTunnel constructs a port-forward agent. Session ID + PIN are
// freshly generated — every `reminal expose` invocation gets a new
// pair, even for the same port, so old URLs become invalid the moment
// a tunnel is restarted.
func NewTunnel(opts TunnelOptions) (*Tunnel, error) {
	if opts.Port <= 0 || opts.Port > 65535 {
		return nil, fmt.Errorf("port %d out of range (1-65535)", opts.Port)
	}
	id, err := session.NewID(8)
	if err != nil {
		return nil, err
	}
	pin, err := session.NewPIN(6)
	if err != nil {
		return nil, err
	}
	pinHash, err := session.HashPIN(pin)
	if err != nil {
		return nil, err
	}
	hc := &http.Client{
		Timeout: 60 * time.Second,
		// Don't follow redirects: the visitor's browser should see the
		// 3xx so it can update its URL bar / honour cookie scope etc.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return &Tunnel{
		sessionID:   id,
		pin:         pin,
		pinHash:     pinHash,
		webURL:      config.WebURL(),
		port:        opts.Port,
		public:      opts.Public,
		version:     opts.Version,
		httpClient:  hc,
		handshakeFD: opts.HandshakeFD,
	}, nil
}

// PublicURL is the path-based URL visitors hit.
func (t *Tunnel) PublicURL() string {
	return fmt.Sprintf("%s/p/%s/", t.webURL, t.sessionID)
}

// Run is the main loop: connect, authenticate, register, serve tunnel
// requests until the WS dies, reconnect with backoff. Always headless —
// port-forwards never own the host terminal.
func (t *Tunnel) Run() error {
	// Refuse a duplicate on the same port — would race for the same DO
	// room and confuse `reminal list`.
	if existing, err := session.ReadActiveByPort(t.port); err == nil && existing.ID != t.sessionID {
		return fmt.Errorf("port %d is already exposed (session %s, started %v ago)",
			t.port, existing.ID, time.Since(existing.StartedAt).Round(time.Second))
	}

	t.startedAt = time.Now()
	_ = session.WriteActive(t.activeRecord())
	defer func() { _ = session.ClearActive(t.sessionID) }()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGUSR1)
	defer signal.Stop(sigCh)
	stop := make(chan struct{})
	go func() {
		for sig := range sigCh {
			if sig == syscall.SIGUSR1 {
				t.paused.Store(true)
			}
			close(stop)
			// Close the live WS too — otherwise the read in
			// runConnection sits until its 60s deadline, which makes
			// `reminal stop` look like it didn't work.
			t.connMu.Lock()
			if t.conn != nil {
				_ = t.conn.Close()
			}
			t.connMu.Unlock()
			return
		}
	}()

	backoff := initialBackoff
	for {
		select {
		case <-stop:
			return nil
		default:
		}
		start := time.Now()
		err := t.runConnection(stop)
		select {
		case <-stop:
			return nil
		default:
		}
		if err == nil {
			err = errors.New("connection closed")
		}
		if time.Since(start) > stableThresh {
			backoff = initialBackoff
		}

		var rl *rateLimitedError
		if errors.As(err, &rl) {
			wait := rl.retryAfter
			if wait < rateLimitMinWait {
				wait = rateLimitMinWait
			}
			fmt.Fprintf(os.Stderr, "reminal expose: %s\n", humanize(err))
			select {
			case <-stop:
				return nil
			case <-time.After(wait):
			}
			backoff = initialBackoff
			continue
		}
		fmt.Fprintf(os.Stderr, "reminal expose: %s — reconnecting in %v\n", humanize(err), backoff)
		select {
		case <-stop:
			return nil
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

func (t *Tunnel) activeRecord() session.Active {
	return session.Active{
		ID:        t.sessionID,
		PIN:       t.pin,
		OpenURL:   t.PublicURL(),
		PID:       os.Getpid(),
		StartedAt: t.startedAt,
		Kind:      session.KindPort,
		Port:      t.port,
	}
}

func (t *Tunnel) runConnection(stop <-chan struct{}) error {
	wsURL := config.SessionWS(t.sessionID, string(protocol.RoleTunnel))
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		if resp != nil && resp.StatusCode == 429 {
			return &rateLimitedError{retryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
		}
		return fmt.Errorf("dial relay: %w", err)
	}
	defer conn.Close()
	t.connMu.Lock()
	t.conn = conn
	t.connMu.Unlock()
	defer func() {
		t.connMu.Lock()
		t.conn = nil
		t.connMu.Unlock()
	}()

	if err := t.writeMsg(conn, protocol.Message{Type: protocol.TypeAuth, PinHash: t.pinHash}); err != nil {
		return err
	}
	_, raw, err := conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("auth read: %w", err)
	}
	var msg protocol.Message
	if err := json.Unmarshal(raw, &msg); err != nil {
		return fmt.Errorf("auth parse: %w", err)
	}
	if msg.Type == protocol.TypeError {
		return errors.New(msg.Error)
	}
	if msg.Type != protocol.TypeAuthOK {
		return fmt.Errorf("unexpected auth response %q", msg.Type)
	}

	regPayload, _ := json.Marshal(map[string]any{
		"port":     t.port,
		"public":   t.public,
		"pin_hash": t.pinHash,
	})
	if err := t.writeMsg(conn, protocol.Message{
		Type: protocol.TypeTunnelRegister,
		Data: string(regPayload),
	}); err != nil {
		return err
	}

	// Connected + registered → tell `reminal expose` to print + exit.
	t.writeHandshake()

	for {
		select {
		case <-stop:
			return nil
		default:
		}
		_ = conn.SetReadDeadline(time.Now().Add(readDeadlineAgent))
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		var m protocol.Message
		if err := json.Unmarshal(raw, &m); err != nil {
			continue
		}
		switch m.Type {
		case protocol.TypePing:
			_ = t.writeMsg(conn, protocol.Message{Type: protocol.TypePong})
		case protocol.TypeTunnelReq:
			go t.handleTunnelReq(conn, m.Data)
		}
	}
}

// handleTunnelReq parses one tunnel_req payload, performs the local
// HTTP request, and sends back a tunnel_resp. Errors surface to the
// visitor as a 502 with the message in the body.
func (t *Tunnel) handleTunnelReq(conn *websocket.Conn, payload string) {
	var req struct {
		ReqID   string            `json:"req_id"`
		Method  string            `json:"method"`
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"` // base64
	}
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return
	}
	var body io.Reader
	if req.Body != "" {
		raw, err := base64.StdEncoding.DecodeString(req.Body)
		if err == nil {
			body = bytes.NewReader(raw)
		}
	}
	target := fmt.Sprintf("http://127.0.0.1:%d%s", t.port, req.URL)
	httpReq, err := http.NewRequest(strings.ToUpper(req.Method), target, body)
	if err != nil {
		t.sendError(conn, req.ReqID, fmt.Sprintf("build request: %v", err))
		return
	}
	for k, v := range req.Headers {
		if isHopHeader(k) {
			continue
		}
		httpReq.Header.Set(k, v)
	}
	// Reverse-proxy hygiene — give the upstream the original scheme +
	// host so it can log + redirect correctly.
	httpReq.Header.Set("X-Forwarded-Proto", "https")
	if h := req.Headers["Host"]; h != "" {
		httpReq.Header.Set("X-Forwarded-Host", h)
	}

	resp, err := t.httpClient.Do(httpReq)
	if err != nil {
		t.sendError(conn, req.ReqID, fmt.Sprintf("local server unreachable: %v", err))
		return
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxTunnelBody))
	if err != nil {
		t.sendError(conn, req.ReqID, fmt.Sprintf("read response: %v", err))
		return
	}

	headers := map[string]string{}
	for k, v := range resp.Header {
		if isHopHeader(k) || len(v) == 0 {
			continue
		}
		headers[k] = v[0]
	}

	out, _ := json.Marshal(map[string]any{
		"req_id":  req.ReqID,
		"status":  resp.StatusCode,
		"headers": headers,
		"body":    base64.StdEncoding.EncodeToString(respBody),
	})
	_ = t.writeMsg(conn, protocol.Message{Type: protocol.TypeTunnelResp, Data: string(out)})
}

func (t *Tunnel) sendError(conn *websocket.Conn, reqID, msg string) {
	out, _ := json.Marshal(map[string]any{
		"req_id":  reqID,
		"status":  502,
		"headers": map[string]string{"Content-Type": "text/plain"},
		"body":    base64.StdEncoding.EncodeToString([]byte("reminal: " + msg + "\n")),
	})
	_ = t.writeMsg(conn, protocol.Message{Type: protocol.TypeTunnelResp, Data: string(out)})
}

func (t *Tunnel) writeMsg(conn *websocket.Conn, m protocol.Message) error {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	return conn.WriteJSON(m)
}

func (t *Tunnel) writeHandshake() {
	if t.handshakeFD == 0 {
		return
	}
	payload := map[string]any{
		"id":       t.sessionID,
		"pin":      t.pin,
		"open_url": t.PublicURL(),
		"pid":      os.Getpid(),
		"port":     t.port,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	f := os.NewFile(uintptr(t.handshakeFD), "handshake")
	if f == nil {
		return
	}
	_, _ = f.Write(append(data, '\n'))
	_ = f.Close()
	t.handshakeFD = 0
}

// isHopHeader returns true for headers that mustn't be forwarded across
// proxies (RFC 7230 §6.1). Standard reverse-proxy hygiene.
func isHopHeader(name string) bool {
	switch strings.ToLower(name) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization",
		"te", "trailer", "transfer-encoding", "upgrade", "host", "content-length":
		return true
	}
	return false
}

// ---- spawn helpers shared with `reminal expose` ----

// SpawnTunnel forks a detached headless port-forwarder via the running
// binary and blocks until the child writes its credentials back. Mirrors
// Spawn() (shell sessions) — same fd-3 handshake, same Setsid detach.
func SpawnTunnel(port int, public bool) (*SpawnedSession, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate self: %w", err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer r.Close()

	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		_ = w.Close()
		return nil, err
	}
	defer devnull.Close()

	args := []string{
		"--expose-headless",
		"--expose-port", fmt.Sprintf("%d", port),
		"--handshake-fd", "3",
	}
	if public {
		args = append(args, "--expose-public")
	}
	cmd := exec.Command(exe, args...)
	cmd.Stdin = devnull
	cmd.Stdout = devnull
	cmd.Stderr = devnull
	cmd.ExtraFiles = []*os.File{w}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("start headless tunnel: %w", err)
	}
	_ = w.Close()
	_ = cmd.Process.Release()

	type result struct {
		s   *SpawnedSession
		err error
	}
	done := make(chan result, 1)
	go func() {
		dec := json.NewDecoder(r)
		var sp SpawnedSession
		if err := dec.Decode(&sp); err != nil {
			done <- result{nil, fmt.Errorf("read handshake: %w", err)}
			return
		}
		done <- result{&sp, nil}
	}()
	select {
	case res := <-done:
		return res.s, res.err
	case <-time.After(spawnHandshakeTimeout):
		return nil, errors.New("headless tunnel didn't report ready within " + spawnHandshakeTimeout.String())
	}
}

// PrintSpawnedTunnel renders the new port-forward credentials for the
// user's calling shell. Distinct from PrintSpawned (shell sessions)
// because the URL shape and the "what is this?" copy differ.
func PrintSpawnedTunnel(sp *SpawnedSession, port int, public bool, version string) {
	fmt.Println()
	mode := "PIN-protected"
	if public {
		mode = "public (no PIN required)"
	}
	fmt.Printf("  reminal — exposing localhost:%d · %s · v%s\n", port, mode, version)
	fmt.Println()
	fmt.Printf("  Public URL:  %s\n", sp.OpenURL)
	if !public {
		fmt.Printf("  PIN:         %s\n", sp.PIN)
		fmt.Printf("  Quick link:  %s#p=%s   (one-tap auth for you)\n", sp.OpenURL, sp.PIN)
	}
	fmt.Printf("  PID:         %d  (detached — survives this terminal closing)\n", sp.PID)
	fmt.Println()
	qrURL := sp.OpenURL
	if !public {
		qrURL = sp.OpenURL + "#p=" + sp.PIN
	}
	qrterminal.GenerateWithConfig(qrURL, qrterminal.Config{
		Level:     qrterminal.L,
		Writer:    os.Stdout,
		BlackChar: qrterminal.BLACK,
		WhiteChar: qrterminal.WHITE,
		QuietZone: 1,
	})
	fmt.Println()
	if public {
		fmt.Println("  This URL is open to anyone who finds it.")
	}
	fmt.Printf("  To stop forwarding: reminal stop %d\n", port)
	fmt.Println()
}

// ResolveLocalPort looks up a port-forward by either session ID or port
// number string. Used by the CLI so `reminal stop 3000` and
// `reminal stop F6WRJPE9` both work.
func ResolveLocalPort(arg string) (*session.Active, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return nil, errors.New("port or session id required")
	}
	if isAllDigits(arg) {
		var port int
		if _, err := fmt.Sscanf(arg, "%d", &port); err != nil {
			return nil, err
		}
		return session.ReadActiveByPort(port)
	}
	return session.ReadActiveByID(strings.ToUpper(arg))
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
