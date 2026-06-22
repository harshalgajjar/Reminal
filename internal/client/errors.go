package client

import (
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// rateLimitedError is returned when the relay's edge (Cloudflare on the
// workers.dev domain) responds 429 to the WS upgrade. The main reconnect
// loop uses this to switch to a 10-minute back-off instead of its usual
// 30-second cap — repeatedly retrying inside a throttle window only
// extends it.
type rateLimitedError struct {
	retryAfter time.Duration // 0 if the server didn't advise one
}

func (e *rateLimitedError) Error() string {
	if e.retryAfter > 0 {
		return "relay rate-limited (retry after " + e.retryAfter.String() + ")"
	}
	return "relay rate-limited"
}

// parseRetryAfter handles both Retry-After variants — seconds (an
// integer) or an HTTP-date string. Returns 0 when neither parses,
// letting the caller fall back to its own default.
func parseRetryAfter(h string) time.Duration {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0
	}
	if n, err := strconv.Atoi(h); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	if t, err := time.Parse(time.RFC1123, h); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// humanize turns a raw error (typically from a websocket dial, a relay error
// message, or a system call) into a one-line, actionable explanation aimed at
// a user who doesn't know reminal's internals. When REMINAL_DEBUG=1 is set in
// the environment, the raw error string is appended in parentheses so the
// debug audience still sees the full detail.
func humanize(err error) string {
	if err == nil {
		return ""
	}
	msg := classify(err)
	if os.Getenv("REMINAL_DEBUG") == "1" {
		return msg + " (" + err.Error() + ")"
	}
	return msg
}

func classify(err error) string {
	// Specific typed errors get explicit copy. rateLimitedError is the
	// one case where the agent must surface a clear "this is throttle,
	// not your session" message — the default "bad handshake → session
	// ended" mapping below is wrong and misleading here.
	var rl *rateLimitedError
	if errors.As(err, &rl) {
		if rl.retryAfter > 0 {
			return "Relay throttled this client (Cloudflare workers.dev rate limit) — backing off " + rl.retryAfter.String() + "."
		}
		return "Relay throttled this client (Cloudflare workers.dev rate limit) — backing off. Frequent reconnects extend the block; if this persists, move the relay onto a custom domain."
	}

	raw := err.Error()

	// Relay-supplied error messages already pass through humans; recognise
	// the canonical ones and return them as-is rather than re-wording.
	for _, known := range []string{
		"session not found or not ready",
		"another viewer is already connected to this session",
		"another agent is already connected to this session",
		"too many failed attempts",
		"incorrect PIN",
	} {
		if strings.Contains(raw, known) {
			return relayMessageHint(raw)
		}
	}

	// Network-layer classification using errors.Is/As where possible, then
	// falling back to substring matching for errors we don't import.
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "Connection timed out — the relay isn't responding. Check your network."
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return "Connection refused — the relay isn't accepting connections at that address."
	}
	if errors.Is(err, syscall.ECONNRESET) {
		return "Connection reset — the relay or a middlebox dropped the link."
	}
	if errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETUNREACH) {
		return "No route to the relay — check your internet connection."
	}

	switch {
	case strings.Contains(raw, "no such host"):
		return "Relay host not found — DNS lookup failed. Are you offline?"
	case strings.Contains(raw, "bad handshake"):
		return "Relay rejected the connection — the session may have ended or moved."
	case strings.Contains(raw, "tls"), strings.Contains(raw, "x509"):
		return "TLS handshake failed — clock skew or a corporate proxy may be intercepting traffic."
	case strings.Contains(raw, "use of closed"):
		return "Connection closed unexpectedly — reconnecting."
	case strings.Contains(raw, "EOF"):
		return "Relay closed the connection — reconnecting."
	}

	// Fall back to the raw error with a leading capital so it reads like a
	// sentence rather than a stack trace.
	if len(raw) > 0 {
		return strings.ToUpper(raw[:1]) + raw[1:]
	}
	return "Unknown error"
}

func relayMessageHint(msg string) string {
	switch {
	case strings.Contains(msg, "session not found"):
		return "Session not found — the host may have stopped reminal, or the session ID has a typo."
	case strings.Contains(msg, "another viewer"):
		return "Another viewer is already connected — only one viewer per session is allowed at a time."
	case strings.Contains(msg, "another agent"):
		return "Another agent is already registered for this session ID — wait a few minutes and try again."
	case strings.Contains(msg, "too many failed"):
		return "Too many wrong PINs — locked out for 5 minutes. Double-check the PIN and wait."
	case strings.Contains(msg, "incorrect PIN"):
		return "Incorrect PIN. Five wrong attempts trigger a 5-minute lockout."
	}
	return msg
}
