// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !windows

package pty

import (
	"io"
	"testing"
	"time"
)

// A terminal reading a password (lines, echo off) hides what is typed; the
// same shell reading a command line does not.
func TestHidesInputOnlyWhileEchoIsOff(t *testing.T) {
	s, err := Start("/bin/sh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	go func() { _, _ = io.Copy(io.Discard, s) }()
	waitFor := func(want bool) {
		t.Helper()
		end := time.Now().Add(5 * time.Second)
		for s.HidesInput() != want {
			if time.Now().After(end) {
				t.Fatalf("HidesInput stayed %v, want %v", !want, want)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	_, _ = s.Write([]byte("stty echo icanon\n"))
	waitFor(false)
	_, _ = s.Write([]byte("stty -echo\n"))
	waitFor(true)
	_, _ = s.Write([]byte("stty echo\n"))
	waitFor(false)
}
