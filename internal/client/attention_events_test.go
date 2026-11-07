// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"bufio"
	"bytes"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"reminal/internal/session"
)

// The attention detector reads the screen when something may have changed it,
// not on a clock: a session nobody touches is not read at all.

func attnTestAgent(t *testing.T) (*Agent, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	a := &Agent{sessionID: "ATTNEVT1"}
	a.screen = testScreen(t, 80, 24)
	log := filepath.Join(t.TempDir(), "probe.jsonl")
	a.attnStop = make(chan struct{})
	go a.runAttention(log, a.attnStop)
	t.Cleanup(a.stopAttention)
	return a, log
}

func attnReads(t *testing.T, log string) int {
	t.Helper()
	b, _ := os.ReadFile(log)
	return bytes.Count(b, []byte("\n"))
}

// waitReads waits for the log to show more than n reads, up to the test's
// own abort cap.
func waitReads(t *testing.T, log string, n int) {
	t.Helper()
	end := time.Now().Add(5 * time.Second)
	for attnReads(t, log) <= n {
		if time.Now().After(end) {
			t.Fatalf("no read after the kick (still %d)", n)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestAttentionAnUntouchedSessionIsNotRead(t *testing.T) {
	_, log := attnTestAgent(t)
	// The window is the assertion: nothing happens in it. A 300 ms clock
	// reads ten times; the detector reads at its start and once more when
	// that first screen comes due as settled.
	time.Sleep(3 * time.Second)
	if n := attnReads(t, log); n > 2 {
		t.Fatalf("read %d times in 3 s with nothing happening; want at most 2", n)
	}
}

func TestAttentionOutputIsReadAtOnce(t *testing.T) {
	a, log := attnTestAgent(t)
	waitReads(t, log, 0)
	time.Sleep(2 * time.Second) // past the settle read
	n := attnReads(t, log)
	a.screenMu.Lock()
	_, _ = a.screen.Write([]byte("hello\r\n"))
	a.screenMu.Unlock()
	a.markActivity(time.Now())
	waitReads(t, log, n)
}

func TestAttentionOutputIsReadAtMostEveryInterval(t *testing.T) {
	a, log := attnTestAgent(t)
	waitReads(t, log, 0)
	start, n0 := time.Now(), attnReads(t, log)
	for time.Since(start) < 1500*time.Millisecond {
		a.screenMu.Lock()
		_, _ = a.screen.Write([]byte("x"))
		a.screenMu.Unlock()
		a.markActivity(time.Now())
		time.Sleep(5 * time.Millisecond)
	}
	// 1.5 s of output a chunk every 5 ms: read at attnInterval's pace (≈5),
	// not per chunk (≈300).
	if n := attnReads(t, log) - n0; n > 8 {
		t.Fatalf("%d reads under 1.5 s of output; want about 5", n)
	}
}

func TestAttentionTheHookVerbReadsAtOnce(t *testing.T) {
	a, log := attnTestAgent(t)
	waitReads(t, log, 0)
	time.Sleep(2 * time.Second)
	n := attnReads(t, log)
	c1, c2 := net.Pipe()
	go a.handleControlConn(c2)
	_, _ = c1.Write([]byte("attn\n"))
	if reply, _ := bufio.NewReader(c1).ReadString('\n'); reply != "ok\n" {
		t.Fatalf("attn replied %q", reply)
	}
	c1.Close()
	waitReads(t, log, n)
}

func TestAttnNextRead(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	settle := time.Duration(attnSettleMs) * time.Millisecond
	ms := time.Millisecond
	cases := []struct {
		name               string
		lastChange, output time.Time
		hs                 *session.HookState
		blind              bool
		want               time.Time // zero: none
	}{
		{"all settled long ago", now.Add(-time.Hour), now.Add(-time.Hour), nil, false, time.Time{}},
		{"a screen settling", now.Add(-200 * ms), now.Add(-time.Hour), nil, false, now.Add(-200*ms + settle + ms)},
		{"output that may start a program", now.Add(-time.Hour), now.Add(-100 * ms), nil, false, now.Add(-100*ms + settle + ms)},
		{"a resting hook adds nothing", now.Add(-time.Hour), now.Add(-time.Hour), &session.HookState{State: "done", TS: now.Add(-time.Minute)}, false, time.Time{}},
		{"a working hook on a still screen", now.Add(-time.Hour), now.Add(-10 * time.Second), &session.HookState{State: "working", TS: now.Add(-time.Minute)},
			false, now.Add(-10*time.Second + hookWorkingSilentMs*ms + ms)},
		{"a working hook near its TTL", now.Add(-time.Hour), time.Time{}, &session.HookState{State: "working", TS: now.Add(-session.HookStateTTL + time.Second)},
			false, now.Add(time.Second + ms)},
		{"a console that cannot be asked", now.Add(-time.Hour), now.Add(-time.Hour), nil, true, now.Add(attnInterval + ms)},
	}
	for _, c := range cases {
		got, ok := attnNextRead(now, c.lastChange, c.output, c.hs, c.blind)
		if ok != !c.want.IsZero() || !got.Equal(c.want) {
			t.Errorf("%s: got %v (%v), want %v", c.name, got, ok, c.want)
		}
	}
}

// stopAttention returns only once the detector has: a read under way when
// the session goes must be over before the PTY is closed under it.
func TestStopAttentionWaitsForTheReadUnderWay(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	inRead, release := make(chan struct{}), make(chan struct{})
	attnReadHook = func() {
		select {
		case inRead <- struct{}{}:
			<-release
		default:
		}
	}
	t.Cleanup(func() { attnReadHook = nil })
	a := &Agent{sessionID: "ATTNSTOP"}
	a.screen = testScreen(t, 80, 24)
	a.startAttention()
	<-inRead // the detector is in a read
	stopped := make(chan struct{})
	go func() { a.stopAttention(); close(stopped) }()
	select {
	case <-stopped:
		t.Fatal("stopAttention returned while the detector was still reading")
	case <-time.After(300 * time.Millisecond): // the window is the assertion
	}
	close(release)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second): // the test's own abort cap
		t.Fatal("stopAttention never returned")
	}
}

// After a burst of output, once the screen has settled, memory is handed
// back to the OS once — not on every read, and not for a trickle.
func TestMemoryIsReleasedOnceAfterABurst(t *testing.T) {
	freed := make(chan struct{}, 8)
	was := freeOSMemory
	freeOSMemory = func() { freed <- struct{}{} }
	t.Cleanup(func() { freeOSMemory = was })
	a, log := attnTestAgent(t)
	waitReads(t, log, 0)
	count := func() int {
		n := 0
		for {
			select {
			case <-freed:
				n++
			case <-time.After(300 * time.Millisecond): // the window is the assertion
				return n
			}
		}
	}
	// A trickle: nothing.
	a.outSinceFree.Store(1 << 10)
	a.markActivity(time.Now().Add(-5 * time.Second))
	if n := count(); n != 0 {
		t.Fatalf("released after %d bytes", 1<<10)
	}
	// A burst, then quiet: once.
	a.outSinceFree.Store(64 << 20)
	n := attnReads(t, log)
	a.markActivity(time.Now().Add(-5 * time.Second))
	waitReads(t, log, n)
	if n := count(); n != 1 {
		t.Fatalf("released %d times after a burst, want 1", n)
	}
	n = attnReads(t, log)
	a.kickAttention()
	waitReads(t, log, n)
	if n := count(); n != 0 {
		t.Fatalf("released again with no new output (%d)", n)
	}
}
