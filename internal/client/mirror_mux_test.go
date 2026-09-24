// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// These tests exercise captureMux — the daemon-side multiplexer that keeps ONE
// `reminal-capture serve` process for every stream (concurrent helper
// PROCESSES kill each other: replayd keys the capture connection by
// code-signing identity) — against a fake helper speaking the serve protocol,
// so they run without ScreenCaptureKit, permissions, or macOS.
//
// The fake is this test binary re-exec'd (TestFakeCaptureHelperMain) behind a
// tiny shell wrapper, since captureMux wants an executable path.

// fakeHelper writes a wrapper script that re-runs this test binary in the
// given fake mode and returns its path.
func fakeHelper(t *testing.T, mode string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake helper wrapper is a shell script")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "fake-capture")
	script := fmt.Sprintf("#!/bin/sh\nGO_FAKE_CAPTURE_HELPER=%s exec %q -test.run='^TestFakeCaptureHelperMain$' -- \"$@\"\n", mode, exe)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestFakeCaptureHelperMain is not a test: it is the fake helper's entry point
// when this binary is re-exec'd by fakeHelper's wrapper. Modes: "usage" acts
// like a pre-serve binary (usage + exit 2); "serve" speaks the serve protocol
// — frames tagged with their sid so the test can prove no cross-stream mixups,
// target "errwin" fails in-band, a "key" command emits a "KEY<sid>" frame.
func TestFakeCaptureHelperMain(t *testing.T) {
	mode := os.Getenv("GO_FAKE_CAPTURE_HELPER")
	if mode == "" {
		t.Skip("fake helper entry point — only meaningful re-exec'd")
	}
	if mode == "usage" {
		fmt.Fprintln(os.Stderr, "usage: reminal-capture <windowID|display:ID> ...")
		os.Exit(2)
	}
	var outMu sync.Mutex
	send := func(sid uint32, payload []byte) {
		outMu.Lock()
		defer outMu.Unlock()
		var hdr [8]byte
		binary.BigEndian.PutUint32(hdr[0:4], sid)
		binary.BigEndian.PutUint32(hdr[4:8], uint32(len(payload)))
		_, _ = os.Stdout.Write(hdr[:])
		_, _ = os.Stdout.Write(payload)
	}
	frame := func(body string) []byte { // the inner [len][bytes] sessions parse
		b := make([]byte, 4+len(body))
		binary.BigEndian.PutUint32(b[0:4], uint32(len(body)))
		copy(b[4:], body)
		return b
	}
	send(0, []byte("READY")) // the hello the daemon probes on
	stops := make(map[string]chan struct{})
	var mu sync.Mutex
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 0 {
			continue
		}
		switch f[0] {
		case "start":
			sid, target := f[1], f[2]
			var id uint32
			fmt.Sscan(sid, &id)
			if target == "errwin" {
				send(id, errFrameBytes("window errwin not found"))
				send(id, nil) // end marker
				continue
			}
			stop := make(chan struct{})
			mu.Lock()
			stops[sid] = stop
			mu.Unlock()
			go func() {
				tick := time.NewTicker(5 * time.Millisecond)
				defer tick.Stop()
				for {
					select {
					case <-stop:
						return
					case <-tick.C:
						send(id, frame("FRAME"+sid))
					}
				}
			}()
		case "stop":
			mu.Lock()
			if c, ok := stops[f[1]]; ok {
				close(c)
				delete(stops, f[1])
			}
			mu.Unlock()
		case "key":
			var id uint32
			fmt.Sscan(f[1], &id)
			send(id, frame("KEY"+f[1]))
		}
	}
	os.Exit(0) // stdin EOF — daemon gone
}

// readFrames collects inner frames off a session conn until it closes,
// reporting bodies and any out-of-band error message.
func readFrames(conn net.Conn) (bodies []string, errMsg string) {
	r := bufio.NewReader(conn)
	var lenBuf [4]byte
	for {
		if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
			return
		}
		n := binary.BigEndian.Uint32(lenBuf[:])
		if n == winErrFrameMagic {
			if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
				return
			}
			msg := make([]byte, binary.BigEndian.Uint32(lenBuf[:]))
			if _, err := io.ReadFull(r, msg); err != nil {
				return
			}
			errMsg = string(msg)
			continue
		}
		body := make([]byte, n)
		if _, err := io.ReadFull(r, body); err != nil {
			return
		}
		bodies = append(bodies, string(body))
	}
}

// Two sessions must stream at the same time through the one helper, each
// getting only its own frames — the impossibility that motivated the mux.
func TestCaptureMuxConcurrentStreams(t *testing.T) {
	helper := fakeHelper(t, "serve")
	m := &captureMux{}

	type result struct {
		bodies []string
		errMsg string
	}
	openStream := func(id string) (chan result, func()) {
		client, server := net.Pipe()
		done := make(chan result, 1)
		go m.serve(server, helper, id, "400", "45", "5", "")
		go func() {
			bodies, errMsg := readFrames(client)
			done <- result{bodies, errMsg}
		}()
		return done, func() { _ = client.Close() }
	}

	doneA, closeA := openStream("111")
	doneB, closeB := openStream("222")
	time.Sleep(700 * time.Millisecond) // both must be live simultaneously
	closeA()
	closeB()

	// Every frame body carries its sid ("FRAME<sid>"). Each stream must have
	// received plenty of frames, all tagged with ONE sid — and a different one
	// per stream — proving both ran live at once with no cross-stream mixing.
	distinct := map[string]string{}
	for name, done := range map[string]chan result{"A": doneA, "B": doneB} {
		select {
		case res := <-done:
			if len(res.bodies) < 2 {
				t.Fatalf("stream %s got %d frames, want a live stream", name, len(res.bodies))
			}
			for _, b := range res.bodies {
				if !strings.HasPrefix(b, "FRAME") {
					t.Fatalf("stream %s got unexpected body %q", name, b)
				}
				if prev, ok := distinct[name]; ok && prev != b {
					t.Fatalf("stream %s saw mixed sids: %q and %q", name, prev, b)
				}
				distinct[name] = b
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("stream %s never finished", name)
		}
	}
	if distinct["A"] == distinct["B"] {
		t.Fatalf("both streams tagged %q — frames crossed", distinct["A"])
	}
}

// A stream that fails helper-side must deliver the reason in-band and close.
func TestCaptureMuxErrorPropagates(t *testing.T) {
	helper := fakeHelper(t, "serve")
	m := &captureMux{}
	client, server := net.Pipe()
	go m.serve(server, helper, "errwin", "400", "45", "5", "")
	type result struct {
		bodies []string
		errMsg string
	}
	done := make(chan result, 1)
	go func() {
		bodies, errMsg := readFrames(client)
		done <- result{bodies, errMsg}
	}()
	select {
	case res := <-done:
		if res.errMsg != "window errwin not found" {
			t.Fatalf("error frame = %q, want the helper's reason", res.errMsg)
		}
		if len(res.bodies) != 0 {
			t.Fatalf("failed stream delivered %d frames", len(res.bodies))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("error stream never ended")
	}
}

// "key" from the session must reach the helper tagged with the right sid.
func TestCaptureMuxForwardsKey(t *testing.T) {
	helper := fakeHelper(t, "serve")
	m := &captureMux{}
	client, server := net.Pipe()
	go m.serve(server, helper, "111", "400", "45", "5", "h264")
	got := make(chan struct{})
	go func() {
		r := bufio.NewReader(client)
		var lenBuf [4]byte
		for {
			if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
				return
			}
			body := make([]byte, binary.BigEndian.Uint32(lenBuf[:]))
			if _, err := io.ReadFull(r, body); err != nil {
				return
			}
			if strings.HasPrefix(string(body), "KEY") {
				close(got)
				return
			}
		}
	}()
	time.Sleep(400 * time.Millisecond) // let the stream come up
	if _, err := client.Write([]byte("key\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("key command never reached the helper")
	}
	_ = client.Close()
}

// A helper binary that predates serve mode must latch m.unsupported — via the
// startup probe when the exit is quick, or the demux exit-code-2 backstop when
// it isn't (this fake is a re-exec'd test binary, slow enough to hit either) —
// and every LATER capture must decline cleanly, nothing written to its conn,
// so the caller spawns the helper per capture instead.
func TestCaptureMuxFallsBackOnPreServeHelper(t *testing.T) {
	helper := fakeHelper(t, "usage")
	m := &captureMux{}

	// First attempt: either declined outright (probe caught the exit) or
	// claimed and then failed in-band (backstop). Both must end promptly and
	// leave the verdict latched.
	client, server := net.Pipe()
	got := make(chan bool, 1)
	go func() { got <- m.serve(server, helper, "111", "400", "45", "5", "") }()
	go func() { _, _ = io.Copy(io.Discard, client); _ = client.Close() }()
	select {
	case <-got: // handled either way — what matters is the latch below
	case <-time.After(5 * time.Second):
		t.Fatal("first serve() never returned")
	}
	waitLatch := time.After(5 * time.Second)
	for {
		m.mu.Lock()
		latched := m.unsupported
		m.mu.Unlock()
		if latched {
			break
		}
		select {
		case <-waitLatch:
			t.Fatal("pre-serve helper verdict never latched")
		case <-time.After(20 * time.Millisecond):
		}
	}

	// Second attempt: must decline with NOTHING on the conn.
	client2, server2 := net.Pipe()
	got2 := make(chan bool, 1)
	go func() { got2 <- m.serve(server2, helper, "111", "400", "45", "5", "") }()
	_ = client2.SetReadDeadline(time.Now().Add(1 * time.Second))
	var b [1]byte
	if n, err := client2.Read(b[:]); err == nil {
		t.Fatalf("conn got %d bytes from a declined serve", n)
	}
	select {
	case handled := <-got2:
		if handled {
			t.Fatal("serve() claimed a stream after latching unsupported")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second serve() never declined")
	}
}

// A serve helper leaves once its last stream ends, so that anything it failed
// to tear down inside ScreenCaptureKit dies with it rather than capturing the
// screen for hours with nobody watching. The daemon must take that in its
// stride: the NEXT window someone mirrors gets a fresh helper, not an error
// about the screen-sharing service, and not frames from a process that is
// already gone.
// A capture helper is long-lived on purpose — one process serves every
// session's mirror — but it must not be immortal. ScreenCaptureKit runs inside
// it, and a stream it failed to tear down keeps capturing a window nobody is
// watching, which on a laptop is hours of battery and a screen-recording
// indicator with no explanation. Once the last stream ends the daemon retires
// it, and the next window someone mirrors gets a fresh one.
func TestCaptureMuxRetiresIdleHelperAndStartsAnother(t *testing.T) {
	helper := fakeHelper(t, "serve")
	m := &captureMux{idleAfter: 150 * time.Millisecond}

	first := liveStream(t, m, helper, "111")
	m.mu.Lock()
	running := m.stdin != nil
	m.mu.Unlock()
	if !running {
		t.Fatal("helper not running while a stream is live")
	}
	first.close()

	waitFor(t, 5*time.Second, "helper to be retired", func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.stdin == nil
	})

	// The helper is gone; mirroring a window must still just work.
	second := liveStream(t, m, helper, "222")
	defer second.close()
	for _, b := range second.frames {
		if !strings.HasPrefix(b, "FRAME") || b != second.frames[0] {
			t.Fatalf("second stream got %q (first %q), want one live stream's frames", b, second.frames[0])
		}
	}
}

// Retirement must never touch a helper that is still streaming: a window left
// open on a phone would go black for no reason.
func TestCaptureMuxKeepsHelperWhileStreaming(t *testing.T) {
	helper := fakeHelper(t, "serve")
	m := &captureMux{idleAfter: 100 * time.Millisecond}

	keep := liveStream(t, m, helper, "111")
	defer keep.close()
	// End a SECOND stream, which is what arms retirement — with one still live.
	other := liveStream(t, m, helper, "222")
	other.close()

	time.Sleep(600 * time.Millisecond) // several times the idle grace
	m.mu.Lock()
	retired := m.stdin == nil
	m.mu.Unlock()
	if retired {
		t.Fatal("helper retired while a stream was still live")
	}
	if got := keep.readMore(t, 1); len(got) == 0 {
		t.Fatal("live stream stopped receiving frames")
	}
}

// liveStream opens a capture and waits until it is actually streaming.
type testStream struct {
	client net.Conn
	bodies chan string
	frames []string
}

func liveStream(t *testing.T, m *captureMux, helper, id string) *testStream {
	t.Helper()
	client, server := net.Pipe()
	go m.serve(server, helper, id, "400", "45", "5", "")
	ts := &testStream{client: client, bodies: make(chan string, 256)}
	go func() {
		defer close(ts.bodies)
		r := bufio.NewReader(client)
		var lenBuf [4]byte
		for {
			if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
				return
			}
			body := make([]byte, binary.BigEndian.Uint32(lenBuf[:]))
			if _, err := io.ReadFull(r, body); err != nil {
				return
			}
			ts.bodies <- string(body)
		}
	}()
	ts.frames = ts.readMore(t, 2)
	if len(ts.frames) < 2 {
		t.Fatalf("stream %s never went live", id)
	}
	return ts
}

// readMore waits for n more frames, or fails the test.
func (ts *testStream) readMore(t *testing.T, n int) []string {
	t.Helper()
	var got []string
	deadline := time.After(15 * time.Second)
	for len(got) < n {
		select {
		case b, ok := <-ts.bodies:
			if !ok {
				return got
			}
			got = append(got, b)
		case <-deadline:
			return got
		}
	}
	return got
}

func (ts *testStream) close() { _ = ts.client.Close() }

// waitFor polls until cond holds, or fails the test with what it was waiting on.
func waitFor(t *testing.T, limit time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
