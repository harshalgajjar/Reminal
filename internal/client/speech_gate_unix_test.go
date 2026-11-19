//go:build !windows

package client

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"reminal/internal/pty"
)

// The agent has exited and the probe's last look still says it is in front:
// the gate asks the terminal itself, finds the shell, and types nothing. With
// a program named as an agent in front, it does.
func TestSpeechGateAsksTheTerminalNotTheLastLook(t *testing.T) {
	term, err := pty.Start("/bin/sh")
	if err != nil {
		t.Fatal(err)
	}
	read := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, term); close(read) }()
	// Everything it started ends with the test: the terminal hung up (the
	// copy of sleep holds it open otherwise), closed, its shell waited for,
	// and the reader done.
	t.Cleanup(func() {
		term.Hangup()
		_ = term.Close()
		done := make(chan struct{})
		go func() { _ = term.Wait(); close(done) }()
		for _, c := range []chan struct{}{done, read} {
			select {
			case <-c:
			case <-time.After(5 * time.Second):
				t.Error("the test terminal did not end")
			}
		}
	})

	a := gateAgent(t, "claude", "done", "> \r\n") // what the probe last saw
	a.term = term
	end := time.Now().Add(5 * time.Second)
	for a.speechBlocked() != speechNoAgent {
		if time.Now().After(end) {
			t.Fatalf("shell in front, cached agent: speechBlocked = %q, want %q", a.speechBlocked(), speechNoAgent)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// A program named as an agent (a copy of sleep called claude) in front.
	agent := filepath.Join(t.TempDir(), "claude")
	sleep, err := os.ReadFile("/bin/sleep")
	if err != nil {
		t.Skip("no /bin/sleep:", err)
	}
	if err := os.WriteFile(agent, sleep, 0o755); err != nil {
		t.Fatal(err)
	}
	_, _ = term.Write([]byte(agent + " 30\n"))
	end = time.Now().Add(5 * time.Second)
	for a.speechBlocked() != "" {
		if time.Now().After(end) {
			t.Fatalf("agent in front: speechBlocked = %q, want it allowed", a.speechBlocked())
		}
		time.Sleep(50 * time.Millisecond)
	}
}
