// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !windows

package pty

import (
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

type Session struct {
	ptmx *os.File
	cmd  *exec.Cmd
}

func Start(shell string, env ...string) (*Session, error) {
	cmd := exec.Command(shell)
	// Run it as a LOGIN shell — the mechanism login(1) and Terminal.app use:
	// argv[0] prefixed with "-". A login shell sources the full profile chain
	// (/etc/zprofile → path_helper, ~/.zprofile → `brew shellenv`, ~/.zshrc, …),
	// so the session's PATH and environment match a freshly-opened terminal rather
	// than whatever the spawning process happened to inherit. This is essential for
	// sessions started by the background host, which runs under launchd/systemd
	// with a bare PATH: a non-login shell there would be missing Homebrew, claude,
	// and everything else set up in the login profile — the session wouldn't behave
	// like a real terminal at all.
	cmd.Args[0] = "-" + filepath.Base(shell)
	cmd.Env = shellEnv(env)

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, err
	}

	return &Session{ptmx: ptmx, cmd: cmd}, nil
}

// Attach wraps an already-open PTY master fd as a Session. Used by the
// hot-restart path (syscall.Exec into a new binary) to take over the
// existing shell without re-spawning it. The shell process continues
// running unaware of the swap because its parent PID is preserved
// across Exec and its controlling terminal — the slave side of this
// PTY — is untouched. cmd is left nil; the new process didn't spawn
// the shell so there's no Cmd to Wait on. EOF on the master fd
// signals shell exit instead.
func Attach(ptmx *os.File) *Session {
	return &Session{ptmx: ptmx, cmd: nil}
}

func (s *Session) Read(p []byte) (int, error) {
	return s.ptmx.Read(p)
}

func (s *Session) Write(p []byte) (int, error) {
	return s.ptmx.Write(p)
}

func (s *Session) Resize(cols, rows uint16) error {
	return pty.Setsize(s.ptmx, &pty.Winsize{Cols: cols, Rows: rows})
}

// Getsize reports the PTY master's current window size. The hot-restart path
// inherits an already-sized PTY, so this recovers the dimensions the shell is
// actually rendering at before any viewer has reported a size.
func (s *Session) Getsize() (cols, rows uint16, err error) {
	ws, err := pty.GetsizeFull(s.ptmx)
	if err != nil {
		return 0, 0, err
	}
	return ws.Cols, ws.Rows, nil
}

func (s *Session) Wait() error {
	return s.cmd.Wait()
}

func (s *Session) Close() error {
	return s.ptmx.Close()
}

// Fd returns the underlying file descriptor of the PTY master. Used by
// the hot-restart path to pass the open PTY to the new binary across a
// syscall.Exec boundary.
func (s *Session) Fd() uintptr {
	return s.ptmx.Fd()
}

// Pid returns a PID representing what's running in the session, used to read
// the live working directory for `reminal list`.
//
// When we spawned the shell we have its PID directly. After a hot-restart we
// inherited the PTY master but not the child, so we fall back to the terminal's
// foreground process group (TIOCGPGRP on the master) — which also has the nice
// property of pointing at whatever is running in the foreground (e.g. an editor
// or `claude`), so the reported cwd tracks that. 0 if neither is available.
func (s *Session) Pid() int {
	if s.cmd != nil && s.cmd.Process != nil {
		return s.cmd.Process.Pid
	}
	if s.ptmx != nil {
		if pgrp, err := unix.IoctlGetInt(int(s.ptmx.Fd()), unix.TIOCGPGRP); err == nil && pgrp > 0 {
			return pgrp
		}
	}
	return 0
}

func (s *Session) CopyFrom(r io.Reader, done chan<- struct{}) {
	defer close(done)
	_, _ = io.Copy(s.ptmx, r)
}

func (s *Session) CopyTo(w io.Writer, done chan<- struct{}) {
	defer close(done)
	_, _ = io.Copy(w, s.ptmx)
}

func HandleSignals() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	go func() {
		for range ch {
			// SIGWINCH is handled by the relay resize messages from the viewer.
		}
	}()
}
