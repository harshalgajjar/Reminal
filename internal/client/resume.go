// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"time"

	"github.com/reminal/reminal/internal/pty"
)

// ResumeState is what the new process reconstructs from env vars after
// an Exec restart. nil from LoadResumeState() means "not resuming —
// take the normal fresh-startup path." Hot restart is Unix-only (it rides
// syscall.Exec); the type lives here untagged because the fresh-startup
// callers reference it on every platform.
type ResumeState struct {
	SessionID string
	PIN       string
	PinHash   string
	Token     string
	StartedAt time.Time
	PTY       *pty.Session
	// Name is the user-set session label, carried through env on Windows —
	// the predecessor's on-disk record can't be trusted for recovery there,
	// since its pid is already dead by the time the successor reads it (and
	// liveness pruning treats such records as stale). Unix leaves this empty
	// and recovers the name from the record, which its same-pid restart keeps
	// valid.
	Name string
	// Headless mirrors the predecessor's mode — a hot-restarted background
	// session must keep reporting (and behaving) headless.
	Headless bool
	// HandshakeAddr is Windows-only: the OLD agent's loopback listener, so the
	// resumed agent can report "registered" and release it to exit. Unix's
	// exec-based restart has no separate process to notify.
	HandshakeAddr string
	// Dump is the predecessor's scrollback, decrypted on the old side and
	// re-encrypted here under the new session key. Nil when the predecessor
	// was an older binary, or when the dump was empty / unreadable.
	Dump *scrollbackDump
}
