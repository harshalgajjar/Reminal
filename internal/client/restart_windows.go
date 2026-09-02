// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build windows

package client

// Hot restart on Windows. There is no exec() to swap the process image, so
// restart is ADOPTION rather than replacement: every session's shell lives in
// a separate ConPTY-holder process (see internal/pty/holder_windows.go), and
// the agent is just a client of it. executeRestart therefore:
//
//  1. spawns the freshly-upgraded binary detached, with the session's
//     credentials and the holder's socket path in env (REMINAL_RESUME_*)
//     plus the standard loopback handshake channel,
//  2. waits for the new agent to report it has registered with the relay
//     (its AttachHolder connection supersedes ours at the holder — the shell
//     never blinks),
//  3. exits WITHOUT running defers — the active record now belongs to the
//     successor (which rewrote it under its own pid), so the usual
//     ClearActive-on-exit must not fire.
//
// The PID changes across a Windows restart (unlike Unix, where exec keeps
// it); everything that consumes the pid — active record, control socket —
// is re-derived by the successor at startup.
//
// Foreground sessions restart by CONVERSION: the successor is spawned
// headless (same as any background restart), and the old process — which owns
// the launching terminal's console and must not exit (the invoking shell
// would reclaim the prompt and fight anything else reading the console) —
// turns itself into an attached viewer of its own session. One console owner
// throughout, the user keeps typing into the same shell, and any FURTHER
// restart is then a plain headless restart with the viewer riding through the
// relay's reconnect supersede.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"time"

	"github.com/reminal/reminal/internal/pty"
	xterm "golang.org/x/term"
)

// Env keys shared with the Unix restart (same names, so tooling that knows
// one knows both), plus the Windows-only holder socket.
const (
	envResume          = "REMINAL_RESUME"
	envResumeSessionID = "REMINAL_RESUME_SESSION_ID"
	envResumePIN       = "REMINAL_RESUME_PIN"
	envResumePinHash   = "REMINAL_RESUME_PIN_HASH"
	envResumeToken     = "REMINAL_RESUME_TOKEN"
	envResumeStartedAt = "REMINAL_RESUME_STARTED_AT"
	// envResumePTYSock carries the ConPTY holder's socket path — the Windows
	// analogue of Unix's inherited PTY fd.
	envResumePTYSock = "REMINAL_RESUME_PTY_SOCK"
	// envResumeName carries the user-set session label; the predecessor's
	// on-disk record can't be read back for it (its pid is dead by then).
	envResumeName = "REMINAL_RESUME_NAME"
	// envResumeHeadless preserves the session's headless mode across the swap.
	envResumeHeadless = "REMINAL_RESUME_HEADLESS"
)

// LoadResumeState reconstructs a hot-restarted session in the successor
// process: reconnect to the holder, rebuild identity from env. (nil, nil)
// means fresh startup.
func LoadResumeState() (*ResumeState, error) {
	if os.Getenv(envResume) != "1" {
		return nil, nil
	}
	// Consume first so a later validation / attach error cannot leave plaintext.
	dump := takeScrollbackDump(os.Getenv(envResumeScrollback))
	defer scrubWindowsResumeEnv()

	id := os.Getenv(envResumeSessionID)
	pin := os.Getenv(envResumePIN)
	pinHash := os.Getenv(envResumePinHash)
	token := os.Getenv(envResumeToken)
	sock := os.Getenv(envResumePTYSock)
	name := os.Getenv(envResumeName)
	if id == "" || pin == "" || pinHash == "" || sock == "" {
		return nil, errors.New("resume requested but session id / pin / pin_hash / pty sock missing")
	}
	startedAtUnix, _ := strconv.ParseInt(os.Getenv(envResumeStartedAt), 10, 64)
	startedAt := time.Unix(startedAtUnix, 0)
	if startedAtUnix == 0 {
		startedAt = time.Now()
	}

	sess, err := pty.AttachHolder(sock)
	if err != nil {
		return nil, fmt.Errorf("resume: reattach pty holder: %w", err)
	}

	return &ResumeState{
		SessionID: id,
		PIN:       pin,
		PinHash:   pinHash,
		Token:     token,
		StartedAt: startedAt,
		PTY:       sess,
		Name:      name,
		Headless:  os.Getenv(envResumeHeadless) == "1",
		Dump:      dump,
		// The old agent set up a loopback handshake (prepareHandshake put the
		// address on our argv); report registration back so it knows it may
		// exit. Empty when nothing was passed — then nobody is waiting.
		HandshakeAddr: ParseHandshakeAddr(os.Args),
	}, nil
}

func scrubWindowsResumeEnv() {
	for _, k := range []string{envResume, envResumeSessionID, envResumePIN,
		envResumePinHash, envResumeToken, envResumeStartedAt, envResumePTYSock,
		envResumeName, envResumeHeadless, envResumeScrollback} {
		_ = os.Unsetenv(k)
	}
}

// boolEnv renders a bool for the resume env.
func boolEnv(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// executeRestart hands the session to a fresh process of the (presumably
// upgraded) on-disk binary. On success it never returns. Returns an error
// only when the handoff couldn't be completed — the current agent is then
// still fully in charge.
func (a *Agent) executeRestart() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate self for restart: %w", err)
	}
	if a.term == nil {
		return errors.New("session is still starting — retry in a moment")
	}
	sock := a.term.SockPath()
	if sock == "" {
		return errors.New("session has no pty holder — cannot hand off")
	}

	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer devnull.Close()

	// From here on the successor may connect to the holder at any moment,
	// which supersedes our PTY connection and makes Run() wind down through
	// its shell-exit path concurrently. Flag the handoff FIRST so no exit
	// path of ours clears the record the successor is about to own. The CAS
	// also serialises restarts: a second `reminal restart` while one is in
	// flight would spawn a second successor to fight the first over the
	// holder.
	if !a.restarting.CompareAndSwap(false, true) {
		return errors.New("a restart is already in flight")
	}

	a.metaMu.Lock()
	name := a.name
	a.metaMu.Unlock()

	cmd := exec.Command(exe)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = devnull, devnull, devnull
	// Diagnosability: the successor's death is otherwise silent (stdio is
	// devnull). With REMINAL_RESTART_DEBUG=1 its stderr lands in a file.
	if os.Getenv("REMINAL_RESTART_DEBUG") == "1" {
		if home, herr := os.UserHomeDir(); herr == nil {
			if f, ferr := os.OpenFile(home+"/.reminal/restart-debug.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); ferr == nil {
				cmd.Stdout, cmd.Stderr = f, f
				defer f.Close() // our copy; the successor holds its own handle
			}
		}
	}
	cmd.Env = append(os.Environ(),
		envResume+"=1",
		envResumeSessionID+"="+a.sessionID,
		envResumePIN+"="+a.pin,
		envResumePinHash+"="+a.pinHash,
		envResumeToken+"="+a.token,
		envResumeStartedAt+"="+strconv.FormatInt(a.startedAt.Unix(), 10),
		envResumePTYSock+"="+sock,
		envResumeName+"="+name,
		// A converting foreground session comes back HEADLESS — its old
		// process becomes a viewer, not a host terminal.
		envResumeHeadless+"="+boolEnv(a.headless || a.localActive),
	)
	dumpPath := a.dumpScrollbackForRestart()
	if dumpPath != "" {
		cmd.Env = append(cmd.Env, envResumeScrollback+"="+dumpPath)
	}
	// prepareHandshake appends --handshake-addr to argv (ignored by the
	// resume path except for ParseHandshakeAddr) and sets the detach attrs.
	recv, afterStart, err := prepareHandshake(cmd)
	if err != nil {
		a.restarting.Store(false) // nothing spawned — we remain sole owner
		removeScrollbackDump(dumpPath)
		return err
	}
	if err := cmd.Start(); err != nil {
		afterStart()
		a.restarting.Store(false) // successor never existed — we remain sole owner
		removeScrollbackDump(dumpPath)
		return fmt.Errorf("start successor: %w", err)
	}
	afterStart()
	// Deliberately NOT Release()d: on timeout we must be able to kill it.

	// Wait until the successor is REGISTERED with the relay — not merely
	// started — so a broken new binary can't take the session down: if it
	// never reports, we time out and stay in charge.
	if _, err := recv(20 * time.Second); err != nil {
		// Kill FIRST, check ownership second: a slow-but-alive successor
		// (Defender scanning a fresh binary can stall a launch >20s) must
		// not adopt the holder AFTER we've reclaimed — that's a split brain
		// where our wind-down deletes its record and strands the session.
		_ = cmd.Process.Kill()
		go func() { _, _ = cmd.Process.Wait() }() // reap; the handle isn't needed again
		// Brief settle: if the successor adopted at the very edge of the
		// timeout, our read pump needs a beat to observe the superseded
		// connection — a false "still alive" here would reclaim a session
		// we no longer own and delete the successor's record on wind-down.
		time.Sleep(150 * time.Millisecond)
		if !a.term.Dead() {
			// The successor never adopted the holder — we still own the
			// shell, so reclaim sole ownership and keep serving.
			a.restarting.Store(false)
			removeScrollbackDump(dumpPath)
			return fmt.Errorf("successor didn't come up (still serving on the old binary): %w", err)
		}
		// Successor may already have take()n the dump; removing a missing
		// file is fine. If they died before LoadResumeState, this clears it.
		removeScrollbackDump(dumpPath)
		// The successor adopted the holder and THEN failed to register: our
		// PTY connection is superseded, so nobody can serve the shell — the
		// holder's grace timer ends the session. The flag stays SET so we
		// don't race a half-born successor for the record (liveness pruning
		// cleans it), and a FOREGROUND process must not park forever on the
		// linger defer: restore the console and exit here.
		if a.localActive {
			if a.hostOldState != nil {
				_ = xterm.Restore(int(os.Stdin.Fd()), a.hostOldState)
			}
			clearHostIndicator()
			fmt.Fprintf(os.Stderr, "\nrestart failed after handoff began — the session is winding down: %v\n", err)
			os.Exit(1)
		}
		return fmt.Errorf("successor adopted the session but never registered — session is winding down: %w", err)
	}
	_ = cmd.Process.Release()

	// Successor owns the session now: it holds the pty socket, has rewritten
	// the active record under its pid, and serves the relay. Tear down the
	// pieces the successor re-creates, then leave WITHOUT running Run's
	// defers — our deferred ClearActive would delete the record the
	// successor just wrote.
	a.stopControlListener()

	if a.localActive {
		a.becomeViewer()
		// unreachable — becomeViewer always ends in os.Exit
	}
	os.Exit(0)
	return nil // unreachable
}

// becomeViewer is the tail of a foreground conversion: reclaim the console
// from the (superseded) host machinery and attach to our own — now headless —
// session as an ordinary viewer. Never returns.
func (a *Agent) becomeViewer() {
	// Terminal phase: from here the console belongs to the viewer. Set BEFORE
	// the nudge so the woken pumpHostStdin read observes it and exits (it
	// merely discards chunks during the earlier, still-revocable phase).
	a.converting.Store(true)
	// The conversion outlives Run()'s signal handling (its Notify was already
	// Stopped); between the cooked-mode restore below and the viewer's own
	// raw mode, a stray Ctrl-C would kill the process mid-conversion.
	signal.Ignore(os.Interrupt)
	// Unblock pumpHostStdin's pending console read, give it a beat to
	// consume the nudge and exit, then flush the console input queue so the
	// synthetic ENTER (or any doomed mid-conversion keystroke) can't leak
	// through the viewer to the shell as a stray keypress.
	nudgeConsoleStdin()
	time.Sleep(150 * time.Millisecond)
	flushConsoleStdin()

	// The viewer expects a cooked terminal to start from (it does its own
	// MakeRaw); Run's restore defers were suppressed by the restarting flag,
	// so restore here, exactly once, on the conversion path.
	if a.hostOldState != nil {
		_ = xterm.Restore(int(os.Stdin.Fd()), a.hostOldState)
	}
	clearHostIndicator()
	fmt.Printf("\r\n  [%s] Hot-restarted onto the new binary. The session now runs in the background;\r\n  this terminal is attached as a viewer — everything works as before. Ctrl-] detaches (session keeps running).\r\n\r\n",
		time.Now().Format("15:04:05"))

	v, err := NewViewer(a.sessionID, a.pin)
	if err == nil {
		err = v.Run()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "viewer: %v\nreattach with: reminal attach %s\n", err, a.sessionID)
		os.Exit(1)
	}
	os.Exit(0)
}
