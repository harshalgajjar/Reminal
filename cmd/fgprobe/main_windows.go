// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build windows

// fgprobe drives a FOREGROUND reminal agent under a ConPTY to exercise the
// foreground hot-restart conversion. Throwaway diagnostic.
package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"reminal/internal/pty"
)

func main() {
	if len(os.Args) > 3 && os.Args[1] == "__ptyhold" {
		_ = pty.RunHolder(os.Args[2], os.Args[3], pty.HolderOpts{}) // zero = measure our own console
		return
	}
	log, _ := os.Create(os.Getenv("USERPROFILE") + `\reminal-test\fgprobe.log`)
	defer log.Close()
	lf := func(format string, a ...any) {
		fmt.Fprintf(log, time.Now().Format("15:04:05.000 ")+format+"\n", a...)
		_ = log.Sync()
	}
	exe := os.Getenv("LOCALAPPDATA") + `\Programs\reminal\reminal.exe`
	s, err := pty.Start(exe)
	if err != nil {
		lf("start: %v", err)
		os.Exit(1)
	}
	_ = s.Resize(120, 40)
	var out strings.Builder
	go func() {
		buf := make([]byte, 8192)
		for {
			n, err := s.Read(buf)
			if n > 0 {
				out.WriteString(string(buf[:n]))
			}
			if err != nil {
				lf("pump end: %v", err)
				return
			}
		}
	}()
	waitFor := func(marker string, timeout time.Duration) bool {
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			if strings.Contains(out.String(), marker) {
				return true
			}
			time.Sleep(200 * time.Millisecond)
		}
		return false
	}
	if !waitFor("HOST:", 20*time.Second) {
		lf("FAIL: no banner; tail: %q", tail(out.String()))
		os.Exit(1)
	}
	time.Sleep(2 * time.Second)
	_, _ = s.Write([]byte("$env:FGMARK='fg2'\r"))
	time.Sleep(1500 * time.Millisecond)
	_, _ = s.Write([]byte(exe + " restart\r"))
	if !waitFor("attached as a viewer", 30*time.Second) {
		lf("FAIL: no conversion notice; tail: %q", tail(out.String()))
		os.Exit(1)
	}
	time.Sleep(3 * time.Second)
	_, _ = s.Write([]byte("echo done-$env:FGMARK\r"))
	if !waitFor("done-fg2", 20*time.Second) {
		lf("FAIL: no echo post-conversion; tail: %q", tail(out.String()))
		os.Exit(1)
	}
	lf("PASS foreground conversion")
	os.Exit(0)
}

func tail(s string) string {
	if len(s) > 1200 {
		return s[len(s)-1200:]
	}
	return s
}
