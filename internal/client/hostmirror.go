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
// cleared screen and the host renders identically to the viewers; only the
// history ABOVE the screen is spared. A deliberate `Clear-Host` therefore
// clears the host's screen but leaves its scrollback (viewers still get the
// full clear) — the right way round, because reminal must never be the reason
// a terminal's history vanishes.
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

// conhostResizeClear is what the Windows pseudo console emits on a resize
// (microsoft/terminal WriteClearScreen): CUP home, erase display, erase
// scrollback. Longest first so a ReplaceAll of the full shim is not undone
// by a shorter match leaving a remnant.
var conhostResizeClear = [][]byte{
	[]byte("\x1b[H\x1b[2J\x1b[3J"),
	[]byte("\x1b[H\x1b[2J"),
	[]byte("\x1b[2J\x1b[3J"),
	[]byte("\x1b[3J"),
	[]byte("\x1b[2J"),
}

// stripConhostResizeClear removes the resize-clear shim from a PTY chunk so
// it cannot undo resizeAnchoredBottom or wipe the scrollback we just moved
// lines into. Genuine app clears after the post-resize window still land.
func stripConhostResizeClear(p []byte) []byte {
	for _, seq := range conhostResizeClear {
		if len(p) == 0 {
			return p
		}
		p = bytes.ReplaceAll(p, seq, nil)
	}
	return p
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
