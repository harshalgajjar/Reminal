// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"encoding/json"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/reminal/reminal/internal/protocol"
)

// handleHostInfo replies to a viewer's TypeHostInfo request with the machine's
// current stats, E2E-encrypted (same envelope as the window/app messages).
func (a *Agent) handleHostInfo(conn *websocket.Conn) {
	if a.box == nil {
		return
	}
	info := gatherHostInfo()
	// Owners connect PIN-free, so the browser never typed the PIN. The share
	// menu still needs it to mint a Join link / `reminal connect` line. This
	// rides the session channel, encrypted — anyone who can read it already
	// has the session key (PIN join, or owner handshake). The directory
	// channel still omits PINs (see protocol.DirSession).
	info.PIN = a.pin
	raw, err := json.Marshal(info)
	if err != nil {
		return
	}
	enc, err := a.box.Encrypt(raw)
	if err != nil {
		return
	}
	_ = a.writeMsg(conn, protocol.Message{Type: protocol.TypeHostInfo, Data: enc})
}

// handleNewSession spawns a fresh detached headless reminal on this host and
// replies with its credentials so the viewer can connect. Runs the (blocking)
// spawn handshake on its own goroutine — see the dispatch in runReader — so it
// never stalls the shell stream. No privilege escalation: a viewer already has
// full shell access and could run `reminal new` itself.
func (a *Agent) handleNewSession(conn *websocket.Conn, data string) {
	if a.box == nil {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if data != "" {
		if pt, err := a.box.Decrypt(data); err == nil {
			_ = json.Unmarshal(pt, &req)
		}
	}
	var payload struct {
		ID    string `json:"id,omitempty"`
		PIN   string `json:"pin,omitempty"`
		Error string `json:"error,omitempty"`
	}
	if sp, err := Spawn(req.Name); err != nil {
		payload.Error = err.Error()
	} else {
		payload.ID, payload.PIN = sp.ID, sp.PIN
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	enc, err := a.box.Encrypt(raw)
	if err != nil {
		return
	}
	_ = a.writeMsg(conn, protocol.Message{Type: protocol.TypeNewSession, Data: enc})
}

// HostInfo is a small snapshot of the machine the agent runs on, sent to
// viewers (TypeHostInfo) so the web UI can show the computer's name and a few
// basic stats. All the size/time fields are best-effort: a platform that can't
// read one leaves it zero and the viewer just omits it.
type HostInfo struct {
	Hostname string  `json:"hostname"`
	OS       string  `json:"os"`   // friendly: "macOS", "Linux", "Windows"
	Arch     string  `json:"arch"` // arm64, amd64, …
	CPUModel string  `json:"cpu_model,omitempty"`
	CPUs     int     `json:"cpus"`
	MemTotal uint64  `json:"mem_total,omitempty"` // bytes
	MemUsed  uint64  `json:"mem_used,omitempty"`  // bytes
	Uptime   int64   `json:"uptime,omitempty"`    // seconds since boot
	Load1    float64 `json:"load1,omitempty"`
	Load5    float64 `json:"load5,omitempty"`
	Load15   float64 `json:"load15,omitempty"`
	// CPUPercent is real CPU utilization (0..100), the "% busy" Activity
	// Monitor / top show — NOT load/cores. A pointer so the viewer can tell
	// "unknown/unsupported" (nil, e.g. a platform without a sampler, or the
	// very first Linux sample that has no delta yet) from a genuine 0%.
	CPUPercent *float64 `json:"cpu_pct,omitempty"`
	// DragPhases says this host accepts a drag as begin/move/end events while
	// the pointer is still down, instead of one whole path after it lifts.
	// Advertised rather than assumed: a viewer that sent phased events to a
	// host expecting the batched form would press and release once per chunk —
	// a burst of clicks instead of a drag. Absent (old host) → batched.
	DragPhases bool `json:"drag_phases,omitempty"`
	// CapsOnly says a hello marked caps_only is recorded and dropped here
	// rather than answered with a peer connection. A host without this builds
	// a whole PeerConnection and offer for every one — reaped only after
	// rtcHandshakeTimeout — so a viewer must not repeat them at it.
	CapsOnly bool `json:"caps_only,omitempty"`
	// PIN is this session's join PIN. Sent so an owner-connected viewer can
	// share the session; omitted from directory listings on purpose.
	PIN string `json:"pin,omitempty"`
}

// gatherHostInfo collects the cross-platform basics, then lets the per-OS hook
// fill in memory / uptime / load / CPU model. Never errors — missing fields are
// simply left zero.
func gatherHostInfo() HostInfo {
	h := HostInfo{
		OS:   friendlyOS(runtime.GOOS),
		Arch: runtime.GOARCH,
		CPUs: runtime.NumCPU(),
		// Only the macOS daemon injects drags phase by phase so far; the other
		// backends still replay a path, and telling a viewer otherwise would
		// turn every drag there into a stutter of clicks.
		DragPhases: runtime.GOOS == "darwin",
		CapsOnly:   true,
	}
	if name, err := os.Hostname(); err == nil {
		h.Hostname = name
	}
	fillHostInfo(&h) // platform-specific (hostinfo_darwin.go / _linux.go / _other.go)
	if pct, ok := cachedCPUPercent(); ok {
		h.CPUPercent = &pct
	}
	return h
}

// CPU utilization is sampled by a platform-specific cpuPercent() (cpustat_*.go).
// On macOS that shells out to `top` (~200ms); on Linux it diffs /proc/stat
// between calls. cachedCPUPercent memoises the result briefly so a burst of
// host_info polls (one per viewer, ~every 1.5s) doesn't spawn a `top` each —
// and so gatherHostInfo stays cheap. handleHostInfo runs on its own goroutine
// (see the dispatch in runReader) so the occasional refresh never stalls the
// shell stream.
var (
	cpuCacheMu  sync.Mutex
	cpuCachePct float64
	cpuCacheOK  bool
	cpuCacheAt  time.Time
)

const cpuCacheTTL = 1200 * time.Millisecond

func cachedCPUPercent() (float64, bool) {
	cpuCacheMu.Lock()
	defer cpuCacheMu.Unlock()
	if cpuCacheOK && time.Since(cpuCacheAt) < cpuCacheTTL {
		return cpuCachePct, true
	}
	if pct, ok := cpuPercent(); ok {
		cpuCachePct, cpuCacheOK, cpuCacheAt = pct, true, time.Now()
		return pct, true
	}
	// No fresh reading (sampler unsupported, or Linux's first no-delta sample).
	// Fall back to the last good value if we have one, so a single hiccup
	// doesn't blank the meter.
	return cpuCachePct, cpuCacheOK
}

func friendlyOS(goos string) string {
	switch goos {
	case "darwin":
		return "macOS"
	case "linux":
		return "Linux"
	case "windows":
		return "Windows"
	default:
		return goos
	}
}
