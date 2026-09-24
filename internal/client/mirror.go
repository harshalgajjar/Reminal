// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

// The mirror service (macOS). ALL window/desktop capture and input injection is
// performed by the always-on daemon (code identity sh.reminal), and every session
// — terminal or "+" — delegates to it over this fixed local socket. That way a
// single reminal.app permission grant (Screen Recording, Accessibility,
// Automation) covers every session, instead of terminal sessions being attributed
// to Terminal.app (the responsible process) and prompting for their own grants.
//
// This file is the DAEMON side (the server) plus the small client dial helpers.
// Wiring the session capture/input path to call these is done separately.

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// mirrorSockPath is the fixed (non-PID) socket the daemon serves capture + input
// on, so any local session can find it. ~/.reminal/mirror.sock.
// REMINAL_MIRROR_SOCK overrides it so a development daemon can be run beside
// the installed one instead of seizing its socket — testing a change to this
// service otherwise means every live session's capture and input abruptly
// routes through an unsigned build.
func mirrorSockPath() (string, error) {
	if p := strings.TrimSpace(os.Getenv("REMINAL_MIRROR_SOCK")); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".reminal", "mirror.sock"), nil
}

// ---- daemon side (server) --------------------------------------------------

// serveMirror runs the daemon's capture + input service until stop closes.
// Started by RunDaemon (macOS only).
func serveMirror(stop <-chan struct{}) {
	sock, err := mirrorSockPath()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		return
	}
	_ = os.Remove(sock) // clear a stale socket left by a crash
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return
	}
	_ = os.Chmod(sock, 0o600)
	go func() {
		<-stop
		_ = ln.Close()
		_ = os.Remove(sock)
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-stop:
				return // listener closed on stop — the intended exit
			default:
			}
			// Any OTHER Accept error (e.g. EMFILE under fd pressure) must NOT kill
			// the always-on mirror service — capture/input would then fail with
			// "service restarting" forever until the daemon is restarted. Back off
			// briefly and keep accepting, matching http.Server.Serve's resilience.
			time.Sleep(50 * time.Millisecond)
			continue
		}
		go handleMirrorConn(conn)
	}
}

// handleMirrorConn reads one command line ("<cmd> <rest>") and dispatches.
// `capture` keeps the connection open streaming frames; `input`/`check` reply once.
func handleMirrorConn(conn net.Conn) {
	// Spawned per connection (go handleMirrorConn); a panic here would crash the
	// always-on daemon and take down capture/input for every session. Contain it.
	defer func() {
		if r := recover(); r != nil {
			recoverLog("handleMirrorConn", r)
		}
	}()
	br := bufio.NewReader(conn)
	line, err := br.ReadString('\n')
	if err != nil {
		_ = conn.Close()
		return
	}
	cmd, rest, _ := strings.Cut(strings.TrimRight(line, "\r\n"), " ")
	switch cmd {
	case "capture":
		mirrorServeCapture(conn, strings.Fields(rest)) // owns + closes conn
	case "captureregion":
		mirrorServeCaptureRegion(conn, strings.Fields(rest))
		_ = conn.Close()
	case "input":
		mirrorServeInput(conn, rest)
		_ = conn.Close()
	case "release":
		_ = darwinWindows{}.releaseInput() // unstick any held mouse button
		fmt.Fprintln(conn, "ok")
		_ = conn.Close()
	case "check":
		writeHelperCheck(conn, "check") // Screen Recording preflight
	case "axcheck":
		writeHelperCheck(conn, "ax-check") // Accessibility preflight
	case "autocheck":
		writeHelperCheck(conn, "auto-check") // Automation preflight
	default:
		_ = conn.Close()
	}
}

// writeHelperCheck runs the capture helper's non-prompting preflight subcommand in
// the daemon's granted (sh.reminal) context and writes "ok"/"no" back, then closes
// conn. The daemon is the ONLY place these checks report truthfully — run from a
// session (Terminal) or via prlctl (prltoolsd) the TCC identity is wrong. Powers
// the session-side polling that lets `reminal permissions` advance one grant at a
// time.
func writeHelperCheck(conn net.Conn, sub string) {
	out := "no"
	if p, e := captureHelperPath(); e == nil {
		if o, e := run(p, sub); e == nil {
			out = strings.TrimSpace(o)
		}
	}
	fmt.Fprintf(conn, "%s\n", out)
	_ = conn.Close()
}

// mirrorServeCapture streams a window's [uint32 BE len][payload] frames to conn
// by running the capture helper in the daemon's granted context. Closing conn
// stops it. An optional 5th arg selects the codec ("h264"); absent means JPEG,
// which is what pre-h264 sessions send. Falls back to a screencapture poll loop
// only when the native helper binary is absent AND the session asked for JPEG —
// an h264 request without a helper just closes the conn, so the session retries
// in jpeg mode (a permission/window failure likewise ends the stream — the
// session then reports it and `check` drives the "run reminal permissions" hint).
func mirrorServeCapture(conn net.Conn, args []string) {
	defer conn.Close()
	if len(args) < 4 {
		return
	}
	id, w, q, fps := args[0], args[1], args[2], args[3]
	codec := ""
	if len(args) >= 5 && args[4] == "h264" {
		codec = "h264"
	}
	helper, err := captureHelperPath()
	if err != nil {
		if codec != "h264" {
			mirrorScreencaptureLoop(conn, id, atoiOr(fps, 8))
		}
		return
	}
	// One capture process is all macOS allows us in practice: replayd keys a
	// stream's "application connection" by code-signing identity, so a SECOND
	// helper process starting any stream kills the first one's with
	// "application connection being interrupted" — a window view and the
	// windows-list previews could never coexist. Every capture therefore runs
	// inside one long-lived `reminal-capture serve` process, multiplexed by
	// stream id. The spawn-per-capture path below survives only for a helper
	// binary that predates serve mode (a REMINAL_CAPTURE_HELPER dev override).
	if capMux.serve(conn, helper, id, w, q, fps, codec) {
		return
	}
	cargs := []string{id, w, q, fps}
	if codec != "" {
		cargs = append(cargs, codec)
	}
	cmd := exec.Command(helper, cargs...)
	// Capture the helper's stderr. Without this Go wires a nil Stderr to
	// /dev/null, so the ONE line explaining why capture failed ("window 1234
	// not found", "stream stopped: …") was discarded before anything could see
	// it — every macOS capture failure reached the user as the generic
	// "capture helper exited", and the daemon log stayed empty too. Bounded,
	// because a wedged helper could otherwise spew without limit.
	errBuf := &capWriter{max: 4096}
	cmd.Stderr = errBuf
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return
	}
	stdin, err := cmd.StdinPipe() // lifeline + command channel
	if err != nil {
		return
	}
	if err := cmd.Start(); err != nil {
		if codec != "h264" {
			mirrorScreencaptureLoop(conn, id, atoiOr(fps, 8))
		}
		return
	}
	kill := func() {
		_ = stdin.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}
	// Forward session→daemon bytes into the helper's stdin: that's how "key\n"
	// (force an IDR) reaches the encoder. A Read returning error/EOF means the
	// session dropped the conn → stop the helper. Old sessions never write, so
	// this degrades to the pure lifeline it used to be.
	go func() {
		_, _ = io.Copy(stdin, conn)
		kill()
	}()
	_, _ = io.Copy(conn, stdout) // raw framed stream straight through
	kill()
	_ = cmd.Wait()
	// The stream ended. Tell the session WHY, and log it here too — this is
	// the only place the reason exists.
	if msg := strings.TrimSpace(errBuf.String()); msg != "" {
		fmt.Fprintf(os.Stderr, "reminal: capture %s ended: %s\n", id, msg)
		writeMirrorError(conn, msg)
	}
}

// capWriter keeps at most max bytes of what is written to it, dropping the
// rest. Enough to carry a helper's one-line failure without letting a
// misbehaving child grow the daemon's memory.
type capWriter struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (w *capWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if room := w.max - len(w.buf); room > 0 {
		if len(p) < room {
			room = len(p)
		}
		w.buf = append(w.buf, p[:room]...)
	}
	return len(p), nil // always "succeed": losing log text must not kill the pipe
}

func (w *capWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(w.buf)
}

// writeMirrorError appends an out-of-band error frame to a capture stream:
// the reserved length sentinel, then the message. An OLD session reads the
// sentinel as an absurd frame length and simply ends its read loop — exactly
// what it did before this existed — so the addition is backward compatible.
func writeMirrorError(conn net.Conn, msg string) {
	_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_, _ = conn.Write(errFrameBytes(msg))
}

// errFrameBytes builds that error frame as bytes, for the paths that go
// through a muxConn queue instead of writing a conn directly.
func errFrameBytes(msg string) []byte {
	if len(msg) > 480 {
		msg = msg[:480]
	}
	b := make([]byte, 8+len(msg))
	binary.BigEndian.PutUint32(b[0:4], winErrFrameMagic)
	binary.BigEndian.PutUint32(b[4:8], uint32(len(msg)))
	copy(b[8:], msg)
	return b
}

// ---- multiplexed capture: one helper process, every stream ------------------

// capMux is the daemon's one long-lived capture helper. See the comment at its
// call site in mirrorServeCapture for why concurrent helper PROCESSES cannot
// exist (replayd's per-identity connection), which is the whole reason this
// multiplexer does.
var capMux captureMux

type captureMux struct {
	mu          sync.Mutex
	stdin       io.WriteCloser      // helper's command channel; nil = not running
	conns       map[uint32]*muxConn // sid → the session conn its frames go to
	nextSID     uint32
	unsupported bool // helper binary predates serve mode — use the legacy path
	// idleGen counts helper generations, so a retirement timer started for one
	// helper cannot retire its successor.
	idleGen uint64
	// idleAfter is how long a helper with no streams left is kept; zero means
	// idleHelperGrace. Tests shorten it.
	idleAfter time.Duration
	// retiring is the exit signal of a helper that has been told to leave but
	// has not finished leaving. The next helper waits for it: replayd keys a
	// stream's application connection by code-signing identity (see the note
	// above serve), so two helper processes overlapping is the one thing this
	// mux exists to prevent.
	retiring <-chan struct{}
	// dead is the running helper's exit signal, kept so retirement can hand it
	// to the successor to wait on.
	dead <-chan struct{}
	// idleDeadline is when the CURRENT idle period is up. Every stream that
	// ends pushes it out, so a timer armed by an earlier one finds it in the
	// future and waits again instead of retiring a helper that has been busy
	// since.
	idleDeadline time.Time
}

// idleHelperGrace is how long the capture helper is kept alive after its last
// stream ends. A helper is deliberately long-lived — one process multiplexes
// every session's mirror — but "long-lived" must not mean "forever, whatever
// it is still doing". ScreenCaptureKit runs inside that process, and a stream
// it failed to tear down goes on capturing a window nobody is watching: the
// screen-recording indicator stays lit and a laptop's battery pays for it
// until someone restarts the machine. Retiring an idle helper puts a bound on
// that: whatever it was still holding ends with it — once every mirror on the
// machine has stopped. A leak beside a mirror somebody is genuinely watching
// outlives the grace, so this is a backstop for the teardown in the helper,
// not a substitute for it. The grace period is long
// enough that flipping between windows, or a pane reopening, reuses the
// running helper rather than paying to start one.
const idleHelperGrace = 30 * time.Second

// muxConn fans one stream's frames from the shared demux loop out to its
// session conn. The bounded queue and dedicated writer exist so one slow or
// stalled session can never block the demux loop — that loop feeds EVERY
// stream. Overflow ends this stream (the session just reconnects) instead of
// stalling the rest.
//
// Teardown discipline: ONLY the demux goroutine — the sole sender — may close
// frames (end/fail); closing it from the session side would race demux's
// sends, and send-on-closed-channel panics. The session side signals quit
// instead, which just stops the writer.
type muxConn struct {
	conn     net.Conn
	frames   chan []byte
	quit     chan struct{}
	endOnce  sync.Once
	quitOnce sync.Once
}

// end closes the frame queue; writeLoop drains what's left and closes the conn.
// Demux goroutine only — see the type comment.
func (mc *muxConn) end() { mc.endOnce.Do(func() { close(mc.frames) }) }

// fail tells the session why (best effort — the queue may be full) and ends.
// Demux goroutine only.
func (mc *muxConn) fail(msg string) {
	select {
	case mc.frames <- errFrameBytes(msg):
	default:
	}
	mc.end()
}

// stopDraining is the session side's teardown: the session is gone, stop
// writing to it. The sid is already out of m.conns by then, so demux stops
// feeding frames; any straggler already in flight fills the buffer harmlessly.
func (mc *muxConn) stopDraining() { mc.quitOnce.Do(func() { close(mc.quit) }) }

func (mc *muxConn) writeLoop() {
	// Closing the conn on the way out is what unblocks the command reader in
	// serve() when the stream ended helper-side, so cleanup converges from
	// either direction.
	defer mc.conn.Close()
	for {
		select {
		case b, ok := <-mc.frames:
			if !ok {
				return // drained — a closed channel yields its buffer first
			}
			_ = mc.conn.SetWriteDeadline(time.Now().Add(15 * time.Second))
			if _, err := mc.conn.Write(b); err != nil {
				return
			}
		case <-mc.quit:
			return
		}
	}
}

// serve runs one capture stream over the shared helper, blocking until the
// stream or the session is done (it owns conn, like the legacy path). Returns
// false — with nothing yet written to conn — when the helper binary doesn't
// speak serve mode, so the caller can fall back to spawning it per capture.
func (m *captureMux) serve(conn net.Conn, helper, id, w, q, fps, codec string) bool {
	m.mu.Lock()
	if m.unsupported {
		m.mu.Unlock()
		return false
	}
	if m.stdin == nil && !m.startLocked(helper) {
		m.mu.Unlock()
		return false
	}
	m.nextSID++ // never reused across helper restarts, so stale commands can't cross streams
	sid := m.nextSID
	mc := &muxConn{conn: conn, frames: make(chan []byte, 64), quit: make(chan struct{})}
	m.conns[sid] = mc
	m.mu.Unlock()

	go mc.writeLoop()
	if codec == "" {
		codec = "jpeg"
	}
	m.command(fmt.Sprintf("start %d %s %s %s %s %s", sid, id, w, q, fps, codec))

	// Read session→daemon commands until the session drops the conn: "key"
	// (force an IDR) forwards with this stream's sid. Old sessions never write,
	// so this blocks straight to EOF — the pure lifeline it always was.
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 256), 256)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == "key" {
			m.command(fmt.Sprintf("key %d", sid))
		}
	}

	m.mu.Lock()
	delete(m.conns, sid)
	m.mu.Unlock()
	mc.stopDraining()
	m.command(fmt.Sprintf("stop %d", sid)) // helper ignores an already-ended sid
	m.armIdleRetire()
	return true
}

// armIdleRetire starts the countdown to retiring an idle helper. Called after
// a stream ends; a no-op while other streams are still running.
//
// The daemon does this rather than the helper exiting by itself, and that is
// the whole point: the decision is made under the same lock that hands a new
// capture its stream, so a capture can never be handed to a helper that has
// already decided to leave. (It was: the session got a stream that never
// delivered a frame, and the pane stayed black until someone reopened it.)
func (m *captureMux) armIdleRetire() {
	m.mu.Lock()
	if len(m.conns) > 0 || m.stdin == nil {
		m.mu.Unlock()
		return
	}
	after := m.idleAfter
	if after <= 0 {
		after = idleHelperGrace
	}
	m.idleDeadline = time.Now().Add(after)
	gen := m.idleGen
	m.mu.Unlock()
	time.AfterFunc(after, func() { m.retireIfIdle(gen) })
}

// awaitRetiredLocked waits for a retired helper to actually be gone before its
// successor starts, so the two never overlap inside replayd. Called with m.mu
// held and RETURNS with it held: the lock is dropped only for the wait itself,
// because the retiring helper's demux needs it to finish its own teardown.
//
// Bounded: a helper that will not leave must not stop mirroring altogether,
// and the exit signal is the same one startLocked's probe already waits on.
func (m *captureMux) awaitRetiredLocked() {
	ch := m.retiring
	if ch == nil {
		return
	}
	m.mu.Unlock()
	select {
	case <-ch:
	case <-time.After(retiredExitWait):
	}
	m.mu.Lock()
	if m.retiring == ch {
		m.retiring = nil
	}
}

// retiredExitWait bounds that wait. A helper told to leave closes in
// milliseconds; this is the backstop for one that is wedged.
const retiredExitWait = 2 * time.Second

// retireIfIdle closes the helper's command channel — its cue to exit, and with
// it anything it still holds — unless a stream arrived in the meantime, or
// this helper has already been replaced.
func (m *captureMux) retireIfIdle(gen uint64) {
	m.mu.Lock()
	if m.idleGen != gen || m.stdin == nil || len(m.conns) > 0 {
		m.mu.Unlock()
		return
	}
	// Streams that came and went since this timer was armed pushed the
	// deadline out, and the grace belongs to the last of them: a pane opened
	// and closed a second before this fires must buy a full grace period, not
	// inherit the tail of an old one. Otherwise the helper goes moments after
	// someone stopped using it and the next pane pays a cold start — the exact
	// cost the grace exists to avoid.
	if left := time.Until(m.idleDeadline); left > 0 {
		m.mu.Unlock()
		time.AfterFunc(left, func() { m.retireIfIdle(gen) })
		return
	}
	defer m.mu.Unlock()
	_ = m.stdin.Close()
	m.stdin = nil
	m.idleGen++
	m.retiring = m.dead
	m.dead = nil
}

// command sends one line to the helper. A dead helper (stdin nilled by the
// demux loop) makes this a no-op — its streams are already being failed.
func (m *captureMux) command(line string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stdin != nil {
		_, _ = io.WriteString(m.stdin, line+"\n")
	}
}

// startLocked launches `reminal-capture serve` and waits for its hello — the
// sid-0 READY frame a serve helper writes within milliseconds of starting. A
// pre-serve binary argv-parses "serve" as a window id and dies with usage
// instead, writing nothing (its death, not a timer, resolves the probe:
// process-liveness checks can't be trusted here — an exited child is a zombie
// until reaped, and a zombie still signals as alive). Called with m.mu held;
// only capture starts stall behind it, and only while a helper comes up.
func (m *captureMux) startLocked(helper string) bool {
	m.awaitRetiredLocked()
	if m.stdin != nil {
		return true // someone else started one while we waited
	}
	cmd := exec.Command(helper, "serve")
	cmd.Stderr = &lineLogger{prefix: "reminal: "}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return false
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return false
	}
	if err := cmd.Start(); err != nil {
		return false
	}
	ready := make(chan struct{})
	dead := make(chan struct{})
	go m.demux(stdout, cmd, stdin, ready, dead)
	select {
	case <-ready:
		m.stdin = stdin
		m.dead = dead
		m.idleGen++ // a pending retirement belongs to the helper this replaces
		if m.conns == nil {
			m.conns = make(map[uint32]*muxConn)
		}
		return true
	case <-dead:
		// Died without a hello — demux latches m.unsupported off the exit code
		// (it needs m.mu for that, which we hold; it gets there right after we
		// return). This capture falls back to the legacy path either way.
		return false
	case <-time.After(2 * time.Second):
		// No hello, not dead: something is wedged. Kill it and fall back this
		// once, WITHOUT latching — a transient wedge must not cost concurrent
		// capture until the daemon restarts.
		_ = cmd.Process.Kill()
		return false
	}
}

// demux is the helper's single reader: [uint32 sid][uint32 len][payload] outer
// frames, payload forwarded verbatim (it is the exact inner framing sessions
// already parse), len 0 meaning "sid's stream ended". Runs until the helper
// dies or desyncs; then every live stream is failed and the next capture
// starts a fresh helper. Closes ready on the first outer frame (the hello) and
// dead when the helper is gone.
func (m *captureMux) demux(stdout io.Reader, cmd *exec.Cmd, stdin io.WriteCloser, ready, dead chan struct{}) {
	br := bufio.NewReaderSize(stdout, 512*1024)
	sawHello := false
	var hdr [8]byte
	for {
		if _, err := io.ReadFull(br, hdr[:]); err != nil {
			break
		}
		if !sawHello {
			sawHello = true
			close(ready)
		}
		sid := binary.BigEndian.Uint32(hdr[0:4])
		n := binary.BigEndian.Uint32(hdr[4:8])
		if n == 0 {
			m.endStream(sid)
			continue
		}
		if n > 16*1024*1024 {
			break // framing desync — kill and restart the helper
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(br, buf); err != nil {
			break
		}
		m.mu.Lock()
		mc := m.conns[sid]
		m.mu.Unlock()
		if mc == nil {
			continue // the hello (sid 0), or a stopped stream's late frames
		}
		select {
		case mc.frames <- buf:
		default:
			// This session stopped draining. Ending its stream (it will
			// reconnect) beats stalling every other stream behind it.
			m.endStream(sid)
		}
	}
	_ = stdin.Close()
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	err := cmd.Wait()
	// Usage (exit 2) with no hello = a pre-serve binary, not a crash.
	var exit *exec.ExitError
	preServe := !sawHello && errors.As(err, &exit) && exit.ExitCode() == 2
	// Wake a startLocked probe BEFORE taking m.mu — it waits holding that lock.
	close(dead)
	m.mu.Lock()
	if preServe && !m.unsupported {
		m.unsupported = true
		fmt.Fprintln(os.Stderr, "reminal: capture helper predates serve — one process per capture (no concurrent streams)")
	}
	// Only the ACTIVE helper's demux may fail streams: a probe helper that
	// never registered must not touch conns a fresh helper now serves.
	var orphans map[uint32]*muxConn
	if m.stdin == stdin {
		m.stdin = nil
		orphans = m.conns
		m.conns = make(map[uint32]*muxConn)
	}
	m.mu.Unlock()
	for _, mc := range orphans {
		mc.fail("capture service restarted")
	}
}

// endStream detaches sid; its writeLoop drains and closes the session conn.
func (m *captureMux) endStream(sid uint32) {
	m.mu.Lock()
	mc := m.conns[sid]
	delete(m.conns, sid)
	m.mu.Unlock()
	if mc != nil {
		mc.end()
	}
}

// lineLogger relays a child's stderr into the daemon's log one prefixed line
// at a time, bounded per line so a misbehaving child can't grow daemon memory.
// This is how a serve helper's "capture <id> ended: <why>" reasons — which
// exist nowhere else — reach daemon.log.
type lineLogger struct {
	mu     sync.Mutex
	prefix string
	buf    []byte
}

func (l *lineLogger) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, c := range p {
		if c == '\n' {
			fmt.Fprintf(os.Stderr, "%s%s\n", l.prefix, l.buf)
			l.buf = l.buf[:0]
			continue
		}
		if len(l.buf) < 4096 {
			l.buf = append(l.buf, c)
		}
	}
	return len(p), nil
}

// mirrorScreencaptureLoop is the no-native-helper fallback: poll screencapture at
// fps and emit [len][JPEG] frames until the session closes the connection.
func mirrorScreencaptureLoop(conn net.Conn, id string, fps int) {
	b := darwinWindows{}
	var target winInfo
	if wins, err := b.list(); err == nil {
		for _, w := range wins {
			if w.ID == id {
				target = w
				break
			}
		}
	}
	if target.ID == "" {
		return
	}
	done := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, conn); close(done) }()
	if fps < 1 {
		fps = 1
	}
	tick := time.NewTicker(time.Second / time.Duration(fps))
	defer tick.Stop()
	var hdr [4]byte
	for {
		select {
		case <-done:
			return
		case <-tick.C:
			img, err := b.capture(target)
			if err != nil || len(img) == 0 {
				continue
			}
			binary.BigEndian.PutUint32(hdr[:], uint32(len(img)))
			if _, err := conn.Write(hdr[:]); err != nil {
				return
			}
			if _, err := conn.Write(img); err != nil {
				return
			}
		}
	}
}

// mirrorServeCaptureRegion returns one JPEG of a screen rectangle (used for the
// right-click context menu, which the OS draws as a separate overlapping window).
// Written as a single [len][JPEG] frame, like the streaming path.
func mirrorServeCaptureRegion(conn net.Conn, args []string) {
	if len(args) < 4 {
		return
	}
	img, err := darwinWindows{}.captureRegion(atoiOr(args[0], 0), atoiOr(args[1], 0), atoiOr(args[2], 0), atoiOr(args[3], 0))
	if err != nil || len(img) == 0 {
		return
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(img)))
	_, _ = conn.Write(hdr[:])
	_, _ = conn.Write(img)
}

// The daemon's record of the front window. Each input arrives on its own
// short-lived connection, so this can't live on the connection. Same rule and
// the same implementation the in-process backend uses — see frontWindowTracker,
// which explains why raising is keyed on which window is in front rather than
// on how recently the last event arrived.
var mirrorInput inputState

// mirrorServeInput injects one forwarded viewer event using the daemon's granted
// backend, then replies ok/error.
// mirrorServeInput injects one forwarded viewer event using the daemon's granted
// backend, then replies ok/error. The injection is applyWindowInput, shared with
// the in-process path so the two cannot drift apart again.
func mirrorServeInput(conn net.Conn, payload string) {
	var ev windowInput
	if json.Unmarshal([]byte(payload), &ev) != nil {
		fmt.Fprintln(conn, "error: bad event")
		return
	}
	// The daemon has no Agent to hang state on, so it keeps its own record of
	// the front window, its own run of clicks and its own drag watchdog.
	applyWindowInput(darwinWindows{}, &mirrorInput, ev, nil)
	fmt.Fprintln(conn, "ok")
}

func atoiOr(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

// ---- session side (client) -------------------------------------------------

// mirrorDialTimeout bounds how long a session waits to reach the daemon before
// treating screen sharing as "service restarting."
const mirrorDialTimeout = 2 * time.Second

// errMirrorUnavailable marks a capture failure that carries NO information
// about whether capture can work: the daemon was restarting, or its socket
// wasn't there yet. Callers must not read it as evidence against a codec — a
// daemon bounce lasts about a second, and treating it as "this machine cannot
// encode H.264" silently drops every open pane to JPEG for the rest of its
// life. Wrapped, so the underlying dial error is still available.
var errMirrorUnavailable = errors.New("screen-sharing service starting — retry")

// startMirrorCapture (session side) dials the daemon's mirror socket and returns
// a winHelper streaming the window's frames from the daemon (sh.reminal context).
// The helper reads the same framed format as the direct exec path, so the
// caller's streaming logic is unchanged. codec "" means jpeg; "h264" appends the
// codec token (an OLD daemon ignores it and streams JPEG — the winHelper framing
// validator catches that and ends the stream, so the caller falls back to jpeg).
// Errors when the daemon is unreachable — callers surface "screen-sharing
// service restarting" rather than falling back to a terminal-attributed capture.
func startMirrorCapture(id string, maxWidth, quality, fps int, codec string) (*winHelper, error) {
	sock, err := mirrorSockPath()
	if err != nil {
		return nil, err
	}
	conn, err := net.DialTimeout("unix", sock, mirrorDialTimeout)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errMirrorUnavailable, err)
	}
	line := fmt.Sprintf("capture %s %d %d %d\n", id, maxWidth, quality, fps)
	if codec == "h264" {
		line = fmt.Sprintf("capture %s %d %d %d %s\n", id, maxWidth, quality, fps, codec)
	}
	if _, err := io.WriteString(conn, line); err != nil {
		_ = conn.Close()
		return nil, err
	}
	h := &winHelper{
		conn:   conn,
		codec:  codec,
		sig:    make(chan struct{}, 1),
		dead:   make(chan struct{}),
		stderr: &bytes.Buffer{},
	}
	go h.readLoop(conn)
	select {
	case <-h.dead:
		// The daemon dropped the stream before it got going (permission/window
		// gone). readLoop ended but doesn't own the conn — close it so we don't
		// leak the fd (the happy path closes it via winHelper.stop()).
		_ = conn.Close()
		if msg := h.errorText(); msg != "" {
			return nil, errors.New(msg) // the daemon told us why
		}
		return nil, errors.New("screen-sharing service closed the stream")
	case <-time.After(helperStartupGrace):
		return h, nil
	}
}

// mirrorForwardInput (session side) forwards one viewer input event (the decrypted
// JSON) to the daemon, which injects it in the granted sh.reminal context. Best
// effort — a missed click is better than blocking the input worker.
func mirrorForwardInput(eventJSON string) {
	sock, err := mirrorSockPath()
	if err != nil {
		return
	}
	conn, err := net.DialTimeout("unix", sock, mirrorDialTimeout)
	if err != nil {
		return
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(4 * time.Second))
	if _, err := fmt.Fprintf(conn, "input %s\n", strings.TrimSpace(eventJSON)); err != nil {
		return
	}
	var reply [16]byte
	_, _ = conn.Read(reply[:]) // wait for ok/error so events stay ordered
}

// mirrorCheck (session side) asks the daemon whether Screen Recording is granted
// in its context. Returns "ok"/"no", or "" when the daemon is unreachable.
func mirrorCheck() string { return mirrorGrantQuery("check") }

// mirrorGrantQuery (session side) asks the daemon to run one non-prompting
// permission preflight in ITS granted (sh.reminal) context — "check" (Screen
// Recording), "axcheck" (Accessibility), or "autocheck" (Automation). Returns
// "ok"/"no", or "" when the daemon is unreachable. This is the only trustworthy
// vantage point: the same check run from a session (Terminal) or via prlctl
// (prltoolsd) reports the wrong identity. Drives `reminal permissions` polling.
func mirrorGrantQuery(cmd string) string {
	sock, err := mirrorSockPath()
	if err != nil {
		return ""
	}
	conn, err := net.DialTimeout("unix", sock, mirrorDialTimeout)
	if err != nil {
		return ""
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(4 * time.Second))
	if _, err := fmt.Fprintln(conn, cmd); err != nil {
		return ""
	}
	var buf [16]byte
	n, _ := conn.Read(buf[:])
	return strings.TrimSpace(string(buf[:n]))
}

// mirrorRelease (session side) asks the daemon to release any held mouse button —
// injection happens in the daemon, so a stranded press from an interrupted drag
// must be cleared there, not in this session's (Terminal) context.
func mirrorRelease() {
	sock, err := mirrorSockPath()
	if err != nil {
		return
	}
	conn, err := net.DialTimeout("unix", sock, mirrorDialTimeout)
	if err != nil {
		return
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(4 * time.Second))
	if _, err := fmt.Fprintln(conn, "release"); err != nil {
		return
	}
	var reply [16]byte
	_, _ = conn.Read(reply[:])
}

// mirrorCaptureRegion (session side) fetches one region JPEG from the daemon (for
// the right-click context menu), reading a single [len][JPEG] frame.
func mirrorCaptureRegion(x, y, w, h int) ([]byte, error) {
	sock, err := mirrorSockPath()
	if err != nil {
		return nil, err
	}
	conn, err := net.DialTimeout("unix", sock, mirrorDialTimeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(4 * time.Second))
	if _, err := fmt.Fprintf(conn, "captureregion %d %d %d %d\n", x, y, w, h); err != nil {
		return nil, err
	}
	var lenBuf [4]byte
	if _, err := io.ReadFull(conn, lenBuf[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(lenBuf[:])
	if n == 0 || n > 16*1024*1024 {
		return nil, errors.New("bad region frame")
	}
	img := make([]byte, n)
	if _, err := io.ReadFull(conn, img); err != nil {
		return nil, err
	}
	return img, nil
}
