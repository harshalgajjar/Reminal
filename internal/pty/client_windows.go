// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build windows

package pty

// The agent-facing Session on Windows: a client of the per-session ConPTY
// holder (see holder_windows.go for the why and the wire protocol). Start
// spawns the holder and connects; AttachHolder just connects — that's the
// whole hot-restart resume path.

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// detachedProcessFlag is CreateProcess's DETACHED_PROCESS: the holder gets no
// console and survives the agent's console closing.
const detachedProcessFlag = 0x00000008

type Session struct {
	conn net.Conn
	sock string
	pid  int // shell pid, from the holder's hello

	mu   sync.Mutex // guards cols/rows and buf
	cols uint16
	rows uint16
	buf  []byte // undelivered tail of the last data frame

	writeMu sync.Mutex // serialises outgoing frames

	waitDone chan struct{}
	waitErr  error
	waitOnce sync.Once

	closeOnce sync.Once
	closeErr  error
}

func Start(shell string, env ...string) (*Session, error) {
	sock, err := HolderSocketPath()
	if err != nil {
		return nil, err
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}

	// Measure the console HERE, in the agent: the holder is spawned detached
	// (no console of its own) and would otherwise build the pseudo console at
	// 80x24, forcing an immediate resize — which on Windows costs the user
	// their screen and scrollback (see startDirect). The cursor goes with it:
	// it is where the banner ended, so it is where the shell must start if the
	// banner and QR are to survive.
	cols, rows := consoleSize()
	curRow, curCol := consoleCursor()
	cmd := exec.Command(exe, "__ptyhold", sock, shell,
		strconv.Itoa(int(cols)), strconv.Itoa(int(rows)),
		strconv.Itoa(int(curRow)), strconv.Itoa(int(curCol)))
	// The holder composes the shell's environment from its own (shellEnv), so
	// session-specific extras ride the holder's env block. The one-shot
	// handshake token is stripped first: the shell has no business seeing the
	// secret that authenticates the credentials report to our parent.
	base := os.Environ()
	kept := base[:0]
	for _, kv := range base {
		if !strings.HasPrefix(kv, "REMINAL_HS_TOKEN=") {
			kept = append(kept, kv)
		}
	}
	cmd.Env = append(kept, env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | detachedProcessFlag,
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start pty holder: %w", err)
	}
	_ = cmd.Process.Release() // it outlives us by design

	s, err := AttachHolder(sock)
	if err != nil {
		return nil, fmt.Errorf("connect to pty holder: %w", err)
	}
	return s, nil
}

// AttachHolder connects to an already-running holder — the fresh-start path
// right after spawning it, and the ENTIRE hot-restart resume path.
func AttachHolder(sock string) (*Session, error) {
	var conn net.Conn
	var err error
	// The holder needs a beat to create the ConPTY and bind the socket.
	deadline := time.Now().Add(10 * time.Second)
	for {
		conn, err = net.Dial("unix", sock)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("dial %s: %w", sock, err)
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Claim the session: the holder only adopts (and supersedes the previous
	// client) after this frame, so probes can't hijack a live session.
	if err := writeFrame(conn, frClaim, nil); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("claim holder: %w", err)
	}
	// A wedged holder must fail the attach, not hang it forever.
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	typ, payload, err := readFrame(conn)
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil || typ != frHello || len(payload) != 8 {
		_ = conn.Close()
		if err == nil {
			err = fmt.Errorf("unexpected frame 0x%02x", typ)
		}
		return nil, fmt.Errorf("holder hello: %w", err)
	}
	s := &Session{
		conn:     conn,
		sock:     sock,
		pid:      int(binary.LittleEndian.Uint32(payload[0:])),
		cols:     binary.LittleEndian.Uint16(payload[4:]),
		rows:     binary.LittleEndian.Uint16(payload[6:]),
		waitDone: make(chan struct{}),
	}
	return s, nil
}

// SockPath is the holder's socket — the one value a hot restart must thread
// through to the next agent (env REMINAL_RESUME_PTY_SOCK).
func (s *Session) SockPath() string { return s.sock }

func (s *Session) Read(p []byte) (int, error) {
	s.mu.Lock()
	if len(s.buf) > 0 {
		n := copy(p, s.buf)
		s.buf = s.buf[n:]
		s.mu.Unlock()
		return n, nil
	}
	s.mu.Unlock()

	for {
		typ, payload, err := readFrame(s.conn)
		if err != nil {
			// Socket gone without an exit frame: holder crash or our own
			// Close. Either way the session is over.
			s.finish(errHolderGone)
			return 0, io.EOF
		}
		switch typ {
		case frData:
			if len(payload) == 0 {
				continue
			}
			n := copy(p, payload)
			if n < len(payload) {
				s.mu.Lock()
				s.buf = append(s.buf, payload[n:]...)
				s.mu.Unlock()
			}
			return n, nil
		case frExit:
			var werr error
			if len(payload) == 4 {
				if code := int32(binary.LittleEndian.Uint32(payload)); code != 0 {
					werr = fmt.Errorf("exit status %d", code)
				}
			}
			s.finish(werr)
			return 0, io.EOF
		}
	}
}

func (s *Session) finish(err error) {
	s.waitOnce.Do(func() {
		s.waitErr = err
		close(s.waitDone)
	})
}

func (s *Session) Write(p []byte) (int, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	for off := 0; off < len(p); off += maxFramePayload {
		end := off + maxFramePayload
		if end > len(p) {
			end = len(p)
		}
		if err := writeFrame(s.conn, frData, p[off:end]); err != nil {
			return off, err
		}
	}
	return len(p), nil
}

func (s *Session) Resize(cols, rows uint16) error {
	s.mu.Lock()
	unchanged := s.cols == cols && s.rows == rows
	s.mu.Unlock()
	if unchanged {
		// Don't even ask. A ConPTY resize repaints, and with PowerShell
		// attached it also wipes the host console's screen and scrollback
		// (see startDirect) — so a re-assertion of the current size is not
		// the harmless no-op it is on Unix. The holder screens for this too.
		return nil
	}
	var payload [4]byte
	binary.LittleEndian.PutUint16(payload[0:], cols)
	binary.LittleEndian.PutUint16(payload[2:], rows)
	s.writeMu.Lock()
	err := writeFrame(s.conn, frResize, payload[:])
	s.writeMu.Unlock()
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.cols, s.rows = cols, rows
	s.mu.Unlock()
	return nil
}

// Getsize reports the holder's current geometry — seeded from its hello (so a
// resumed agent starts at the size the shell is really rendering at), tracked
// through our own resizes after that.
func (s *Session) Getsize() (cols, rows uint16, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cols, s.rows, nil
}

// Dead reports (without blocking) whether this client's link to the shell is
// over — the shell exited, the holder vanished, or a hot-restart successor
// superseded this connection. Used by executeRestart to decide whether a
// failed handoff can be safely reclaimed.
func (s *Session) Dead() bool {
	select {
	case <-s.waitDone:
		return true
	default:
		return false
	}
}

// Wait blocks until the shell inside the holder exits. The exit signal
// arrives through the read stream (frExit), so Wait completes when the read
// pump has drained everything before it — same ordering the Unix Session has.
func (s *Session) Wait() error {
	<-s.waitDone
	return s.waitErr
}

// Close ends the SESSION: ask the holder to kill the shell, then drop the
// connection. (A hot restart must NOT call this — the old agent just exits
// and lets the socket drop; the holder keeps serving the successor.)
func (s *Session) Close() error {
	s.closeOnce.Do(func() {
		s.writeMu.Lock()
		_ = writeFrame(s.conn, frKill, nil)
		s.writeMu.Unlock()
		s.closeErr = s.conn.Close()
		s.finish(errHolderGone)
	})
	return s.closeErr
}

// Fd is only meaningful on the Unix hot-restart path (fd across exec).
func (s *Session) Fd() uintptr { return 0 }

// Pid returns the shell's process id, used for the live-cwd column.
func (s *Session) Pid() int { return s.pid }

// ForegroundPgrp has no analog on Windows (no tty process groups); attention
// detection falls back to the alt-screen signal there. Returns 0.
func (s *Session) ForegroundPgrp() int { return 0 }

func (s *Session) CopyFrom(r io.Reader, done chan<- struct{}) {
	defer close(done)
	_, _ = io.Copy(s, r)
}

func (s *Session) CopyTo(w io.Writer, done chan<- struct{}) {
	defer close(done)
	_, _ = io.Copy(w, s)
}
