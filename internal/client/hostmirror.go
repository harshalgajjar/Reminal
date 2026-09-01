// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import "bytes"

// hostFiltered are the sequences the host terminal must never receive. Each is
// something the pseudo console emits on the user's behalf without the user (or
// the app they ran) ever asking for it — see hostMirror.
var hostFiltered = [][]byte{
	// ED 3 — "erase the saved lines". Deletes the terminal's scrollback:
	// everything above the visible screen, gone.
	[]byte("\x1b[3J"),
	// Win32 input mode. Turns every keystroke into an INPUT_RECORD escape
	// sequence rather than ordinary text.
	[]byte("\x1b[?9001h"),
	// Focus event reporting. Makes the terminal emit ESC[I / ESC[O whenever
	// the window gains or loses focus.
	[]byte("\x1b[?1004h"),
}

// hostMirror filters the PTY stream on its way to the HOST terminal only (the
// bytes viewers and the snapshot emulator see are never touched — they must
// reflect exactly what the app did). Windows only; a Unix pty emits none of
// this on its own.
//
// A pseudo console does not merely relay its shell: it also negotiates with
// whatever terminal it thinks it is talking to, and reminal is sitting in the
// middle of that conversation with a real terminal — the user's — on the far
// side. Two kinds of damage come out of it.
//
// Clearing. PowerShell blanks its whole buffer through the Win32 fill API on a
// resize, and conhost's PowerShell shim turns that into "ESC[H ESC[2J ESC[3J"
// (microsoft/terminal, src/host/_stream.cpp: WriteClearScreen). Mirrored
// verbatim, the ED 3 deletes the scrollback of the terminal the user ran
// `reminal` in. ED 2 still passes, so the shell's repaint lands on a properly
// cleared screen. A deliberate `Clear-Host` therefore clears the screen
// (ED 2) and is not allowed to delete reminal's history (ED 3) — the host
// path already refused that, and the recorded stream (emulator + viewers)
// matches. ConPTY has no scrollback of its own (viewport == buffer); CSI 3 J
// is the cls shim asking the connected terminal to forget a buffer ConPTY
// does not keep. Reminal's scrollback is the session log, like Unix `clear`
// (ED 2, no ED 3). We must never be the reason that history vanishes.
//
// Input modes. At start-up the pseudo console asks its terminal for win32 input
// mode and focus events (VtIo::StartIfNeeded). Those requests are meant for a
// terminal the console will hand back cleanly when it exits — but ours is the
// user's shell prompt, and reminal does not always get to exit cleanly:
// `reminal kill` is a TerminateProcess on Windows, and no defer survives that.
// The modes outlive the session, and the terminal keeps encoding every key as
// an escape sequence — "a bunch of gibberish characters for every key I press",
// unfixable from inside that shell because only the terminal can undo it.
// Never enabling them is the only guarantee. Nothing is lost: a console whose
// terminal ignores the request falls back to ordinary VT input, which is what
// remote viewers have always sent anyway.
type hostMirror struct {
	// pending holds a trailing partial match (a sequence split across two
	// reads) until the next chunk either completes it or proves it wasn't one.
	pending []byte
}

// forward returns the bytes to write to the host terminal for this chunk.
// The input slice is never modified — the caller still records it verbatim.
func (m *hostMirror) forward(p []byte) []byte {
	if !filterHostMirror {
		return p
	}
	return m.stripSequences(p)
}

// stripSequences removes every hostFiltered sequence from p, carrying a split
// sequence across chunk boundaries. Split out from forward so the platform gate
// and the stream logic can be tested independently, on any OS.
func (m *hostMirror) stripSequences(p []byte) []byte {
	if len(m.pending) > 0 {
		joined := make([]byte, 0, len(m.pending)+len(p))
		joined = append(joined, m.pending...)
		joined = append(joined, p...)
		m.pending = nil
		p = joined
	}
	var out []byte
	stripped := false
	for {
		i, n := firstFiltered(p)
		if i < 0 {
			break
		}
		out = append(out, p[:i]...)
		p = p[i+n:]
		stripped = true
	}
	if n := maxTrailingPrefix(p); n > 0 {
		m.pending = append(m.pending, p[len(p)-n:]...)
		p = p[:len(p)-n]
	}
	if !stripped {
		return p
	}
	return append(out, p...)
}

// firstFiltered returns the offset and length of the earliest filtered sequence
// in p, or -1. Earliest-wins matters: the sequences share a prefix, so scanning
// them in list order could otherwise strip a later match first and leave an
// earlier one behind.
func firstFiltered(p []byte) (offset, length int) {
	offset, length = -1, 0
	for _, seq := range hostFiltered {
		if i := bytes.Index(p, seq); i >= 0 && (offset < 0 || i < offset) {
			offset, length = i, len(seq)
		}
	}
	return offset, length
}

// maxTrailingPrefix returns how many bytes at the end of p form a proper prefix
// of some filtered sequence — the tail to hold back in case the next read
// completes it. The longest candidate wins, so a tail that could still become
// the longest sequence is never released early.
func maxTrailingPrefix(p []byte) int {
	best := 0
	for _, seq := range hostFiltered {
		if n := trailingPrefixLen(p, seq); n > best {
			best = n
		}
	}
	return best
}

// conhostResizeClear is WriteClearScreen (microsoft/terminal _stream.cpp):
// CUP home, erase display, erase scrollback. Longest first so firstSeq
// prefers the full production over a shorter remnant at the same offset.
var conhostResizeClear = [][]byte{
	[]byte("\x1b[H\x1b[2J\x1b[3J"),
	[]byte("\x1b[H\x1b[2J"),
	[]byte("\x1b[2J\x1b[3J"),
	[]byte("\x1b[3J"),
	[]byte("\x1b[2J"),
}

var ed3Only = [][]byte{[]byte("\x1b[3J")}

// conhostClearFilter is the recorded-stream (emulator + viewers) half of
// hostMirror. Two invariants, neither a timer nor a shim count:
//
//  1. CSI 3 J is never reminal history. ConPTY's buffer is the viewport;
//     the cls shim emits ED 3 so the *connected terminal* forgets a
//     scrollback ConPTY does not have. The host path already drops it.
//     The session log matches. Clear-Host still ED 2s the screen.
//
//  2. After OUR ResizePseudoConsole, WriteClearScreen (CUP+ED2, and the
//     ED3 already covered by (1)) is ConPTY translating a whole-buffer
//     space fill — the same opcode, possibly emitted once per axis of
//     the size change. It is not application output. Armed by Resize,
//     disarmed by the next non-empty bytes that are not that opcode.
//     Leftover app bytes already in the pipe stay armed: the opcode
//     has not arrived yet.
//
// Matching is streaming (pending prefix) because ConPTY splits the
// production across Read calls. A chunk-atomic ReplaceAll lets CUP
// home leak, then ED2 wipe the screen resizeAnchoredBottom just laid
// out.
type conhostClearFilter struct {
	pending  []byte
	draining bool
	gotShim  bool
	// forceED3 is for tests on non-Windows builders. Production uses
	// filterHostMirror.
	forceED3 bool
}

func (f *conhostClearFilter) dropED3() bool {
	return f.forceED3 || filterHostMirror
}

func (f *conhostClearFilter) arm() {
	f.draining = true
	f.gotShim = false
}

func (f *conhostClearFilter) reset() {
	f.pending = nil
	f.draining = false
	f.gotShim = false
	f.forceED3 = false
}

func (f *conhostClearFilter) seqs() [][]byte {
	if f.draining {
		return conhostResizeClear
	}
	if f.dropED3() {
		return ed3Only
	}
	return nil
}

// feed returns the bytes of p that belong in the session log. The input
// slice is never modified.
func (f *conhostClearFilter) feed(p []byte) []byte {
	seqs := f.seqs()
	if len(seqs) == 0 && len(f.pending) == 0 {
		return p
	}
	if len(f.pending) > 0 {
		joined := make([]byte, 0, len(f.pending)+len(p))
		joined = append(joined, f.pending...)
		joined = append(joined, p...)
		f.pending = nil
		p = joined
	}
	if len(seqs) == 0 {
		return p
	}
	p = f.takeSeqs(p, seqs)
	if f.draining && f.gotShim && len(f.pending) == 0 && len(p) > 0 {
		f.draining = false
		f.gotShim = false
	}
	// After a Resize echo ends, leftover CSI 3 J is still not reminal
	// history (Clear-Host's ED 2 stays).
	if f.dropED3() && !f.draining && len(f.pending) == 0 {
		p = f.takeSeqs(p, ed3Only)
	}
	return p
}

// takeSeqs removes seqs from p, carrying a split production in pending.
// While draining, consecutive copies of WriteClearScreen are one opcode
// (ConPTY may emit it once per axis). A gap of other bytes means the
// opcode ended; a later clear is the application's.
func (f *conhostClearFilter) takeSeqs(p []byte, seqs [][]byte) []byte {
	if len(seqs) == 0 {
		return p
	}
	var out []byte
	stripped := false
	for {
		i, n := firstSeq(p, seqs)
		if i < 0 {
			break
		}
		if f.draining && f.gotShim && i > 0 {
			break
		}
		if seqStillGrowing(p[i:], seqs) {
			out = append(out, p[:i]...)
			p = p[i:]
			break
		}
		out = append(out, p[:i]...)
		p = p[i+n:]
		if f.draining {
			f.gotShim = true
		}
		stripped = true
	}
	if n := maxSeqHoldBack(p, seqs); n > 0 {
		f.pending = append([]byte{}, p[len(p)-n:]...)
		p = p[:len(p)-n]
	}
	if !stripped && len(out) == 0 {
		return p
	}
	return append(out, p...)
}

// firstSeq returns the offset and length of the earliest seq in p,
// longest at that offset. -1 if none.
func firstSeq(p []byte, seqs [][]byte) (offset, length int) {
	offset, length = -1, 0
	for _, seq := range seqs {
		i := bytes.Index(p, seq)
		if i < 0 {
			continue
		}
		if offset < 0 || i < offset || (i == offset && len(seq) > length) {
			offset, length = i, len(seq)
		}
	}
	return offset, length
}

func seqStillGrowing(from []byte, seqs [][]byte) bool {
	for _, seq := range seqs {
		if len(seq) > len(from) && bytes.HasPrefix(seq, from) {
			return true
		}
	}
	return false
}

func maxSeqHoldBack(p []byte, seqs [][]byte) int {
	best := 0
	for _, seq := range seqs {
		if n := trailingPrefixLen(p, seq); n > best {
			best = n
		}
		if bytes.HasSuffix(p, seq) && seqStillGrowing(seq, seqs) && len(seq) > best {
			best = len(seq)
		}
	}
	return best
}

// trailingPrefixLen returns the length of the longest proper prefix of seq that
// p ends with (0 if none).
func trailingPrefixLen(p, seq []byte) int {
	n := len(seq) - 1
	if n > len(p) {
		n = len(p)
	}
	for ; n > 0; n-- {
		if bytes.HasSuffix(p, seq[:n]) {
			return n
		}
	}
	return 0
}
