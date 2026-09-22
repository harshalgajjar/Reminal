// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"time"

	"reminal/internal/protocol"
)

// transcriptPINQuiet is how long the replay stream must fall silent before we
// treat the scrollback dump as complete. The agent paints a joining viewer with
// one snapshot frame (plus, rarely, a couple of trailing buffered frames) sent
// back-to-back on resume, so a short gap means the replay is done.
const transcriptPINQuiet = 400 * time.Millisecond

// transcriptPINDeadline caps the whole read so a stuck agent or relay can't hang
// an MCP call indefinitely.
const transcriptPINDeadline = 8 * time.Second

// ReadTranscriptPIN reads a session's scrollback using only its id and PIN — the
// read counterpart to SendKeysPIN, so an agent that was handed "session id + PIN"
// can both write (send_keys) and read (read_transcript) without the machine being
// enrolled as an owned box. It connects exactly as `reminal connect` would (EKE
// over the relay), asks for the full scrollback replay, captures the snapshot the
// agent paints for a joining viewer, and returns it as plain text.
//
// It is strictly read-only and non-disruptive: it never sends keystrokes, and it
// deliberately never reports a terminal size. Viewer sizes are min'd into the PTY
// geometry (see viewersize.go), so a reader that reported a size could SHRINK the
// live user's terminal — this path must not, so it stays silent on geometry and
// simply captures the paint at whatever size the session is already running.
func ReadTranscriptPIN(sessionID, pin string) (text string, truncated bool, err error) {
	v, err := NewViewer(sessionID, pin)
	if err != nil {
		return "", false, err
	}
	return v.readSnapshotOnce()
}

func (v *Viewer) readSnapshotOnce() (string, bool, error) {
	// Prefer a same-machine attach socket over the relay (works offline, skips the
	// cloud round-trip); falls back to the relay for a session on another box —
	// exactly like an interactive `reminal connect`.
	conn, local, resp, err := v.dial()
	if err != nil {
		if resp != nil && resp.StatusCode == 429 {
			return "", false, &rateLimitedError{retryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
		}
		return "", false, fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()
	conn.SetReadLimit(maxRelayMessageBytes) // untrusted peer — bound frame size
	_ = conn.SetReadDeadline(time.Now().Add(kexTimeout))

	// The relay auths the viewer on the session being live (relay path only; a
	// local attach talks straight to the agent). The PIN never leaves this
	// process — it authenticates end-to-end via the EKE below.
	if !local {
		if err := v.authenticate(conn); err != nil {
			return "", false, err
		}
	}
	if err := v.negotiateSessionKey(conn); err != nil {
		return "", false, err
	}

	// Ask for the full replay from the very first byte. FromSeq:0 makes the agent
	// paint the joining-viewer snapshot (screen + scrollback). We never send a
	// resize, so the PTY geometry the live user is on is left untouched.
	if err := v.writeMsg(conn, protocol.Message{Type: protocol.TypeResume, FromSeq: 0}); err != nil {
		return "", false, err
	}

	var buf bytes.Buffer
	overall := time.Now().Add(transcriptPINDeadline)
	gotData := false
	for {
		// Read until whichever comes first: the overall cap, or a short quiet
		// window after the last data frame (the replay has gone silent → done).
		deadline := overall
		if gotData {
			if q := time.Now().Add(transcriptPINQuiet); q.Before(deadline) {
				deadline = q
			}
		}
		_ = conn.SetReadDeadline(deadline)
		_, raw, rerr := conn.ReadMessage()
		if rerr != nil {
			// Once we've received data, a read timeout is the normal "replay is
			// quiet, we're done" exit. Before any data, a timeout means the agent
			// never answered — offline, or nothing to show.
			if gotData {
				break
			}
			if ne, ok := rerr.(net.Error); ok && ne.Timeout() {
				return "", false, fmt.Errorf("no scrollback received — the session may be offline or the agent is not connected")
			}
			return "", false, rerr
		}
		var msg protocol.Message
		if json.Unmarshal(raw, &msg) != nil {
			continue
		}
		switch msg.Type {
		case protocol.TypeData:
			pt, derr := v.box.Decrypt(msg.Data)
			if derr != nil {
				continue // a frame we can't decrypt (e.g. mid-rekey) — skip it
			}
			buf.Write(pt)
			gotData = true
			// The snapshot is one frame; a runaway stream (a session printing
			// continuously) is bounded so the read still returns promptly.
			if buf.Len() > maxSnapshotPlaintext {
				return finishPINTranscript(&buf)
			}
		case protocol.TypePing:
			_ = v.writeMsg(conn, protocol.Message{Type: protocol.TypePong})
		case protocol.TypeClosed:
			text := msg.Error
			if text == "" {
				text = "session ended"
			}
			if !gotData {
				return "", false, fmt.Errorf("%s", text)
			}
			return finishPINTranscript(&buf)
		case protocol.TypeError:
			if !gotData {
				return "", false, fmt.Errorf("%s", msg.Error)
			}
			return finishPINTranscript(&buf)
		}
	}
	return finishPINTranscript(&buf)
}

// finishPINTranscript strips terminal chrome from the captured replay and clips
// it to the newest tail, matching the shape of the owned-session transcript.
func finishPINTranscript(buf *bytes.Buffer) (string, bool, error) {
	text, truncated := clipTranscript(stripANSI(buf.String()), maxTranscriptBytes)
	return text, truncated, nil
}
