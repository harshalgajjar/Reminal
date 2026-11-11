// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build darwin

package client

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Typing used to cost a process per character. Every key injected on macOS ran
// `osascript -e 'tell application "System Events" to keystroke …'`, measured at
// 40-50ms on an idle machine and worse under load — so typing had a ceiling
// near 16 characters a second, and anything faster piled up behind it. What
// that looked like from a phone was letters arriving seconds after they were
// typed while the picture kept updating on time, because frames come from a
// different goroutine and a different process entirely.
//
// The injection was never the cost; starting something was. reminal-capture
// has a `keys` mode that stays running and posts CGEvents, and one round trip
// through it measures ~0.1ms. This is the client for it: one helper, started
// on first use, restarted if it ever stops answering, and a fall back to
// osascript whenever it cannot be used — an old helper beside a new binary, a
// build with no helper at all, or a machine where it will not start.

// keyHelperTimeout bounds one injection. It is generous next to the ~0.1ms a
// round trip takes: the point is to notice a wedged helper, not to race it.
const keyHelperTimeout = 2 * time.Second

// CGEventFlags bits for the modifiers we carry.
const (
	cgFlagShift   = 1 << 17
	cgFlagControl = 1 << 18
	cgFlagOption  = 1 << 19
	cgFlagCommand = 1 << 20
)

var cgModifierBits = map[string]uint64{
	"shift": cgFlagShift, "ctrl": cgFlagControl,
	"alt": cgFlagOption, "cmd": cgFlagCommand,
}

type keyHelper struct {
	mu   sync.Mutex
	cmd  *exec.Cmd
	in   *os.File
	out  *bufio.Reader
	outF *os.File
	dead bool // tried and could not start: stop trying until something changes
}

var keysHelper keyHelper

// start brings the helper up. Caller holds mu.
func (k *keyHelper) start() bool {
	if k.cmd != nil {
		return true
	}
	if k.dead {
		return false
	}
	path, err0 := captureHelperPath()
	if err0 != nil || path == "" {
		k.dead = true
		return false
	}
	inR, inW, err := os.Pipe()
	if err != nil {
		k.dead = true
		return false
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		k.dead = true
		return false
	}
	cmd := helperCommand(path, "keys")
	cmd.Stdin = inR
	cmd.Stdout = outW
	if err := cmd.Start(); err != nil {
		inR.Close()
		inW.Close()
		outR.Close()
		outW.Close()
		// An older helper has no `keys` mode. It will fail the same way every
		// time, so stop asking and leave osascript to it.
		k.dead = true
		return false
	}
	inR.Close()
	outW.Close()
	k.cmd, k.in, k.outF, k.out = cmd, inW, outR, bufio.NewReader(outR)
	return true
}

// stop tears the helper down so the next call starts a fresh one. Caller holds
// mu. Not marked dead: a helper that died once may well start again, and the
// alternative is a session typing through osascript for the rest of its life.
func (k *keyHelper) stop() {
	if k.cmd == nil {
		return
	}
	if k.in != nil {
		k.in.Close()
	}
	if k.cmd.Process != nil {
		_ = k.cmd.Process.Kill()
	}
	_ = k.cmd.Wait()
	if k.outF != nil {
		k.outF.Close()
	}
	k.cmd, k.in, k.out, k.outF = nil, nil, nil, nil
}

// send injects one event. Reports false when the helper could not do it, which
// means the caller falls back rather than dropping the keystroke.
func (k *keyHelper) send(ev map[string]any) bool {
	line, err := json.Marshal(ev)
	if err != nil {
		return false
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if !k.start() {
		return false
	}
	_ = k.in.SetWriteDeadline(time.Now().Add(keyHelperTimeout))
	if _, err := k.in.Write(append(line, '\n')); err != nil {
		k.stop()
		return false
	}
	_ = k.outF.SetReadDeadline(time.Now().Add(keyHelperTimeout))
	reply, err := k.out.ReadString('\n')
	if err != nil {
		k.stop()
		return false
	}
	return strings.TrimSpace(reply) == "ok"
}

// keyHelperType types text. False means fall back.
func keyHelperType(text string) bool {
	if text == "" {
		return true
	}
	return keysHelper.send(map[string]any{"t": "text", "s": text})
}

// keyHelperKey presses one key by macOS virtual keycode, with modifiers.
func keyHelperKey(code int, mods []string) bool {
	var flags uint64
	for _, m := range mods {
		flags |= cgModifierBits[m]
	}
	return keysHelper.send(map[string]any{"t": "key", "code": code, "flags": flags})
}

// keyHelperClick clicks at a screen point: what clickN used to start an
// osascript for, at every click.
func keyHelperClick(x, y int, right bool, count int) bool {
	return keysHelper.send(map[string]any{"t": "click", "x": x, "y": y, "right": right, "count": count})
}

// keyHelperScroll scrolls at a screen point by wheel deltas (CGEvent sign).
func keyHelperScroll(x, y, dy, dx int, phase string) bool {
	m := map[string]any{"t": "scroll", "x": x, "y": y, "dy": dy, "dx": dx}
	if phase != "" {
		m["phase"] = phase // an older helper ignores the key
	}
	return keysHelper.send(m)
}

// keyHelperDrag posts one step of a live drag: down, move or up.
func keyHelperDrag(phase string, x, y int) bool {
	return keysHelper.send(map[string]any{"t": "drag", "phase": phase, "x": x, "y": y})
}
