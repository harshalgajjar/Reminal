// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !windows

package client

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// prepareHandshake wires a not-yet-started detached child so it can report one
// JSON line of credentials back to this parent, and detaches it from our
// session so it survives this terminal closing.
//
// Unix mechanism: an inherited pipe on fd 3 (ExtraFiles) + Setsid. The child
// is told where to write via `--handshake-fd 3` appended to its argv.
//
// Returns:
//   - recv: blocks for the child's line (or the timeout). Call after Start.
//   - afterStart: MUST be called immediately after cmd.Start() (success or
//     failure) — it closes the parent's copy of the write end so recv EOFs
//     if the child dies before reporting, instead of hanging to the timeout.
func prepareHandshake(cmd *exec.Cmd) (recv func(timeout time.Duration) (string, error), afterStart func(), err error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, nil, fmt.Errorf("create handshake pipe: %w", err)
	}
	cmd.Args = append(cmd.Args, "--handshake-fd", "3")
	cmd.ExtraFiles = []*os.File{w}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	recv = func(timeout time.Duration) (string, error) {
		defer r.Close()
		type result struct {
			line string
			err  error
		}
		done := make(chan result, 1)
		go func() {
			line, err := bufio.NewReader(r).ReadString('\n')
			done <- result{line, err}
		}()
		select {
		case res := <-done:
			if res.err != nil {
				return "", fmt.Errorf("read handshake: %w", res.err)
			}
			return res.line, nil
		case <-time.After(timeout):
			return "", errors.New("detached reminal didn't report ready within " + timeout.String())
		}
	}
	afterStart = func() { _ = w.Close() }
	return recv, afterStart, nil
}
