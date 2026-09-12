// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/mdp/qrterminal/v3"
	"github.com/reminal/reminal/internal/config"
	"github.com/reminal/reminal/internal/protocol"
	"github.com/reminal/reminal/internal/session"
)

// tunnelChunkBytes bounds ONE tunnel_resp message's raw body. Cloudflare DOs
// cap each WS message at 1 MiB; with base64 expansion (4/3x) + headers JSON +
// envelope, ~700 KB of raw bytes is the safe ceiling. A proxied response larger
// than this is streamed as several tunnel_resp chunks (each carrying `more`),
// which the relay reassembles into a streamed Response — so large images/GIFs/
// downloads are no longer truncated at the old single-message limit.
const tunnelChunkBytes = 700 * 1024

// maxTunnelResponse caps a whole streamed response. It's an abuse guard AND a
// memory guard: a slow visitor lets chunks queue in the relay's Durable Object
// (which has ~128 MB), so the ceiling stays well under that. 64 MB is ~90x the
// old single-message limit — ample for any normal page, image, GIF, or asset.
// Past it the stream is closed cleanly (a truncated body, as before, just at a
// far higher ceiling).
const maxTunnelResponse = 64 * 1024 * 1024

// schemeProbeTimeout bounds the one-time TLS/TCP probe used to learn whether the
// local backend speaks plain HTTP or HTTPS (see backendScheme).
const schemeProbeTimeout = 3 * time.Second

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
	// HandshakeAddr is the Windows handshake channel: the parent's loopback
	// listener address to dial and report credentials to (see handshakeWriter).
	HandshakeAddr string
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

	writeMu       sync.Mutex // serialises WS writes across the per-request goroutines
	httpClient    *http.Client
	handshakeFD   int
	handshakeAddr string

	// schemeMu guards scheme, the cached result of probing whether the local
	// backend speaks plain HTTP or HTTPS. Detected lazily on the first proxied
	// request (see backendScheme) so `reminal expose 8443` transparently reaches
	// an HTTPS admin UI (webmin, UniFi, …) the same way it reaches an HTTP one.
	schemeMu sync.Mutex
	scheme   string // "" (unknown), "http", or "https"

	// connMu guards conn so the signal handler can close the live WS
	// the moment stop fires, instead of waiting up to 60s for the read
	// deadline to expire.
	connMu sync.Mutex
	conn   *websocket.Conn

	// wsMu guards wsStreams — the live proxied visitor WebSockets, keyed by the
	// relay-assigned stream id. Each entry owns a dialed backend socket plus its
	// reader/writer goroutines (see handleTunnelWSOpen).
	wsMu      sync.Mutex
	wsStreams map[string]*wsStream
}

// wsStream is one proxied visitor WebSocket: the dialed backend connection and
// the channel that feeds visitor→backend frames to its writer goroutine. done
// is closed exactly once (via closeWSStream) to stop both pumps and unblock any
// pending send — sendCh is never closed, so a late frame can't panic.
type wsStream struct {
	conn      *websocket.Conn
	sendCh    chan wsFrame
	done      chan struct{}
	closeOnce sync.Once
}

type wsFrame struct {
	data   []byte
	binary bool
}

// wsStreamSendBuffer bounds queued visitor→backend frames per stream. If a
// backend can't keep up and the buffer fills, the stream is torn down rather
// than stalling the shared tunnel control socket (head-of-line blocking).
const wsStreamSendBuffer = 256

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
	// Clone the default transport so we keep sane connection pooling/timeouts,
	// then allow self-signed TLS: HTTPS admin UIs on localhost (webmin, UniFi,
	// Proxmox, …) almost always present a self-signed cert, and the visitor's
	// trust boundary is the public tunnel URL (real TLS via Cloudflare), not the
	// loopback hop. This is the same posture as `cloudflared --no-tls-verify`.
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	hc := &http.Client{
		Timeout:   60 * time.Second,
		Transport: tr,
		// Don't follow redirects: the visitor's browser should see the
		// 3xx so it can update its URL bar / honour cookie scope etc.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return &Tunnel{
		sessionID:     id,
		pin:           pin,
		pinHash:       pinHash,
		webURL:        config.WebURL(),
		port:          opts.Port,
		public:        opts.Public,
		version:       opts.Version,
		httpClient:    hc,
		handshakeFD:   opts.HandshakeFD,
		handshakeAddr: opts.HandshakeAddr,
		wsStreams:     make(map[string]*wsStream),
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
	notifyAgentSignals(sigCh)
	defer signal.Stop(sigCh)
	stop := make(chan struct{})
	go func() {
		// Any of SIGINT/SIGTERM/SIGUSR1 shuts the forward down — a tunnel has no
		// local shell to keep alive, so (unlike the shell agent) there's nothing
		// to pause; `reminal stop` simply ends the forward.
		<-sigCh
		close(stop)
		// Close the live WS too — otherwise the read in runConnection sits until
		// its 60s deadline, which makes `reminal stop` look like it didn't work.
		t.connMu.Lock()
		if t.conn != nil {
			_ = t.conn.Close()
		}
		t.connMu.Unlock()
	}()

	// Serve this machine's owner-derived directory channel too, so a machine
	// that's running ONLY a port forward (no shell session) is still visible and
	// reachable in `reminal machines`. No-op unless the machine has owners; the
	// relay elects a single host across all of the machine's sessions.
	go runDirectoryHost(stop, false, t.version)

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
		ID:           t.sessionID,
		PidStartedAt: session.SelfStartTime(),
		PIN:          t.pin,
		OpenURL:      t.PublicURL(),
		PID:          os.Getpid(),
		StartedAt:    t.startedAt,
		Kind:         session.KindPort,
		Port:         t.port,
	}
}

func (t *Tunnel) runConnection(stop <-chan struct{}) (err error) {
	// Port-forward is a process-lifetime service; a panic in the synchronous
	// register/dispatch path must not crash it (spawned handleTunnelReq recovers
	// separately). Recover into an error so the caller reconnects.
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("recovered from panic in tunnel connection: %v", r)
		}
	}()
	wsURL := config.SessionWS(t.sessionID, string(protocol.RoleTunnel))
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		if resp != nil && resp.StatusCode == 429 {
			return &rateLimitedError{retryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
		}
		return fmt.Errorf("dial relay: %w", err)
	}
	defer conn.Close()
	conn.SetReadLimit(maxRelayMessageBytes) // untrusted relay — bound frame size
	t.connMu.Lock()
	t.conn = conn
	t.connMu.Unlock()
	defer func() {
		t.connMu.Lock()
		t.conn = nil
		t.connMu.Unlock()
		// The control socket is gone; every proxied visitor WebSocket rode over
		// it, so tear them all down. The relay closes the visitor sides too.
		t.closeAllWSStreams()
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
		case protocol.TypeTunnelWSOpen:
			go t.handleTunnelWSOpen(conn, m.Data)
		case protocol.TypeTunnelWSData:
			// Fast, non-blocking (buffered channel push) — must not stall the
			// shared read loop, so it never blocks on a slow backend.
			t.handleTunnelWSData(conn, m.Data)
		case protocol.TypeTunnelWSClose:
			t.handleTunnelWSClose(m.Data)
		}
	}
}

// handleTunnelReq parses one tunnel_req payload, performs the local
// HTTP request, and sends back a tunnel_resp. Errors surface to the
// visitor as a 502 with the message in the body.
func (t *Tunnel) handleTunnelReq(conn *websocket.Conn, payload string) {
	// Spawned per relay-forwarded request (go t.handleTunnelReq); a panic here would
	// crash the port-forward. Contain it so one bad request just fails.
	defer func() {
		if r := recover(); r != nil {
			recoverLog("handleTunnelReq", r)
		}
	}()
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
	// Build the target with a FIXED local host and ONLY the visitor's path+query.
	// String-concatenating req.URL would let a crafted value (e.g. "@evil.com/",
	// an absolute URL) redirect the proxied request off-box — an open-proxy / SSRF
	// pivot to internal services. Taking just Path+RawQuery and letting url.URL
	// re-add a leading "/" makes the host impossible to confuse.
	ref, perr := url.Parse(req.URL)
	if perr != nil {
		t.sendError(conn, req.ReqID, "bad request url")
		return
	}
	target := (&url.URL{
		Scheme:   t.backendScheme(),
		Host:     fmt.Sprintf("127.0.0.1:%d", t.port),
		Path:     ref.Path,
		RawQuery: ref.RawQuery,
	}).String()
	httpReq, err := http.NewRequest(strings.ToUpper(req.Method), target, body)
	if err != nil {
		t.sendError(conn, req.ReqID, fmt.Sprintf("build request: %v", err))
		return
	}
	for k, v := range req.Headers {
		if isHopHeader(k) {
			continue
		}
		// Never forward the visitor's Accept-Encoding. If we did, Go's Transport
		// treats compression as caller-managed and hands us the raw, still-
		// compressed body with its Content-Encoding intact — which later gets
		// dropped in the relay, so the browser renders gzip bytes as gibberish.
		// By omitting it we let the Transport add its own Accept-Encoding: gzip
		// and transparently decompress, so we always forward an identity body.
		// Cloudflare re-compresses to the visitor at the edge.
		if strings.EqualFold(k, "Accept-Encoding") {
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

	headers := map[string]string{}
	for k, v := range resp.Header {
		if isHopHeader(k) || len(v) == 0 {
			continue
		}
		headers[k] = v[0]
	}

	// Stream the body as one or more tunnel_resp chunks. The FIRST chunk carries
	// status + headers; every chunk carries `more` (true = another follows). The
	// relay hands the visitor a streamed Response and enqueues each chunk as it
	// arrives, so a large body is no longer capped at one 1-MiB WS message. A
	// single-chunk body (the common case) is one message with more=false, which
	// an older relay still treats as a whole response.
	buf := make([]byte, tunnelChunkBytes)
	var total int64
	firstSent := false
	send := func(chunk []byte, more bool) error {
		payload := map[string]any{
			"req_id": req.ReqID,
			"body":   base64.StdEncoding.EncodeToString(chunk),
			"more":   more,
		}
		if !firstSent {
			payload["status"] = resp.StatusCode
			payload["headers"] = headers
			firstSent = true
		}
		out, _ := json.Marshal(payload)
		return t.writeMsg(conn, protocol.Message{Type: protocol.TypeTunnelResp, Data: string(out)})
	}
	for {
		n, rerr := io.ReadFull(resp.Body, buf)
		total += int64(n)
		capped := total >= maxTunnelResponse
		switch {
		case rerr == nil:
			// Filled the buffer — more data may follow (or an exact-boundary EOF
			// next read). Stop early if we've hit the abuse cap.
			if err := send(buf[:n], !capped); err != nil || capped {
				return
			}
		case rerr == io.ErrUnexpectedEOF:
			// Final, partial chunk.
			_ = send(buf[:n], false)
			return
		case rerr == io.EOF:
			// EOF exactly on a chunk boundary (or an empty body): nothing more to
			// send, so close the stream. firstSent stays false only for a
			// zero-length body — send an empty final chunk so status+headers go out.
			_ = send(nil, false)
			return
		default:
			// Mid-stream read error. If we've already sent the head we can only
			// end the stream; otherwise surface a clean error to the visitor.
			if firstSent {
				_ = send(nil, false)
			} else {
				t.sendError(conn, req.ReqID, fmt.Sprintf("read response: %v", rerr))
			}
			return
		}
	}
}

// backendScheme learns—once, then caches—whether the local server on t.port
// speaks plain HTTP or HTTPS, so `reminal expose` transparently proxies either.
// A completed TLS handshake ⇒ "https" (webmin, UniFi, Proxmox and friends serve
// HTTPS-only, usually with a self-signed cert); anything else ⇒ "http". If the
// backend is unreachable at probe time we deliberately DON'T cache the answer,
// so a server started after `reminal expose` is detected correctly on a later
// request instead of being pinned to the wrong scheme for the tunnel's life.
func (t *Tunnel) backendScheme() string {
	t.schemeMu.Lock()
	defer t.schemeMu.Unlock()
	if t.scheme != "" {
		return t.scheme
	}
	addr := fmt.Sprintf("127.0.0.1:%d", t.port)
	// A successful TLS handshake is the unambiguous signal of an HTTPS backend.
	// InsecureSkipVerify: we only care that the peer speaks TLS, not who it is.
	d := &net.Dialer{Timeout: schemeProbeTimeout}
	if c, err := tls.DialWithDialer(d, "tcp", addr, &tls.Config{InsecureSkipVerify: true}); err == nil {
		_ = c.Close()
		t.scheme = "https"
		return t.scheme
	}
	// Not TLS. If a plain TCP connection still opens, it's a live HTTP server.
	if c, err := net.DialTimeout("tcp", addr, schemeProbeTimeout); err == nil {
		_ = c.Close()
		t.scheme = "http"
		return t.scheme
	}
	// Backend down: use http for this attempt (the request will surface a clean
	// 502) but leave the cache empty so we re-probe next time.
	return "http"
}

// ---- port-forward WebSocket proxying ----

// handleTunnelWSOpen dials the local backend WebSocket for a visitor connection
// the relay just accepted, then starts the two pumps that shuttle frames both
// ways over the shared tunnel control socket. A dial failure is reported back so
// the relay can close the visitor side cleanly.
func (t *Tunnel) handleTunnelWSOpen(conn *websocket.Conn, payload string) {
	defer func() {
		if r := recover(); r != nil {
			recoverLog("handleTunnelWSOpen", r)
		}
	}()
	var req struct {
		StreamID string            `json:"stream_id"`
		URL      string            `json:"url"`
		Headers  map[string]string `json:"headers"`
	}
	if err := json.Unmarshal([]byte(payload), &req); err != nil || req.StreamID == "" {
		return
	}
	ref, perr := url.Parse(req.URL)
	if perr != nil {
		t.sendWSClose(conn, req.StreamID)
		return
	}
	scheme := "ws"
	if t.backendScheme() == "https" {
		scheme = "wss"
	}
	dialURL := (&url.URL{
		Scheme:   scheme,
		Host:     fmt.Sprintf("127.0.0.1:%d", t.port),
		Path:     ref.Path,
		RawQuery: ref.RawQuery,
	}).String()

	// Forward the visitor's headers, minus the ones the WebSocket dialer must own
	// itself (Upgrade/Connection/Sec-WebSocket-Key/Version/Extensions, Host). The
	// requested subprotocols move to the dialer's Subprotocols field so it can
	// negotiate them properly instead of us hand-rolling the header.
	hdr := http.Header{}
	var subprotocols []string
	for k, v := range req.Headers {
		if isHopHeader(k) || isWSReservedHeader(k) {
			continue
		}
		if strings.EqualFold(k, "Sec-WebSocket-Protocol") {
			for _, p := range strings.Split(v, ",") {
				if p = strings.TrimSpace(p); p != "" {
					subprotocols = append(subprotocols, p)
				}
			}
			continue
		}
		hdr.Set(k, v)
	}

	dialer := *websocket.DefaultDialer
	dialer.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	dialer.HandshakeTimeout = 15 * time.Second
	dialer.Subprotocols = subprotocols
	backend, resp, err := dialer.Dial(dialURL, hdr)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.sendWSClose(conn, req.StreamID)
		return
	}
	// Bound a single backend frame so it can't exceed the relay's per-message
	// cap once base64-wrapped in a tunnel_ws_data envelope (the DO limits each WS
	// message to ~1 MiB). An over-limit frame trips gorilla's read limit, which
	// closes just this backend socket — the shared control socket, and every
	// other proxied stream on it, stays up. Matches the HTTP path's chunk ceiling.
	backend.SetReadLimit(tunnelChunkBytes)

	st := &wsStream{
		conn:   backend,
		sendCh: make(chan wsFrame, wsStreamSendBuffer),
		done:   make(chan struct{}),
	}
	t.wsMu.Lock()
	// A stream id collision (shouldn't happen — relay uses UUIDs) would orphan the
	// prior socket; close its resources directly (not via closeWSStream, which
	// would re-lock and, keyed by id, hit the entry we're about to overwrite).
	if prev := t.wsStreams[req.StreamID]; prev != nil {
		prev.closeOnce.Do(func() {
			close(prev.done)
			_ = prev.conn.Close()
		})
	}
	t.wsStreams[req.StreamID] = st
	t.wsMu.Unlock()

	// Writer: visitor→backend frames drained from sendCh.
	go func() {
		for {
			select {
			case <-st.done:
				return
			case f := <-st.sendCh:
				_ = backend.SetWriteDeadline(time.Now().Add(wsWriteWait))
				mt := websocket.TextMessage
				if f.binary {
					mt = websocket.BinaryMessage
				}
				if err := backend.WriteMessage(mt, f.data); err != nil {
					t.closeWSStream(conn, req.StreamID, st, true)
					return
				}
			}
		}
	}()

	// Reader: backend→visitor frames pushed as tunnel_ws_data.
	go func() {
		for {
			mt, data, rerr := backend.ReadMessage()
			if rerr != nil {
				t.closeWSStream(conn, req.StreamID, st, true)
				return
			}
			t.sendWSData(conn, req.StreamID, data, mt == websocket.BinaryMessage)
		}
	}()
}

// handleTunnelWSData delivers one visitor→backend frame onto its stream's send
// queue. It never blocks the shared read loop: if the backend has fallen far
// enough behind that the queue is full, the stream is torn down instead.
func (t *Tunnel) handleTunnelWSData(conn *websocket.Conn, payload string) {
	var m struct {
		StreamID string `json:"stream_id"`
		Data     string `json:"data"`
		Binary   bool   `json:"binary"`
	}
	if err := json.Unmarshal([]byte(payload), &m); err != nil || m.StreamID == "" {
		return
	}
	t.wsMu.Lock()
	st := t.wsStreams[m.StreamID]
	t.wsMu.Unlock()
	if st == nil {
		return
	}
	raw, err := base64.StdEncoding.DecodeString(m.Data)
	if err != nil {
		return
	}
	select {
	case st.sendCh <- wsFrame{data: raw, binary: m.Binary}:
	case <-st.done:
	default:
		t.closeWSStream(conn, m.StreamID, st, true)
	}
}

// handleTunnelWSClose tears down a stream because the visitor side closed. No
// need to echo a close back to the relay — it initiated it.
func (t *Tunnel) handleTunnelWSClose(payload string) {
	var m struct {
		StreamID string `json:"stream_id"`
	}
	if err := json.Unmarshal([]byte(payload), &m); err != nil || m.StreamID == "" {
		return
	}
	t.wsMu.Lock()
	st := t.wsStreams[m.StreamID]
	t.wsMu.Unlock()
	// Visitor side initiated the close, so don't echo one back (notify=false).
	t.closeWSStream(nil, m.StreamID, st, false)
}

// closeWSStream stops both pumps, closes the backend socket, and (when notify is
// set, i.e. the backend side ended) tells the relay to close the visitor socket.
// The caller passes the exact *wsStream it owns so a stream-id reuse can never
// tear down a newer stream; the map entry is removed only if it still points at
// this stream. Idempotent via closeOnce, so the reader, writer, and control
// paths can all call it safely.
func (t *Tunnel) closeWSStream(conn *websocket.Conn, streamID string, st *wsStream, notify bool) {
	if st == nil {
		return
	}
	t.wsMu.Lock()
	if t.wsStreams[streamID] == st {
		delete(t.wsStreams, streamID)
	}
	t.wsMu.Unlock()
	st.closeOnce.Do(func() {
		close(st.done)
		_ = st.conn.Close()
		if notify && conn != nil {
			t.sendWSClose(conn, streamID)
		}
	})
}

func (t *Tunnel) closeAllWSStreams() {
	t.wsMu.Lock()
	streams := make(map[string]*wsStream, len(t.wsStreams))
	for id, st := range t.wsStreams {
		streams[id] = st
	}
	t.wsMu.Unlock()
	for id, st := range streams {
		t.closeWSStream(nil, id, st, false)
	}
}

func (t *Tunnel) sendWSData(conn *websocket.Conn, streamID string, data []byte, binary bool) {
	payload, _ := json.Marshal(map[string]any{
		"stream_id": streamID,
		"data":      base64.StdEncoding.EncodeToString(data),
		"binary":    binary,
	})
	_ = t.writeMsg(conn, protocol.Message{Type: protocol.TypeTunnelWSData, Data: string(payload)})
}

func (t *Tunnel) sendWSClose(conn *websocket.Conn, streamID string) {
	payload, _ := json.Marshal(map[string]any{"stream_id": streamID})
	_ = t.writeMsg(conn, protocol.Message{Type: protocol.TypeTunnelWSClose, Data: string(payload)})
}

// isWSReservedHeader reports headers the WebSocket dialer sets itself; forwarding
// them from the visitor would collide with the handshake gorilla performs.
func isWSReservedHeader(name string) bool {
	switch strings.ToLower(name) {
	case "sec-websocket-key", "sec-websocket-version", "sec-websocket-extensions", "host":
		return true
	}
	return false
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
	_ = conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
	return conn.WriteJSON(m)
}

func (t *Tunnel) writeHandshake() {
	if t.handshakeFD == 0 && t.handshakeAddr == "" {
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
	w, err := handshakeWriter(t.handshakeFD, t.handshakeAddr)
	if err != nil {
		return
	}
	_, _ = w.Write(append(data, '\n'))
	_ = w.Close()
	t.handshakeFD = 0
	t.handshakeAddr = ""
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
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	defer devnull.Close()

	args := []string{
		"--expose-headless",
		"--expose-port", fmt.Sprintf("%d", port),
	}
	if public {
		args = append(args, "--expose-public")
	}
	cmd := exec.Command(exe, args...)
	cmd.Stdin = devnull
	cmd.Stdout = devnull
	cmd.Stderr = devnull
	recv, afterStart, err := prepareHandshake(cmd)
	if err != nil {
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		afterStart()
		return nil, fmt.Errorf("start headless tunnel: %w", err)
	}
	afterStart()
	_ = cmd.Process.Release()

	line, err := recv(spawnHandshakeTimeout)
	if err != nil {
		return nil, err
	}
	var sp SpawnedSession
	if err := json.Unmarshal([]byte(line), &sp); err != nil {
		return nil, fmt.Errorf("parse handshake: %w", err)
	}
	return &sp, nil
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
