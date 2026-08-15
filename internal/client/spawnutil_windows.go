// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build windows

package client

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// detachedProcess is CreateProcess's DETACHED_PROCESS flag: the child gets no
// console, so a spawned background agent can't flash a window or die with the
// parent's console. Not exposed by package syscall, hence the literal.
const detachedProcess = 0x00000008

// hsTokenEnv carries the one-shot secret that authenticates the child on the
// loopback handshake socket (see below). Never logged.
const hsTokenEnv = "REMINAL_HS_TOKEN"

// prepareHandshake — Windows twin of the Unix fd-3 pipe. exec.Cmd.ExtraFiles
// doesn't exist on Windows, so the child instead dials back over a one-shot
// loopback TCP listener: we pass `--handshake-addr 127.0.0.1:<port>` on its
// argv and a random token in its environment; the child connects, presents the
// token, then writes its one JSON line. The token stops another local process
// from racing us to the port with forged credentials.
//
// Detach: DETACHED_PROCESS (no console → survives this console closing, no
// window flash) + CREATE_NEW_PROCESS_GROUP (a Ctrl-C to the parent's group
// can't reach it) — together the moral equivalent of Setsid.
func prepareHandshake(cmd *exec.Cmd) (recv func(timeout time.Duration) (string, error), afterStart func(), err error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, fmt.Errorf("create handshake listener: %w", err)
	}
	var tok [16]byte
	if _, err := rand.Read(tok[:]); err != nil {
		_ = ln.Close()
		return nil, nil, err
	}
	token := hex.EncodeToString(tok[:])

	cmd.Args = append(cmd.Args, "--handshake-addr", ln.Addr().String())
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	cmd.Env = append(cmd.Env, hsTokenEnv+"="+token)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | detachedProcess,
	}

	recv = func(timeout time.Duration) (string, error) {
		defer ln.Close()
		deadline := time.Now().Add(timeout)
		// Keep accepting until the deadline: a wrong-token connection (port
		// scanner, unrelated local process) must not eat the child's one shot.
		for {
			if tl, ok := ln.(*net.TCPListener); ok {
				_ = tl.SetDeadline(deadline)
			}
			conn, err := ln.Accept()
			if err != nil {
				return "", errors.New("detached reminal didn't report ready within " + timeout.String())
			}
			line, err := func() (string, error) {
				defer conn.Close()
				_ = conn.SetDeadline(deadline)
				br := bufio.NewReader(conn)
				pre, err := br.ReadString('\n')
				if err != nil || strings.TrimSpace(pre) != "tok "+token {
					return "", errors.New("bad handshake preamble")
				}
				return br.ReadString('\n')
			}()
			if err == nil {
				return line, nil
			}
			if time.Now().After(deadline) {
				return "", errors.New("detached reminal didn't report ready within " + timeout.String())
			}
		}
	}
	afterStart = func() {} // nothing to release — the listener closes in recv
	return recv, afterStart, nil
}
