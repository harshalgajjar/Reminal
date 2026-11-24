// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !windows

package pty

import (
	"testing"
)

// How a shell ended is told as the system tells it: its exit status when it
// exited, the signal when one ended it. Asked more than once, and with a
// wait of the caller's own, it is the same answer.
func TestHowTheShellEndedIsTold(t *testing.T) {
	drain := func(s *Session) {
		b := make([]byte, 256)
		for {
			if _, err := s.Read(b); err != nil {
				return
			}
		}
	}
	s, err := Start("/bin/sh")
	if err != nil {
		t.Fatal(err)
	}
	go drain(s)
	if _, err := s.Write([]byte("exit 3\n")); err != nil {
		t.Fatal(err)
	}
	if got := s.Ended(); got != "exit status 3" {
		t.Errorf("a shell that exited 3: %q", got)
	}
	if s.EndedBySignal() {
		t.Error("a shell that exited is said to be ended by a signal")
	}
	if err := s.Wait(); err == nil || s.Ended() != "exit status 3" {
		t.Errorf("asked again: %v, %q", err, s.Ended())
	}
	_ = s.Close()

	s, err = Start("/bin/sh")
	if err != nil {
		t.Fatal(err)
	}
	go drain(s)
	if err := s.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if got := s.Ended(); got != "signal: killed" || !s.EndedBySignal() {
		t.Errorf("a shell that was killed: %q, by a signal %v", got, s.EndedBySignal())
	}
	_ = s.Close()

	// A PTY taken over: its shell is not this process's to wait for.
	if got := (&Session{}).Ended(); got != "" {
		t.Errorf("a shell that cannot be waited for: %q", got)
	}
}
