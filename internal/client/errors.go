package client

import (
	"errors"
	"net"
	"os"
	"strings"
	"syscall"
)

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
