//go:build !windows

package client

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
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
	var outMu sync.Mutex
	var out strings.Builder
	go func() {
		b := make([]byte, 4096)
		for {
			n, err := term.Read(b)
			outMu.Lock()
			out.Write(b[:n])
			outMu.Unlock()
			if err != nil {
				close(read)
				return
			}
		}
	}()
	// What it printed (not the typed line the terminal echoes back).
	said := func(re *regexp.Regexp) bool { outMu.Lock(); defer outMu.Unlock(); return re.MatchString(out.String()) }
	// Everything it started ends with the test: the terminal hung up (the
	// stand-in agent holds it open otherwise), closed, its shell waited for,
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

	a := gateAgent(t, "claude", "done", claudeAtBox) // what the probe last saw
	a.term = term
	end := time.Now().Add(5 * time.Second)
	for a.speechBlocked() != speechNoAgent {
		if time.Now().After(end) {
			t.Fatalf("shell in front, cached agent: speechBlocked = %q, want %q", a.speechBlocked(), speechNoAgent)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// A program named as an agent in front: a copy of sh called claude,
	// running a command it cannot hand off to (the ":" after the sleep keeps
	// it from exec'ing sleep in its place), so its process group's leader is
	// named claude for as long as it runs. Its output says it started.
	agent := filepath.Join(t.TempDir(), "claude")
	sh, err := os.ReadFile("/bin/sh")
	if err != nil {
		t.Skip("no /bin/sh:", err)
	}
	if err := os.WriteFile(agent, sh, 0o755); err != nil {
		t.Fatal(err)
	}
	// A login shell on a busy machine may take a while to read its profile
	// before it reads this line: the gate is asked once the agent has said
	// it started.
	_, _ = term.Write([]byte(agent + " -c 'echo agent-start$$ed; sleep 30; :'\n"))
	for end, started := time.Now().Add(60*time.Second), regexp.MustCompile(`agent-start[0-9]+ed`); !said(started); {
		if time.Now().After(end) {
			t.Fatal("the stand-in agent did not start")
		}
		time.Sleep(50 * time.Millisecond)
	}
	end = time.Now().Add(10 * time.Second)
	for a.speechBlocked() != "" {
		if time.Now().After(end) {
			pgrp := term.ForegroundPgrp()
			t.Fatalf("agent in front: speechBlocked = %q, want it allowed (foreground pgrp %d, named %q)", a.speechBlocked(), pgrp, foregroundProgram(pgrp, attentionForegroundName(pgrp)))
		}
		time.Sleep(50 * time.Millisecond)
	}
}
