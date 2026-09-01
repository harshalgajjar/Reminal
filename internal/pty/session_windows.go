// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build windows

package pty

import (
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/term"
)

type directSession struct {
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

// startDirect launches shell under a fresh pseudo console. cols/rows is the
// geometry to build that console at; 0 means "work it out from our own console,
// or fall back to 80x24" (see consoleSize). Passing the real size matters — the
// holder runs detached with no console of its own, so only its launcher knows
// what the user is looking at, and getting it right here is what spares the
// user's screen a start-up resize (see below).
func startDirect(shell string, cols, rows uint16, inheritCursor bool, env ...string) (*directSession, error) {
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

	// Size the pseudo console to the console we're attached to, NOT to a
	// placeholder we then resize. A ConPTY resize is not the cheap ioctl a Unix
	// TIOCSWINSZ is: PowerShell reacts by blanking its whole buffer through the
	// Win32 fill API, which conhost's PowerShell shim translates into
	// "ESC[H ESC[2J ESC[3J" on the output stream (microsoft/terminal,
	// src/host/_stream.cpp: WriteClearScreen). Those bytes are mirrored straight
	// to the user's console, so a start-up resize wiped their screen AND their
	// scrollback — the join banner and QR code included — which read as "reminal
	// restarted my PowerShell and ate my history". Starting at the right size
	// means the first applyEffectiveSize is a no-op (see Resize) and nothing is
	// cleared at all.
	if cols == 0 || rows == 0 {
		cols, rows = consoleSize()
	}
	// INHERIT_CURSOR is what stops the console taking the whole screen. Without
	// it a pseudo console assumes it owns the terminal from the top-left: its
	// very first paint erases the viewport (VtEngine::StartPaint → _ClearScreen,
	// "\x1b[2J") and it anchors output at row 1 — so reminal's banner and QR,
	// printed moments earlier, are wiped or painted over. With it, the console
	// asks the terminal where the cursor is and starts BELOW what is already on
	// screen, exactly as a shell started from a Unix pty does.
	//
	// The flag alone is not enough: conhost only skips that first-paint clear
	// once the answer arrives (VtEngine::InheritCursor sets _firstPaint=false).
	// The holder answers it from the position its launcher measured — see
	// dsrResponder — so this never depends on the terminal replying in time.
	var flags uint32
	if inheritCursor {
		flags |= windows.PSEUDOCONSOLE_INHERIT_CURSOR
	}
	// RESIZE_QUIRK (undocumented, 0x2): ConPTY will not dump its viewport
	// back onto the output pipe on ResizePseudoConsole. Windows Terminal
	// and wezterm set this because they own reflow themselves — "quirky
	// resize" is "don't InvalidateAll when the terminal resizes"
	// (microsoft/terminal #4741, #16911). Reminal already bottom-anchors
	// the agent emulator and every viewer; without the flag, ConPTY's dump
	// is a second, top-anchored buffer fighting ours, and PowerShell's
	// whole-buffer fill on that dump is what WriteClearScreen translates
	// into ESC[H ESC[2J ESC[3J. Unknown on older OS builds: retry without.
	const pseudoConsoleResizeQuirk = 0x2
	flags |= pseudoConsoleResizeQuirk
	var hpc windows.Handle
	err = windows.CreatePseudoConsole(
		windows.Coord{X: int16(cols), Y: int16(rows)},
		windows.Handle(inRead.Fd()),
		windows.Handle(outWrite.Fd()),
		flags,
		&hpc,
	)
	if err != nil && flags&pseudoConsoleResizeQuirk != 0 {
		flags &^= pseudoConsoleResizeQuirk
		err = windows.CreatePseudoConsole(
			windows.Coord{X: int16(cols), Y: int16(rows)},
			windows.Handle(inRead.Fd()),
			windows.Handle(outWrite.Fd()),
			flags,
			&hpc,
		)
	}
	// CreatePseudoConsole duplicates the handles it needs, so the ConPTY-side
	// ends are ours to drop immediately — success or failure.
	inRead.Close()
	outWrite.Close()
	if err != nil {
		inWrite.Close()
		outRead.Close()
		return nil, fmt.Errorf("CreatePseudoConsole: %w", err)
	}

	fail := func(err error) (*directSession, error) {
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

	cmdline, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(shellArgv(shell)))
	if err != nil {
		return fail(err)
	}

	siEx := new(windows.StartupInfoEx)
	siEx.Cb = uint32(unsafe.Sizeof(*siEx))
	siEx.ProcThreadAttributeList = attrs.List()
	// STARTF_USESTDHANDLES with all three std handles NULL is load-bearing:
	// without it, a child spawned from a process that itself has a console
	// picks up that console's std handles, so the shell's output lands on the
	// PARENT's console and its input never comes from the ConPTY. With the
	// flag + null handles, the client's CRT re-opens the console it is
	// actually attached to — the pseudo console. (Same trick as Windows
	// Terminal and every working Go ConPTY wrapper.)
	siEx.Flags |= windows.STARTF_USESTDHANDLES

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

	s := &directSession{
		hpc:      hpc,
		inPipe:   inWrite,
		outPipe:  outRead,
		proc:     pi.Process,
		pid:      int(pi.ProcessId),
		cols:     cols,
		rows:     rows,
		waitDone: make(chan struct{}),
	}
	go s.reap()
	return s, nil
}

// shellArgv builds the child's argv. PowerShell gets -NoLogo: the session shell
// starts inside the terminal the user is already sitting in, so re-printing the
// version banner and copyright makes reminal look like it restarted their
// shell. (The Unix side gets the same effect for free — a login shell prints no
// banner.) Any other shell — cmd, a $SHELL-configured git-bash — is launched
// exactly as configured; guessing flags for shells we don't recognise would
// just break them.
func shellArgv(shell string) []string {
	switch strings.ToLower(filepath.Base(shell)) {
	case "pwsh.exe", "pwsh", "powershell.exe", "powershell":
		return []string{shell, "-NoLogo"}
	}
	return []string{shell}
}

// consoleCursor reports where this process's console cursor sits, as a 1-based
// position within the visible window — the form a cursor-position report takes.
// Returns 0,0 when there is no console to ask, which is the signal not to
// inherit a cursor at all.
//
// Measured in the AGENT, right after it prints the join banner, and handed to
// the holder: that is the row the shell must start on if the banner is to
// survive. The holder itself is detached and has no console.
func consoleCursor() (row, col uint16) {
	h := windows.Handle(os.Stdout.Fd())
	var info windows.ConsoleScreenBufferInfo
	if err := windows.GetConsoleScreenBufferInfo(h, &info); err != nil {
		return 0, 0
	}
	// Buffer coordinates → window-relative, 1-based. A console buffer is
	// usually taller than its window, so the difference is what CPR reports.
	r := int(info.CursorPosition.Y) - int(info.Window.Top) + 1
	c := int(info.CursorPosition.X) - int(info.Window.Left) + 1
	if r < 1 || c < 1 || r > math.MaxUint16 || c > math.MaxUint16 {
		return 0, 0
	}
	return uint16(r), uint16(c)
}

// consoleSize reports this process's console size, or 80x24 when it has none
// (a headless agent, or the detached holder — which is why Start measures it in
// the AGENT process and passes the answer down).
func consoleSize() (cols, rows uint16) {
	const defaultCols, defaultRows = 80, 24
	fd := int(os.Stdout.Fd())
	if !term.IsTerminal(fd) {
		return defaultCols, defaultRows
	}
	c, r, err := term.GetSize(fd)
	if err != nil || c <= 0 || r <= 0 {
		return defaultCols, defaultRows
	}
	// ConPTY takes a signed 16-bit COORD; clamp rather than wrap.
	if c > math.MaxInt16 {
		c = math.MaxInt16
	}
	if r > math.MaxInt16 {
		r = math.MaxInt16
	}
	return uint16(c), uint16(r)
}

// reap waits for the shell to exit, then closes the pseudo console. That
// ordering is the load-bearing liveness detail of this file: ConPTY's output
// pipe does NOT deliver EOF when the shell exits — conhost keeps its copy of
// the write end open until ClosePseudoConsole is called. reminal's agent
// detects shell exit by the read pump returning, so without this goroutine a
// dead shell would leave the session (and its viewers) hanging forever.
// Closing the console EOFs outPipe, the pump drains the tail and returns, and
// Wait callers unblock via waitDone.
func (s *directSession) reap() {
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
func (s *directSession) closePseudoConsole() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.hpcClosed {
		s.hpcClosed = true
		windows.ClosePseudoConsole(s.hpc)
	}
}

func (s *directSession) Read(p []byte) (int, error) {
	return s.outPipe.Read(p)
}

func (s *directSession) Write(p []byte) (int, error) {
	return s.inPipe.Write(p)
}

func (s *directSession) Resize(cols, rows uint16) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hpcClosed {
		return os.ErrClosed
	}
	if cols == s.cols && rows == s.rows {
		// Never re-assert the size we already have. Every ResizePseudoConsole
		// costs a full repaint and, with PowerShell attached, a buffer clear
		// that erases the user's console scrollback (see startDirect). Callers
		// re-apply the effective size freely; only real changes may reach here.
		return nil
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
func (s *directSession) Getsize() (cols, rows uint16, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cols, s.rows, nil
}

func (s *directSession) Wait() error {
	<-s.waitDone
	return s.waitErr
}

// Close tears the session down: pseudo console first (which also detaches —
// and thereby terminates — a still-running shell), then our pipe ends. The
// HPCON close must come first so any Read blocked on outPipe unblocks before
// outPipe.Close waits for it. Idempotent, and safe against the concurrent
// reaper via closePseudoConsole's once-semantics.
func (s *directSession) Close() error {
	s.closeOnce.Do(func() {
		s.closePseudoConsole()
		s.closeErr = s.inPipe.Close()
		if err := s.outPipe.Close(); err != nil && s.closeErr == nil {
			s.closeErr = err
		}
	})
	return s.closeErr
}

// Pid returns the spawned shell's process id.
func (s *directSession) Pid() int {
	return s.pid
}

func (s *directSession) CopyFrom(r io.Reader, done chan<- struct{}) {
	defer close(done)
	_, _ = io.Copy(s.inPipe, r)
}

func (s *directSession) CopyTo(w io.Writer, done chan<- struct{}) {
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
