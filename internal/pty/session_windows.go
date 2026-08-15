// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build windows

package pty

import (
	"fmt"
	"io"
	"os"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

type Session struct {
	hpc     windows.Handle // the pseudo console (HPCON)
	inPipe  *os.File       // our write end → shell stdin
	outPipe *os.File       // our read end ← shell output
	proc    windows.Handle // shell process handle, owned by the reaper goroutine
	pid     int

	mu        sync.Mutex // guards cols/rows and hpcClosed
	cols      uint16
	rows      uint16
	hpcClosed bool

	waitDone chan struct{} // closed by the reaper once the shell has exited
	waitErr  error         // written before waitDone is closed

	closeOnce sync.Once
	closeErr  error
}

func Start(shell string, env ...string) (*Session, error) {
	// ConPTY wiring: two anonymous pipe pairs. The pseudo console gets the
	// input-read and output-write ends; we keep the opposite ends and expose
	// them through Read/Write. There is no login-shell argv trick here — that
	// is a Unix mechanism; Windows shells configure themselves from the
	// registry and user profile on their own.
	inRead, inWrite, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	outRead, outWrite, err := os.Pipe()
	if err != nil {
		inRead.Close()
		inWrite.Close()
		return nil, err
	}

	const defaultCols, defaultRows = 80, 24
	var hpc windows.Handle
	err = windows.CreatePseudoConsole(
		windows.Coord{X: defaultCols, Y: defaultRows},
		windows.Handle(inRead.Fd()),
		windows.Handle(outWrite.Fd()),
		0,
		&hpc,
	)
	// CreatePseudoConsole duplicates the handles it needs, so the ConPTY-side
	// ends are ours to drop immediately — success or failure.
	inRead.Close()
	outWrite.Close()
	if err != nil {
		inWrite.Close()
		outRead.Close()
		return nil, fmt.Errorf("CreatePseudoConsole: %w", err)
	}

	fail := func(err error) (*Session, error) {
		windows.ClosePseudoConsole(hpc)
		inWrite.Close()
		outRead.Close()
		return nil, err
	}

	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return fail(err)
	}
	defer attrs.Delete()
	// Per Microsoft's ConPTY documentation the attribute VALUE is the HPCON
	// itself, not a pointer to it. go vet's unsafeptr check rejects a direct
	// uintptr→Pointer conversion, so reinterpret the handle's bits through its
	// address instead; the resulting unsafe.Pointer carries the handle value.
	hpcValue := *(*unsafe.Pointer)(unsafe.Pointer(&hpc))
	if err := attrs.Update(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE, hpcValue, unsafe.Sizeof(hpc)); err != nil {
		return fail(fmt.Errorf("attach pseudo console attribute: %w", err))
	}

	cmdline, err := windows.UTF16PtrFromString(windows.ComposeCommandLine([]string{shell}))
	if err != nil {
		return fail(err)
	}

	siEx := new(windows.StartupInfoEx)
	siEx.Cb = uint32(unsafe.Sizeof(*siEx))
	siEx.ProcThreadAttributeList = attrs.List()

	var pi windows.ProcessInformation
	// CurrentDir nil → the shell inherits our working directory.
	err = windows.CreateProcess(
		nil,
		cmdline,
		nil,
		nil,
		false,
		windows.EXTENDED_STARTUPINFO_PRESENT|windows.CREATE_UNICODE_ENVIRONMENT,
		utf16EnvBlock(shellEnv(env)),
		nil,
		&siEx.StartupInfo,
		&pi,
	)
	if err != nil {
		return fail(fmt.Errorf("CreateProcess %s: %w", shell, err))
	}
	windows.CloseHandle(pi.Thread) // never needed; the process handle is kept for Wait

	s := &Session{
		hpc:      hpc,
		inPipe:   inWrite,
		outPipe:  outRead,
		proc:     pi.Process,
		pid:      int(pi.ProcessId),
		cols:     defaultCols,
		rows:     defaultRows,
		waitDone: make(chan struct{}),
	}
	go s.reap()
	return s, nil
}

// reap waits for the shell to exit, then closes the pseudo console. That
// ordering is the load-bearing liveness detail of this file: ConPTY's output
// pipe does NOT deliver EOF when the shell exits — conhost keeps its copy of
// the write end open until ClosePseudoConsole is called. reminal's agent
// detects shell exit by the read pump returning, so without this goroutine a
// dead shell would leave the session (and its viewers) hanging forever.
// Closing the console EOFs outPipe, the pump drains the tail and returns, and
// Wait callers unblock via waitDone.
func (s *Session) reap() {
	_, err := windows.WaitForSingleObject(s.proc, windows.INFINITE)
	if err == nil {
		var code uint32
		if e := windows.GetExitCodeProcess(s.proc, &code); e != nil {
			err = e
		} else if code != 0 {
			err = fmt.Errorf("exit status %d", code)
		}
	}
	s.waitErr = err
	s.closePseudoConsole()
	windows.CloseHandle(s.proc)
	close(s.waitDone)
}

// closePseudoConsole closes the HPCON exactly once. Both the reaper and
// Close race to call it; the mutex + flag make that safe.
func (s *Session) closePseudoConsole() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.hpcClosed {
		s.hpcClosed = true
		windows.ClosePseudoConsole(s.hpc)
	}
}

func (s *Session) Read(p []byte) (int, error) {
	return s.outPipe.Read(p)
}

func (s *Session) Write(p []byte) (int, error) {
	return s.inPipe.Write(p)
}

func (s *Session) Resize(cols, rows uint16) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hpcClosed {
		return os.ErrClosed
	}
	if err := windows.ResizePseudoConsole(s.hpc, windows.Coord{X: int16(cols), Y: int16(rows)}); err != nil {
		return err
	}
	s.cols, s.rows = cols, rows
	return nil
}

// Getsize reports the last size set on the pseudo console (the 80x24 default
// from Start until a viewer resizes it). ConPTY has no getter, so we remember
// what we told it.
func (s *Session) Getsize() (cols, rows uint16, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cols, s.rows, nil
}

func (s *Session) Wait() error {
	<-s.waitDone
	return s.waitErr
}

// Close tears the session down: pseudo console first (which also detaches —
// and thereby terminates — a still-running shell), then our pipe ends. The
// HPCON close must come first so any Read blocked on outPipe unblocks before
// outPipe.Close waits for it. Idempotent, and safe against the concurrent
// reaper via closePseudoConsole's once-semantics.
func (s *Session) Close() error {
	s.closeOnce.Do(func() {
		s.closePseudoConsole()
		s.closeErr = s.inPipe.Close()
		if err := s.outPipe.Close(); err != nil && s.closeErr == nil {
			s.closeErr = err
		}
	})
	return s.closeErr
}

// Fd is only meaningful on the Unix hot-restart path, which passes the PTY
// master across a syscall.Exec boundary. There is no equivalent on Windows.
func (s *Session) Fd() uintptr {
	return 0
}

// Pid returns the spawned shell's process id, used to read the live working
// directory for `reminal list`.
func (s *Session) Pid() int {
	return s.pid
}

func (s *Session) CopyFrom(r io.Reader, done chan<- struct{}) {
	defer close(done)
	_, _ = io.Copy(s.inPipe, r)
}

func (s *Session) CopyTo(w io.Writer, done chan<- struct{}) {
	defer close(done)
	_, _ = io.Copy(w, s.outPipe)
}

// HandleSignals is a no-op on Windows: there is no SIGWINCH, and resizes
// arrive exclusively as relay messages from the viewer.
func HandleSignals() {}

// utf16EnvBlock encodes "K=V" strings as the CreateProcessW environment
// block: each entry UTF-16 with its NUL terminator, the whole block ending in
// an extra NUL (so it is double-NUL terminated). CREATE_UNICODE_ENVIRONMENT
// tells CreateProcess to read it as UTF-16.
func utf16EnvBlock(env []string) *uint16 {
	var block []uint16
	for _, kv := range env {
		u, err := windows.UTF16FromString(kv) // trailing NUL included
		if err != nil {
			continue // an interior NUL can't be represented; drop the entry
		}
		block = append(block, u...)
	}
	block = append(block, 0) // terminator; also covers the empty-env case
	return &block[0]
}
