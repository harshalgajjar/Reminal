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
//        a) marshals session state (id, pin, started_at) into env vars
//        b) clears O_CLOEXEC on the PTY master fd so it survives Exec
//        c) restores the host terminal to cooked mode (so the new
//           agent can re-enter raw mode and capture the right "old
//           state" for its own restore-on-exit defer)
//        d) closes the listening control socket so the new agent can
//           re-bind it (PID stays the same across Exec)
//        e) calls syscall.Exec with REMINAL_RESUME=1 in env so the
//           new binary takes the resume boot path instead of
//           generating a fresh session
//   4. The new binary starts up. main detects REMINAL_RESUME=1, opens
//      the inherited PTY fd, builds an Agent with the resumed state,
//      runs the normal loop. Viewers see a < 1s disconnect and
//      reconnect to the same session ID.

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"syscall"
	"time"

	"github.com/reminal/reminal/internal/pty"
	"golang.org/x/sys/unix"
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
	// A fresh hash from the same PIN would fail the equality check
	// ("session credentials mismatch") and the resumed agent would
	// loop forever trying to reconnect.
	envResumePinHash   = "REMINAL_RESUME_PIN_HASH"
	envResumeStartedAt = "REMINAL_RESUME_STARTED_AT"
	// Always 3 — first ExtraFile after stdio. Kept as a constant in
	// case we ever pass more inherited fds.
	resumePTYFD = 3
)

// ResumeState is what the new process reconstructs from env vars after
// an Exec restart. nil from LoadResumeState() means "not resuming —
// take the normal fresh-startup path."
type ResumeState struct {
	SessionID string
	PIN       string
	// PinHash is the bcrypt hash the relay already has registered for
	// this session. We can't re-derive it (bcrypt has a random salt
	// per call) and the relay does a string-equality check, so we
	// have to pass the original through.
	PinHash   string
	StartedAt time.Time
	PTY       *pty.Session
}

// LoadResumeState reads REMINAL_RESUME=1 + the surrounding env vars
// from the freshly-Exec'd process and returns a ResumeState if we're
// resuming. Returns (nil, nil) for the common fresh-startup case so
// callers can fall through. Unsets all REMINAL_RESUME_* env vars so
// they don't leak into child processes (the spawned shell shouldn't
// see them).
func LoadResumeState() (*ResumeState, error) {
	if os.Getenv(envResume) != "1" {
		return nil, nil
	}
	id := os.Getenv(envResumeSessionID)
	pin := os.Getenv(envResumePIN)
	pinHash := os.Getenv(envResumePinHash)
	if id == "" || pin == "" || pinHash == "" {
		return nil, errors.New("resume requested but session id / pin / pin_hash missing")
	}
	startedAtUnix, _ := strconv.ParseInt(os.Getenv(envResumeStartedAt), 10, 64)
	startedAt := time.Unix(startedAtUnix, 0)
	if startedAtUnix == 0 {
		startedAt = time.Now()
	}

	// fd 3 is the inherited PTY master. Wrap it as an *os.File then
	// hand to pty.Attach so the new agent can read/write it without
	// re-spawning the shell.
	ptyFile := os.NewFile(uintptr(resumePTYFD), "ptmx")
	if ptyFile == nil {
		return nil, errors.New("resume: failed to open inherited pty fd")
	}

	// Scrub env so nothing downstream (the shell we never spawn, any
	// child commands, `reminal info` from inside it) sees these.
	_ = os.Unsetenv(envResume)
	_ = os.Unsetenv(envResumeSessionID)
	_ = os.Unsetenv(envResumePIN)
	_ = os.Unsetenv(envResumePinHash)
	_ = os.Unsetenv(envResumeStartedAt)

	return &ResumeState{
		SessionID: id,
		PIN:       pin,
		PinHash:   pinHash,
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

	// Tell the new image who we are. PTY survives via fd inheritance;
	// everything else is in env. pinHash MUST be the original bcrypt
	// the relay already has registered — re-deriving would generate
	// a different salt/digest and the relay would reject the resumed
	// agent with "session credentials mismatch".
	env := append(os.Environ(),
		envResume+"=1",
		envResumeSessionID+"="+a.sessionID,
		envResumePIN+"="+a.pin,
		envResumePinHash+"="+a.pinHash,
		envResumeStartedAt+"="+strconv.FormatInt(a.startedAt.Unix(), 10),
	)

	// Surface the PTY master as fd 3 in the new process. Clear
	// O_CLOEXEC if set so syscall.Exec doesn't slam it shut.
	ptyFD, err := ptyMasterFD(a.term)
	if err != nil {
		return fmt.Errorf("locate pty master fd: %w", err)
	}
	if err := dupToFD3(ptyFD); err != nil {
		return fmt.Errorf("dup pty fd to 3: %w", err)
	}

	// Restore the host terminal to cooked mode so the new agent can
	// re-enter raw mode and capture the correct "previous" state for
	// its own defer-restore. Without this, the new agent's xterm.MakeRaw
	// returns the already-raw state as "old", so its eventual exit
	// would leave the user's terminal in raw mode.
	if a.localActive {
		if a.hostOldState != nil {
			_ = xterm.Restore(int(os.Stdin.Fd()), a.hostOldState)
		}
		clearHostIndicator()
	}

	// Let viewers know we're about to disappear briefly. They'll
	// auto-reconnect once the new image is up. Best-effort.
	agentNotify("\n  [%s] Restarting reminal in place — viewers will briefly disconnect…\n",
		time.Now().Format("15:04:05"))

	// Close the control-socket listener so the new process can re-bind
	// the same path (PID is preserved across Exec). The listener fd is
	// otherwise inherited but tied to the old in-memory state.
	a.stopControlListener()

	// Replace this process. On success, never returns.
	if err := syscall.Exec(exe, []string{exe}, env); err != nil {
		return fmt.Errorf("exec %s: %w", exe, err)
	}
	// Unreachable.
	return nil
}

// dupToFD3 ensures the PTY master sits at fd 3 in the new image. If
// it's already there, no-op. Also clears O_CLOEXEC so Exec inherits it.
func dupToFD3(src int) error {
	if src != 3 {
		// unix.Dup2 wraps either the legacy dup2 (macOS, x86_64
		// Linux) or modern dup3(..., 0) (arm64 Linux, where dup2
		// was retired).
		if err := unix.Dup2(src, 3); err != nil {
			return err
		}
	}
	// Clear close-on-exec on fd 3 — without this, Exec would still
	// shut the fd before the new image gets to see it.
	if _, err := fcntl(3, syscall.F_SETFD, 0); err != nil {
		return err
	}
	return nil
}

// ptyMasterFD digs the underlying fd out of the pty.Session. The
// package doesn't expose it directly so we use the io.File interface
// pattern — Session embeds an *os.File internally and the existing
// Read/Write/Resize methods all forward to it. For the restart path
// we add a small accessor below in the pty package, called via this
// indirection.
func ptyMasterFD(s *pty.Session) (int, error) {
	type fdHolder interface{ Fd() uintptr }
	if h, ok := any(s).(fdHolder); ok {
		return int(h.Fd()), nil
	}
	return 0, errors.New("pty session does not expose underlying fd")
}

// fcntl is a tiny wrapper because syscall.Fcntl isn't exposed on every
// platform identically. Returns (ret, err).
func fcntl(fd int, cmd int, arg int) (int, error) {
	r1, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), uintptr(cmd), uintptr(arg))
	if errno != 0 {
		return int(r1), errno
	}
	return int(r1), nil
}
