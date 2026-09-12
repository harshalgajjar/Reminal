// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// winHelperFPS is the frame-rate ceiling handed to the reminal-capture helper
// for JPEG streams. Each frame is an independent ~60 KB image, so the rate is
// bounded by wire cost, not by the encoder: 30fps of JPEG already costs ~20
// Mbps on busy content. The WS relay path is capped far below this
// independently — see wsFrameMinInterval.
const winHelperFPS = 30

// winHelperFPSH264 is the ceiling for compressed-video streams. H.264 only
// runs peer-to-peer over a DataChannel (never the billed relay), and temporal
// compression makes extra frames nearly free — measured on a full-motion
// 1100×700 window: 30fps = 1.7 Mbps, 60fps = 2.6 Mbps for the same picture,
// where the JPEG path would need ~41 Mbps for that frame rate. 60 is the
// ceiling rather than the panel's 120 because the gain past 60 isn't visible
// on a window mirror and every frame still costs host encode + viewer decode.
const winHelperFPSH264 = 60

// winCaptureQuality is the JPEG quality (0-100) the helper encodes at, matching
// the old sips path so frame sizes and looks are unchanged.
const winCaptureQuality = 45

// helperStartupGrace bounds how long startWinHelper waits to see whether the
// helper dies immediately (missing Screen Recording permission, window gone).
// SCShareableContent rejects fast, so a helper still alive after this is healthy.
const helperStartupGrace = 700 * time.Millisecond

// winFrame is one frame off the capture helper: a JPEG (H264 false), or an
// H.264 Annex-B access unit (H264 true; Key marks a self-contained IDR AU —
// SPS/PPS inline — that a decoder can start from).
type winFrame struct {
	Data []byte
	H264 bool
	Key  bool
}

// winErrFrameMagic is a reserved length prefix marking an out-of-band error
// frame on a capture stream rather than a picture: [magic][uint32 len][utf8].
// It is far above any real frame size, so a reader that predates it treats it
// as a framing desync and ends the stream — which is precisely the behaviour
// it had before, making the channel safe to add mid-protocol.
const winErrFrameMagic = 0xFFFFFFFF

// Frame flags in the helper's h264 framing ([uint32 len][flag][Annex-B AU]).
// The same two values label AUs inside a batched relay message, so the viewer
// parses one shape wherever the bytes arrived from.
const (
	flagH264Delta = 1
	flagH264Key   = 2
)

// winH264QueueMax bounds the pending-frame queue in h264 mode. JPEG mode keeps
// only the newest frame (each is independently decodable), but H.264 deltas
// depend on every predecessor, so frames must queue. If the consumer falls this
// far behind (~2s at 30fps), the queue is cleared and a fresh IDR is requested
// instead — bounded memory and bounded staleness, at the cost of one keyframe.
const winH264QueueMax = 64

// winHelper streams one window's frames from the reminal-capture helper
// process. In JPEG mode it keeps only the newest frame (a mirror only ever
// wants the latest picture); in h264 mode it queues frames in order (deltas
// are worthless without their predecessors) and re-keys after any gap. next()
// is signalled when content arrives.
type winHelper struct {
	cmd *exec.Cmd
	// stdin is held open purely as a lifeline: the helper exits on stdin EOF,
	// so it can't outlive the agent even when the agent dies without running
	// defers — SIGKILL, a crash, or the hot-restart syscall.Exec (which closes
	// this fd via CLOEXEC). Without it, a helper on a static window would
	// linger forever: it only notices a dead peer when a frame WRITE fails, and
	// a static window never writes. In h264 mode it doubles as the command
	// channel ("key\n" → force an IDR).
	stdin io.WriteCloser

	// conn is set instead of cmd/stdin when the frames come from the daemon's
	// mirror socket (the session-delegates-to-daemon path). Closing it stops the
	// daemon-side capture, so it doubles as the lifeline; writes to it reach the
	// helper's stdin (the daemon forwards them).
	conn net.Conn

	codec string // "jpeg" (default) or "h264" — selects the stdout framing

	// stopFn and keyFn override the default process/stdin teardown and keyframe
	// request for helpers that are not the macOS reminal-capture process — the
	// ffmpeg-backed Windows/Linux helper drives its own capture goroutine and
	// encoder subprocess, and its stdin carries raw pixels, so a literal "key\n"
	// written there would corrupt the stream. When set they are used instead of
	// touching cmd/stdin/conn. Nil for the macOS and daemon paths.
	stopFn func()
	keyFn  func()

	mu           sync.Mutex
	latest       []byte     // jpeg: newest frame not yet consumed; nil once taken
	queue        []winFrame // h264: pending frames in decode order
	dropUntilKey bool       // h264: discard deltas until the next IDR arrives

	sig    chan struct{} // buffered(1): coalesced "new frame available"
	dead   chan struct{} // closed when the reader loop ends (process/pipe gone)
	stderr *bytes.Buffer

	// badFraming is set when an h264 stream delivered bytes that aren't the
	// h264 framing — the signature of an OLD daemon that ignored the codec arg
	// and streamed JPEGs. The consumer uses it to stop asking for h264.
	badFraming atomic.Bool

	// remoteErr holds the failure reason reported by the daemon for a stream
	// it ran on our behalf (the direct-exec path uses stderr instead).
	remoteErr atomic.Value // string
}

// errorText is why this helper stopped, in the caller's preferred order: the
// daemon's report when capture ran there, else the child's own stderr. Empty
// when it simply ended with nothing to say.
func (h *winHelper) errorText() string {
	if v, ok := h.remoteErr.Load().(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	if h.stderr != nil {
		return strings.TrimSpace(h.stderr.String())
	}
	return ""
}

// captureHelperPath finds the reminal-capture binary: an explicit override
// first (handy for local testing), then next to the running reminal binary
// (how releases bundle it), then PATH. macOS-only — the helper needs
// ScreenCaptureKit; other platforms fall back to the screencapture path.
func captureHelperPath() (string, error) {
	if runtime.GOOS != "darwin" {
		return "", errors.New("capture helper is macOS-only")
	}
	if p := strings.TrimSpace(os.Getenv("REMINAL_CAPTURE_HELPER")); p != "" {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
		return "", fmt.Errorf("REMINAL_CAPTURE_HELPER=%s not found", p)
	}
	if exe, err := os.Executable(); err == nil {
		// Resolve symlinks: the release installs reminal.app and symlinks the CLI
		// (~/.local/bin/reminal -> reminal.app/Contents/MacOS/reminal), so the
		// helper lives next to the RESOLVED path (inside the bundle), not next to
		// the symlink.
		if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
			exe = resolved
		}
		cand := filepath.Join(filepath.Dir(exe), "reminal-capture")
		if st, err := os.Stat(cand); err == nil && !st.IsDir() {
			return cand, nil
		}
	}
	if p, err := exec.LookPath("reminal-capture"); err == nil {
		return p, nil
	}
	return "", errors.New("reminal-capture not found")
}

// startWinHelper spawns the helper for window id and returns it once it's proven
// healthy (survived the startup grace without dying). Returns an error — with
// the helper's stderr when it exited — so the caller falls back to screencapture
// (or from h264 to jpeg). codec "" means jpeg; the codec argv is only appended
// for h264, keeping the jpeg spawn byte-identical to what old helpers expect.
func startWinHelper(id string, maxWidth, quality, fps int, codec string) (*winHelper, error) {
	path, err := captureHelperPath()
	if err != nil {
		return nil, err
	}
	args := []string{id, strconv.Itoa(maxWidth), strconv.Itoa(quality), strconv.Itoa(fps)}
	if codec == "h264" {
		args = append(args, codec)
	}
	cmd := exec.Command(path, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stdin, err := cmd.StdinPipe() // lifeline — see winHelper.stdin
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, err
	}
	h := &winHelper{
		cmd:    cmd,
		stdin:  stdin,
		codec:  codec,
		sig:    make(chan struct{}, 1),
		dead:   make(chan struct{}),
		stderr: &stderr,
	}
	go h.readLoop(stdout)

	// Watch for early death (permission denied / window not found exit within a
	// few hundred ms). Survive the grace window → healthy.
	select {
	case <-h.dead:
		msg := h.errorText()
		if msg == "" {
			msg = "capture helper exited immediately"
		}
		return nil, errors.New(msg)
	case <-time.After(helperStartupGrace):
		return h, nil
	}
}

// readLoop reads [uint32 big-endian length][payload] frames off the helper's
// stdout until the pipe closes. JPEG mode: payload is the JPEG, keep only the
// newest. h264 mode: payload is [1 flag byte][Annex-B AU] — flag 1 delta, 2
// key — queued in decode order. Any other flag value means the peer is NOT
// speaking the h264 framing (an old daemon that ignored the codec arg streams
// plain JPEGs, whose first byte is 0xFF) — end the stream so the caller falls
// back to jpeg mode instead of feeding garbage to a decoder.
func (h *winHelper) readLoop(stdout io.Reader) {
	defer close(h.dead)
	r := bufio.NewReaderSize(stdout, 512*1024)
	var lenBuf [4]byte
	for {
		if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
			return
		}
		n := binary.BigEndian.Uint32(lenBuf[:])
		if n == winErrFrameMagic {
			// The daemon is telling us why the stream is ending (see
			// writeMirrorError). Record it so the pane can show the real
			// reason instead of a generic "helper exited".
			if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
				return
			}
			mlen := binary.BigEndian.Uint32(lenBuf[:])
			if mlen == 0 || mlen > 4096 {
				return
			}
			msg := make([]byte, mlen)
			if _, err := io.ReadFull(r, msg); err != nil {
				return
			}
			h.remoteErr.Store(string(msg))
			continue
		}
		// Sanity bound: a 1100px JPEG is tens of KB; anything past 16 MiB is a
		// framing desync, not a frame.
		if n == 0 || n > 16*1024*1024 {
			return
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(r, buf); err != nil {
			return
		}
		if h.codec == "h264" {
			if n < 2 || (buf[0] != flagH264Delta && buf[0] != flagH264Key) {
				h.badFraming.Store(true)
				return // framing mismatch — see doc comment
			}
			h.pushH264(winFrame{Data: buf[1:], H264: true, Key: buf[0] == flagH264Key})
		} else {
			h.mu.Lock()
			h.latest = buf
			h.mu.Unlock()
		}
		select {
		case h.sig <- struct{}{}:
		default: // a pending signal already covers this newer frame
		}
	}
}

// pushH264 queues one decoded-order frame, honoring the drop-until-key gate and
// the bounded queue. On overflow the queue is cleared and a fresh IDR requested
// — the consumer then resumes from the next key with no undecodable gap.
func (h *winHelper) pushH264(f winFrame) {
	h.mu.Lock()
	if h.dropUntilKey {
		if !f.Key {
			h.mu.Unlock()
			return
		}
		h.dropUntilKey = false
	}
	if len(h.queue) >= winH264QueueMax {
		h.queue = h.queue[:0]
		h.dropUntilKey = !f.Key // an overflowing key frame can still start fresh
		if h.dropUntilKey {
			h.mu.Unlock()
			h.requestKey()
			return
		}
	}
	h.queue = append(h.queue, f)
	h.mu.Unlock()
}

// next returns the next unconsumed frame, blocking up to timeout for a fresh
// one. ok is false on timeout (nothing arrived this interval — with the
// helper's 1fps idle refresh that means the helper is stalled, not merely the
// window static), on stop, or when the helper died. A returned frame is always
// worth shipping: a detected change, or the helper's paced idle refresh. In
// h264 mode frames come off a queue in decode order, and the caller MUST ship
// every frame it takes (a skipped delta corrupts the stream — use rekey() when
// dropping is unavoidable).
func (h *winHelper) next(stop <-chan struct{}, timeout time.Duration) (f winFrame, ok bool) {
	deadline := time.After(timeout)
	for {
		h.mu.Lock()
		f, ok = h.popLocked()
		h.mu.Unlock()
		if ok {
			return f, true
		}
		// The sig channel is coalesced (buffered 1), so a wake-up with nothing
		// to pop is possible — e.g. the signal for a frame that was already
		// taken via the queue-first path. Loop rather than report a false
		// "static interval".
		select {
		case <-h.sig:
		case <-deadline:
			return winFrame{}, false
		case <-stop:
			return winFrame{}, false
		case <-h.dead:
			return winFrame{}, false
		}
	}
}

// popLocked takes the next frame under h.mu: queue head in h264 mode, the
// newest JPEG otherwise. Re-signals when queued frames remain so the consumer
// keeps draining without waiting on the producer.
func (h *winHelper) popLocked() (winFrame, bool) {
	if h.codec == "h264" {
		if len(h.queue) == 0 {
			return winFrame{}, false
		}
		f := h.queue[0]
		h.queue = h.queue[1:]
		if len(h.queue) > 0 {
			select {
			case h.sig <- struct{}{}:
			default:
			}
		}
		return f, true
	}
	img := h.latest
	h.latest = nil
	return winFrame{Data: img}, img != nil
}

// rekey discards all pending h264 frames and requests a fresh IDR from the
// encoder. Call whenever the consumer had to drop a frame (backpressure) or a
// new viewer needs an entry point — the next frame out of next() will be a
// self-contained keyframe. No-op in jpeg mode.
func (h *winHelper) rekey() {
	if h.codec != "h264" {
		return
	}
	h.mu.Lock()
	h.queue = h.queue[:0]
	h.dropUntilKey = true
	h.mu.Unlock()
	h.requestKey()
}

// requestKey asks the running helper for an immediate IDR ("key\n" on its
// command channel — stdin when spawned directly, the daemon conn otherwise;
// the daemon forwards conn bytes to the helper's stdin). Best effort.
func (h *winHelper) requestKey() {
	if h.keyFn != nil {
		h.keyFn()
		return
	}
	if h.conn != nil {
		_, _ = h.conn.Write([]byte("key\n"))
		return
	}
	if h.stdin != nil {
		_, _ = io.WriteString(h.stdin, "key\n")
	}
}

// alive reports whether the helper process/reader is still running.
func (h *winHelper) alive() bool {
	select {
	case <-h.dead:
		return false
	default:
		return true
	}
}

// stop kills the helper process and waits for it to reap. Closing stdin first
// gives it the graceful EOF exit; the Kill is the backstop.
func (h *winHelper) stop() {
	if h.stopFn != nil {
		h.stopFn() // ffmpeg-backed helper: stop its capture goroutine + encoder
		return
	}
	if h.conn != nil {
		_ = h.conn.Close() // daemon detects the closed conn and stops its helper
		return
	}
	_ = h.stdin.Close()
	if h.cmd.Process != nil {
		_ = h.cmd.Process.Kill()
	}
	_ = h.cmd.Wait()
}
