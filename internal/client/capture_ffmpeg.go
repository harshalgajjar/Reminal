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
	"math"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// itoa is a local shorthand for the ffmpeg argument list below.
func itoa(n int) string { return strconv.Itoa(n) }

// H.264 for Windows and Linux, via ffmpeg. macOS captures and encodes in the
// native reminal-capture helper (ScreenCaptureKit + VideoToolbox); the other
// platforms have no such helper, so their window mirror was plain JPEG — ~15-40
// Mbps where H.264 needs ~2, which made a smooth remote window impossible on any
// real link. This closes that gap without a second native codebase: frames are
// captured in Go (the same GDI / X11 grab the JPEG path already uses) and piped
// through ffmpeg, which reaches the platform's hardware encoder — Media
// Foundation (h264_mf) on Windows, VAAPI (h264_vaapi) on Linux — or falls back
// to libx264 in software. ffmpeg's Annex-B output is re-framed into the exact
// [len][flag][Annex-B AU] shape the macOS helper emits, so the winHelper
// consumer, the DataChannel transport and the viewer's WebCodecs decoder need no
// changes at all: to everything downstream this is just another H.264 helper.

// rawCapturer is a windowBackend that can hand back one frame as tightly-packed
// RGBA at an exact size — the input ffmpeg's rawvideo demuxer needs. The Win32
// and X11 backends implement it; a backend that doesn't simply gets no ffmpeg
// helper and stays on JPEG.
type rawCapturer interface {
	// captureRaw returns exactly tw*th*4 bytes of RGBA for window w, scaled to
	// tw×th (forced to that exact size — a resized window renders distorted until
	// the stream restarts the helper, never a byte-count mismatch that would
	// desync ffmpeg's fixed-size input).
	captureRaw(w winInfo, tw, th int) ([]byte, error)
}

// ffmpegAvailable reports whether an ffmpeg the helper can drive is on PATH. Its
// absence is why startFFmpegHelper returns an error the stream reads as "no
// H.264 here" and quietly keeps serving JPEG.
func ffmpegAvailable() bool { return have("ffmpeg") || os.Getenv("REMINAL_FFMPEG") != "" }

// ffmpegPath is the ffmpeg binary to run: an explicit override first (handy for
// a bundled copy), else PATH.
func ffmpegPath() string {
	if p := strings.TrimSpace(os.Getenv("REMINAL_FFMPEG")); p != "" {
		return p
	}
	return "ffmpeg"
}

// evenDown rounds down to an even number (>=2). H.264 4:2:0 needs even
// dimensions, and the encoder rejects odd ones.
func evenDown(n int) int {
	if n < 2 {
		return 2
	}
	return n &^ 1
}

// ffmpegTargetDims scales a window's content size down so its longer side is at
// most maxWidth, preserving aspect and forcing even dimensions. The result is
// fixed for the helper's life; a resize is handled by the stream restarting it.
func ffmpegTargetDims(w winInfo, maxWidth int) (tw, th int) {
	cw, ch := w.W, w.H
	if cw <= 0 || ch <= 0 {
		cw, ch = maxWidth, maxWidth*9/16 // last-ditch default; a real capture corrects it
	}
	if maxWidth <= 0 {
		maxWidth = 1100
	}
	scale := 1.0
	longer := cw
	if ch > longer {
		longer = ch
	}
	if longer > maxWidth {
		scale = float64(maxWidth) / float64(longer)
	}
	return evenDown(int(float64(cw) * scale)), evenDown(int(float64(ch) * scale))
}

// ffmpegBitrate mirrors the macOS helper's budget: ~0.074 bits per pixel-frame
// at 30fps (measured 1.7 Mbps for a full-motion 1100×700 window), scaled
// sublinearly with frame rate — extra frames on temporally-compressed video are
// far cheaper than linear — and clamped to a sane band.
func ffmpegBitrate(tw, th, fps int) int {
	if fps <= 0 {
		fps = 30
	}
	base := float64(tw*th) * 2.2 * math.Pow(float64(fps)/30.0, 0.6)
	br := int(base)
	if br < 600_000 {
		br = 600_000
	}
	if br > 12_000_000 {
		br = 12_000_000
	}
	return br
}

// h264Encoder picks the H.264 encoder ffmpeg should use. Hardware first — Media
// Foundation on Windows, VAAPI on Linux — because software x264 at these sizes
// can cost more CPU than the rest of the agent combined; libx264 is the portable
// fallback that a full ffmpeg always has. REMINAL_FFMPEG_ENCODER overrides the
// choice (e.g. to force libx264 when a flaky VAAPI stack keeps failing).
func h264Encoder(goos string) (name string, encodeArgs []string, ok bool) {
	if forced := strings.TrimSpace(os.Getenv("REMINAL_FFMPEG_ENCODER")); forced != "" {
		return forced, encoderArgs(forced), true
	}
	list := ffmpegEncoders()
	var prefer []string
	switch goos {
	case "windows":
		prefer = []string{"h264_mf", "libx264", "h264_qsv", "h264_nvenc"}
	case "linux":
		// libx264 before VAAPI by default: VAAPI needs a working /dev/dri render
		// node and the hwupload filter chain, which is often absent on servers
		// and headless boxes, and a failed hardware init just stalls the mirror.
		// A box that has VAAPI set up can force it with REMINAL_FFMPEG_ENCODER.
		prefer = []string{"libx264", "h264_vaapi", "h264_nvenc"}
	default:
		prefer = []string{"libx264"}
	}
	for _, e := range prefer {
		if list[e] {
			return e, encoderArgs(e), true
		}
	}
	// Nothing we recognise — but ffmpeg without libx264 is unusual, so try it
	// blind rather than refuse; a start failure falls back to JPEG anyway.
	return "libx264", encoderArgs("libx264"), true
}

// encoderArgs returns the low-latency tuning for one encoder. The common goal is
// no B-frames, no lookahead, and small, frequent keyframes so a viewer that
// joins or drops a frame recovers within about a second.
func encoderArgs(enc string) []string {
	switch enc {
	case "libx264":
		// superfast, NOT ultrafast: ultrafast forces CAVLC, which downgrades the
		// stream to Constrained Baseline even with -profile:v main, so it would
		// not match the Main profile the viewer declares (avc1.4d0028) or the
		// macOS VideoToolbox path. superfast keeps CABAC → real Main, at a small
		// CPU cost. zerolatency drops B-frames and lookahead; level is left to
		// the encoder (the viewer decodes from the in-band SPS regardless).
		return []string{"-preset", "superfast", "-tune", "zerolatency",
			"-profile:v", "main", "-x264-params", "sliced-threads=0:sync-lookahead=0:rc-lookahead=0:bframes=0"}
	case "h264_mf":
		// Media Foundation. Do NOT pass -profile:v main: h264_mf's profile
		// option is not a named-constant enum (ffmpeg reports "Unable to parse
		// profile option value main" and fails to open the encoder), so we let
		// MF choose the profile. The viewer decodes from the in-band SPS
		// regardless of the avc1.4d0028 it declares, so this still plays. No
		// -hw_encoding either: forcing it fails on a box with no hardware MFT
		// (a VM, a headless server); MF uses hardware automatically when present.
		return nil
	case "h264_vaapi":
		return []string{"-profile:v", "main"}
	case "h264_qsv":
		return []string{"-preset", "veryfast", "-profile:v", "main"}
	case "h264_nvenc":
		return []string{"-preset", "p1", "-tune", "ll", "-profile:v", "main", "-bf", "0"}
	default:
		return []string{"-profile:v", "main"}
	}
}

// ffmpegEncoders returns the set of H.264 encoder names this ffmpeg has, parsed
// from `ffmpeg -encoders`. Empty on any error (the caller then tries libx264
// blind).
func ffmpegEncoders() map[string]bool {
	out := map[string]bool{}
	cmd := exec.Command(ffmpegPath(), "-hide_banner", "-encoders")
	b, err := cmd.Output()
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(b), "\n") {
		// Lines look like " V....D h264_vaapi   H.264/AVC (VAAPI)". The encoder
		// name is the second whitespace field.
		f := strings.Fields(line)
		if len(f) >= 2 && strings.HasPrefix(f[1], "h264") || (len(f) >= 2 && f[1] == "libx264") {
			out[f[1]] = true
		}
	}
	return out
}

// startFFmpegHelper captures window w in Go and encodes it to H.264 through
// ffmpeg, delivering frames as the same [len][flag][AU] stream startWinHelper
// produces on macOS. Returns an error (→ the stream falls back to JPEG) if the
// backend can't hand back raw frames, ffmpeg is missing, the first capture
// fails, or the encoder dies inside the startup grace.
func startFFmpegHelper(b windowBackend, w winInfo, maxWidth, quality, fps int) (*winHelper, error) {
	rc, ok := b.(rawCapturer)
	if !ok {
		return nil, errors.New("backend cannot capture raw frames for ffmpeg")
	}
	if !ffmpegAvailable() {
		return nil, errors.New("ffmpeg not found — install it for H.264 (JPEG mirror still works)")
	}
	if fps <= 0 {
		fps = winHelperFPS
	}
	tw, th := ffmpegTargetDims(w, maxWidth)

	// Prove capture works and the dimensions are right before spending an ffmpeg
	// process on it: a first frame that fails is exactly the closed-window /
	// no-permission case the stream must see as an error, not a black video.
	first, err := rc.captureRaw(w, tw, th)
	if err != nil {
		return nil, fmt.Errorf("initial capture: %w", err)
	}
	if want := tw * th * 4; len(first) != want {
		return nil, fmt.Errorf("capture returned %d bytes, want %d for %dx%d RGBA", len(first), want, tw, th)
	}

	enc, encArgs, _ := h264Encoder(runtime.GOOS)
	bitrate := ffmpegBitrate(tw, th, fps)
	// One keyframe per second bounds recovery: ffmpeg cannot be told to emit an
	// IDR on demand through a raw pipe, so a viewer that joins mid-stream or
	// loses a frame waits at most this long for a self-contained entry point
	// (keyFn is therefore a no-op — see below). It is the same "bounded
	// staleness" contract the queue overflow path already relies on.
	keyint := fps
	if keyint < 1 {
		keyint = 1
	}
	args := []string{
		"-hide_banner", "-loglevel", "error",
		"-f", "rawvideo", "-pix_fmt", "rgba", "-s", fmt.Sprintf("%dx%d", tw, th), "-r", itoa(fps), "-i", "-",
		"-an",
		"-c:v", enc,
	}
	args = append(args, encArgs...)
	args = append(args,
		"-pix_fmt", "yuv420p",
		"-g", itoa(keyint), "-keyint_min", itoa(keyint),
		"-force_key_frames", "expr:gte(t,n_forced*1)",
		"-b:v", itoa(bitrate), "-maxrate", itoa(bitrate), "-bufsize", itoa(bitrate/2),
		"-flush_packets", "1",
		"-f", "h264", "-",
	)

	cmd := exec.Command(ffmpegPath(), args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("start ffmpeg: %w", err)
	}

	h := &winHelper{
		cmd:    cmd,
		codec:  "h264",
		sig:    make(chan struct{}, 1),
		dead:   make(chan struct{}),
		stderr: &stderr,
	}

	// The framed pipe the consumer reads: the splitter turns ffmpeg's raw
	// Annex-B stream into [len][flag][AU] messages and writes them here;
	// readLoop reads them exactly as it does the macOS helper's stdout.
	pr, pw := io.Pipe()
	stop := make(chan struct{})
	var stopOnce sync.Once
	closeStop := func() { stopOnce.Do(func() { close(stop) }) }

	h.stopFn = func() {
		closeStop()             // tell the capture goroutine to stop
		_ = stdin.Close()       // EOF to ffmpeg → it flushes and exits
		if cmd.Process != nil { // backstop if it doesn't
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
		_ = pw.CloseWithError(io.EOF) // unblock readLoop → close(dead)
	}
	// ffmpeg gets no on-demand IDR through a raw-pixel pipe; the 1s keyint above
	// is the recovery bound instead, so a keyframe request is a no-op here.
	h.keyFn = func() {}

	// Capture goroutine: paced grabs into ffmpeg's stdin. The first frame is
	// already in hand, so send it immediately (a viewer sees a picture without
	// waiting a whole frame interval).
	go ffmpegCaptureLoop(rc, w, tw, th, fps, first, stdin, stop)

	// Splitter goroutine: ffmpeg's Annex-B stdout → framed AUs on the pipe.
	go func() {
		err := splitAnnexBToFramed(stdout, pw)
		if err == nil {
			err = io.EOF
		}
		_ = pw.CloseWithError(err) // readLoop ends; dead closes
	}()

	go h.readLoop(pr)

	// Same early-death guard as startWinHelper: a bad encoder or a missing
	// libx264 dies within a few hundred ms, and the stream must fall back to
	// JPEG rather than show a frozen pane.
	select {
	case <-h.dead:
		closeStop()
		_ = cmd.Wait()
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = "ffmpeg exited immediately"
		}
		return nil, errors.New(msg)
	case <-time.After(helperStartupGrace):
		return h, nil
	}
}

// ffmpegCaptureLoop feeds RGBA frames to ffmpeg's stdin at up to fps, never
// faster. Capture is the bottleneck on the X11/GDI path (a grab can cost >60ms),
// so this paces to a ceiling and simply runs slower when a grab is slow — the
// encoder handles a variable real rate. It ends (closing stdin so ffmpeg exits)
// on stop, a write failure (ffmpeg gone), or a persistent capture error.
func ffmpegCaptureLoop(rc rawCapturer, w winInfo, tw, th, fps int, first []byte, stdin io.WriteCloser, stop <-chan struct{}) {
	interval := time.Second / time.Duration(maxInt(fps, 1))
	if _, err := stdin.Write(first); err != nil {
		_ = stdin.Close()
		return
	}
	want := tw * th * 4
	fails := 0
	for {
		start := time.Now()
		select {
		case <-stop:
			_ = stdin.Close()
			return
		default:
		}
		pix, err := rc.captureRaw(w, tw, th)
		if err != nil || len(pix) != want {
			// A few transient misses are normal (a grab racing a repaint);
			// give up only after a run of them, and let ffmpeg keep the last
			// frame on screen meanwhile.
			fails++
			if fails > 30 {
				_ = stdin.Close()
				return
			}
			if sleepOrStop(stop, interval) {
				_ = stdin.Close()
				return
			}
			continue
		}
		fails = 0
		if _, err := stdin.Write(pix); err != nil {
			_ = stdin.Close() // ffmpeg died; splitter will see EOF
			return
		}
		if rem := interval - time.Since(start); rem > 0 {
			if sleepOrStop(stop, rem) {
				_ = stdin.Close()
				return
			}
		}
	}
}

// splitAnnexBToFramed reads ffmpeg's Annex-B H.264 elementary stream and writes
// one [uint32 len][flag][AU] message per access unit — the framing winHelper's
// readLoop expects. An access unit is grouped as the parameter sets / SEI that
// precede a picture plus its VCL slice; it is flushed as soon as the slice's NAL
// completes (the next start code arrives), which is minimal latency for the
// single-slice, no-B-frame stream the encoder is configured to produce. flag is
// flagH264Key when the AU carries an IDR slice or an SPS (a decodable entry
// point), else flagH264Delta.
func splitAnnexBToFramed(r io.Reader, w io.Writer) error {
	br := bufio.NewReaderSize(r, 1<<20)
	var pending []byte // bytes read, not yet cut at a start code
	var au []byte      // current access unit, each NAL prefixed with 00 00 00 01
	auKey := false

	flush := func() error {
		if len(au) == 0 {
			return nil
		}
		if err := writeFramedAU(w, au, auKey); err != nil {
			return err
		}
		au = au[:0]
		auKey = false
		return nil
	}
	// onNAL appends one NAL (payload without start code) to the current AU,
	// flushing a completed AU when a picture slice ends.
	onNAL := func(nal []byte) error {
		if len(nal) == 0 {
			return nil
		}
		t := nal[0] & 0x1f
		au = append(au, 0, 0, 0, 1)
		au = append(au, nal...)
		switch {
		case t == 5: // IDR slice → keyframe
			auKey = true
			return flush()
		case t >= 1 && t <= 4: // non-IDR slice → picture end
			return flush()
		case t == 7: // SPS present → the AU it belongs to is an entry point
			auKey = true
		}
		return nil
	}

	buf := make([]byte, 64*1024)
	for {
		n, rerr := br.Read(buf)
		if n > 0 {
			pending = append(pending, buf[:n]...)
			// Cut every complete NAL: bytes between one start code and the next.
			for {
				sc, scLen := findStartCode(pending, 0)
				if sc < 0 {
					break
				}
				next, _ := findStartCode(pending, sc+scLen)
				if next < 0 {
					// Only a partial NAL so far; keep from this start code on.
					if sc > 0 {
						pending = append(pending[:0], pending[sc:]...)
					}
					break
				}
				nal := trimTrailingZeros(pending[sc+scLen : next])
				if err := onNAL(nal); err != nil {
					return err
				}
				pending = append(pending[:0], pending[next:]...)
			}
		}
		if rerr != nil {
			// Flush the last NAL still buffered, then the final AU.
			if sc, scLen := findStartCode(pending, 0); sc >= 0 {
				if err := onNAL(trimTrailingZeros(pending[sc+scLen:])); err != nil {
					return err
				}
			}
			if err := flush(); err != nil {
				return err
			}
			if rerr == io.EOF {
				return nil
			}
			return rerr
		}
	}
}

// writeFramedAU writes one access unit in the helper framing: a big-endian
// uint32 length covering the flag byte and the AU, then the flag, then the AU.
func writeFramedAU(w io.Writer, au []byte, key bool) error {
	flag := byte(flagH264Delta)
	if key {
		flag = flagH264Key
	}
	var hdr [5]byte
	binary.BigEndian.PutUint32(hdr[:4], uint32(len(au)+1))
	hdr[4] = flag
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(au)
	return err
}

// findStartCode returns the index and length (3 or 4) of the next Annex-B start
// code (00 00 01, or 00 00 00 01) at or after off, or -1.
func findStartCode(b []byte, off int) (idx, length int) {
	for i := off; i+2 < len(b); i++ {
		if b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 {
			if i > off && b[i-1] == 0 {
				return i - 1, 4
			}
			return i, 3
		}
	}
	return -1, 0
}

// trimTrailingZeros drops the run of 0x00 bytes an encoder may pad after a NAL
// (they belong to the next start code, not this NAL's payload).
func trimTrailingZeros(b []byte) []byte {
	n := len(b)
	for n > 0 && b[n-1] == 0 {
		n--
	}
	return b[:n]
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
