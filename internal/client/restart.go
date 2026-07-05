// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

// Hot-restart support: replace the running agent's binary image with a
// freshly-on-disk one via syscall.Exec, preserving the PTY (and thus
// the shell + everything running inside) plus the session ID/PIN so
// viewers reconnect to the same session. Same idea as nginx hot
// reload.
//
// The flow:
//
//   1. User runs `reminal restart` (typically from inside the shared
//      shell after `reminal upgrade` replaced the binary on disk).
//   2. The CLI sends "restart" over the local control socket.
//   3. The receiving agent calls executeRestart below, which:
//        a) tells viewers we're going dark briefly
//        b) restores the host terminal to cooked mode
//        c) closes the listening control socket (so the new image can
//           re-bind the same path) and any other goroutine-y work
//        d) clears O_CLOEXEC on the PTY master fd so it survives Exec
//        e) marshals session state (id, pin, pin_hash, started_at,
//           pty_fd) into env vars
//        f) calls syscall.Exec with REMINAL_RESUME=1 in env so the
//           new binary takes the resume boot path instead of
//           generating a fresh session
//   4. The new binary starts up. main detects REMINAL_RESUME=1, opens
//      the inherited PTY fd at the env-supplied number, builds an
//      Agent with the resumed state, runs the normal loop. Viewers
//      see a < 1s disconnect and reconnect to the same session ID.
//
// Critical ordering note: anything that involves the Go runtime
// (agentNotify, terminal restore — both eventually schedule across
// threads) MUST happen BEFORE the fd dance. Otherwise the runtime's
// netpoller (kqueue/epoll) can fire on the manipulated fds while
// the runtime still thinks they're its own, and crash with "kevent
// failed with 9" (EBADF). Once the fd manipulation starts, we must
// hit syscall.Exec without any further Go runtime activity.

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"syscall"
	"time"

	"github.com/reminal/reminal/internal/pty"
	xterm "golang.org/x/term"
)

// Env vars used to thread state across the Exec boundary. Anything not
// in this set is lost (scrollback, viewer counts, etc.) — they'll be
// rebuilt as viewers reconnect. The receiving end reads these in
// LoadResumeState() below.
const (
	envResume          = "REMINAL_RESUME"
	envResumeSessionID = "REMINAL_RESUME_SESSION_ID"
	envResumePIN       = "REMINAL_RESUME_PIN"
	// envResumePinHash carries the ORIGINAL bcrypt hash, not a freshly
	// re-derived one — bcrypt is non-deterministic (random salt per
	// call), and the relay compares the agent-reported pin_hash for
	// strict equality against the value it captured at first connect.
	envResumePinHash = "REMINAL_RESUME_PIN_HASH"
	// envResumeToken carries the high-entropy reattach token (Level B) across
	// the exec. Absent when we were restarted by an older binary that predates
	// tokens — the new image treats that as a legacy session and migrates it.
	envResumeToken     = "REMINAL_RESUME_TOKEN"
	envResumeStartedAt = "REMINAL_RESUME_STARTED_AT"
	// envResumePTYFD holds the fd number of the inherited PTY master.
	// We pass the actual fd (whatever Go assigned it — e.g. 7) instead
	// of dup2-ing it to a fixed slot. Dup2-ing over a low fd that Go's
	// netpoller had registered with kqueue/epoll causes the runtime to
	// crash on its next poll cycle with EBADF.
	envResumePTYFD = "REMINAL_RESUME_PTY_FD"
)

// ResumeState is what the new process reconstructs from env vars after
// an Exec restart. nil from LoadResumeState() means "not resuming —
// take the normal fresh-startup path."
type ResumeState struct {
	SessionID string
	PIN       string
	PinHash   string
	Token     string
	StartedAt time.Time
	PTY       *pty.Session
}

// LoadResumeState reads REMINAL_RESUME=1 + the surrounding env vars
// from the freshly-Exec'd process and returns a ResumeState if we're
// resuming. Returns (nil, nil) for the common fresh-startup case so
// callers can fall through. Unsets all REMINAL_RESUME_* env vars so
// they don't leak into child processes.
func LoadResumeState() (*ResumeState, error) {
	if os.Getenv(envResume) != "1" {
		return nil, nil
	}
	id := os.Getenv(envResumeSessionID)
	pin := os.Getenv(envResumePIN)
	pinHash := os.Getenv(envResumePinHash)
	token := os.Getenv(envResumeToken) // may be empty (restarted by a pre-token binary)
	if id == "" || pin == "" || pinHash == "" {
		return nil, errors.New("resume requested but session id / pin / pin_hash missing")
	}
	fdStr := os.Getenv(envResumePTYFD)
	ptyFD, err := strconv.Atoi(fdStr)
	if err != nil || ptyFD < 3 {
		return nil, fmt.Errorf("resume: invalid pty fd %q", fdStr)
	}
	startedAtUnix, _ := strconv.ParseInt(os.Getenv(envResumeStartedAt), 10, 64)
	startedAt := time.Unix(startedAtUnix, 0)
	if startedAtUnix == 0 {
		startedAt = time.Now()
	}

	ptyFile := os.NewFile(uintptr(ptyFD), "ptmx")
	if ptyFile == nil {
		return nil, fmt.Errorf("resume: failed to open inherited pty fd %d", ptyFD)
	}

	// Scrub env so nothing downstream (the shell we never spawn, any
	// child commands, `reminal info` from inside it) sees these.
	_ = os.Unsetenv(envResume)
	_ = os.Unsetenv(envResumeSessionID)
	_ = os.Unsetenv(envResumePIN)
	_ = os.Unsetenv(envResumePinHash)
	_ = os.Unsetenv(envResumeToken)
	_ = os.Unsetenv(envResumePTYFD)
	_ = os.Unsetenv(envResumeStartedAt)

	return &ResumeState{
		SessionID: id,
		PIN:       pin,
		PinHash:   pinHash,
		Token:     token,
		StartedAt: startedAt,
		PTY:       pty.Attach(ptyFile),
	}, nil
}

// executeRestart performs the in-place Exec into a fresh binary image.
// Never returns on success — the calling process is replaced. Returns
// an error only when the Exec call itself fails (binary missing,
// permissions, etc.).
func (a *Agent) executeRestart() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate self for restart: %w", err)
	}

	// ---- Phase 1: graceful tear-down with Go runtime still healthy.
	// We do everything that could trigger scheduling / syscall pumps
	// here, before touching any fd state. After this phase the runtime
	// must not fire any more poll cycles, because the fd numbering is
	// about to change underneath it.

	agentNotify("\n  [%s] Restarting reminal in place — viewers will briefly disconnect…\n",
		time.Now().Format("15:04:05"))

	// Restore the host terminal to cooked mode so the new agent can
	// re-enter raw mode and capture the correct "previous" state for
	// its own defer-restore. Without this, the new agent's
	// xterm.MakeRaw would return the already-raw state as "old", so
	// its eventual exit would leave the user's terminal in raw mode.
	if a.localActive {
		if a.hostOldState != nil {
			_ = xterm.Restore(int(os.Stdin.Fd()), a.hostOldState)
		}
		clearHostIndicator()
	}

	// Close the control-socket listener so the new process can re-bind
	// the same path (PID is preserved across Exec). Also de-registers
	// the listener's fd from Go's netpoller, which is the main reason
	// we want it gone before Exec — leaving it registered + then
	// stomping over the fd elsewhere caused the original "kevent on
	// fd 3 failed with 9" crash.
	a.stopControlListener()

	// ---- Phase 2: tight fd-and-exec sequence. No Go runtime calls
	// from here on.

	// Locate the PTY master fd — we pass its actual number via env so
	// the new image opens the same fd. (We deliberately don't dup it
	// onto a low slot like 3, because doing so collides with whatever
	// the runtime's netpoller is currently watching.)
	ptyFD := int(a.term.Fd())

	// Clear O_CLOEXEC so Exec doesn't shut the fd before the new image
	// gets to see it. Default on most Go-opened fds is to set it.
	if _, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(ptyFD), uintptr(syscall.F_SETFD), 0); errno != 0 {
		return fmt.Errorf("clear cloexec on pty fd %d: %w", ptyFD, errno)
	}

	env := append(os.Environ(),
		envResume+"=1",
		envResumeSessionID+"="+a.sessionID,
		envResumePIN+"="+a.pin,
		envResumePinHash+"="+a.pinHash,
		envResumeToken+"="+a.token,
		envResumePTYFD+"="+strconv.Itoa(ptyFD),
		envResumeStartedAt+"="+strconv.FormatInt(a.startedAt.Unix(), 10),
	)

	if err := syscall.Exec(exe, []string{exe}, env); err != nil {
		return fmt.Errorf("exec %s: %w", exe, err)
	}
	return nil // unreachable
}
