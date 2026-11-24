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

	"reminal/internal/protocol"
	"reminal/internal/updater"
)

// handleHostInfo replies to a viewer's TypeHostInfo request with the machine's
// current stats, E2E-encrypted (same envelope as the window/app messages).
//
// The PIN rides only on a reply to a request that asks for it ({"pin":true},
// under the session key): the Share menu of a viewer that connected as an
// owner, which never typed it. Never on every reply nor on a push, where an
// owner's browser would come to hold it — and keep it after the device was
// revoked.
func (a *Agent) handleHostInfo(conn *websocket.Conn, data string) {
	wantPIN := false
	if data != "" && a.box != nil {
		if pt, err := a.box.Decrypt(data); err == nil {
			var req struct {
				PIN bool `json:"pin"`
			}
			_ = json.Unmarshal(pt, &req)
			wantPIN = req.PIN
		}
	}
	if msg, ok := a.hostInfoMsg(wantPIN); ok {
		_ = a.writeMsg(conn, msg)
	}
}

// pushHostInfo sends an UNSOLICITED host_info to the connected viewer, used to
// surface a state change (right now, an attention transition) the instant it
// happens instead of waiting for the viewer's next poll. No-op when nobody's
// watching. Runs on its own goroutine at the call site — gatherHostInfo samples
// the machine, which we don't want on the detector's tick.
func (a *Agent) pushHostInfo() {
	a.currentConnMu.Lock()
	conn := a.currentConn
	a.currentConnMu.Unlock()
	if conn == nil {
		return
	}
	if msg, ok := a.hostInfoMsg(false); ok {
		_ = a.writeMsg(conn, msg)
	}
}

// hostInfoMsg builds the encrypted host_info message shared by the request
// reply and the live push, so the two can't drift. ok is false when there's no
// session key yet (nothing to encrypt under) or serialization fails.
func (a *Agent) hostInfoMsg(withPIN bool) (protocol.Message, bool) {
	if a.box == nil {
		return protocol.Message{}, false
	}
	info := gatherHostInfo()
	info.Version = a.version
	info.Update = updater.Available(a.version)
	info.Sessions = countRestartableSessions()
	a.metaMu.Lock()
	info.Attn = a.attnState
	info.Name = a.name
	info.Voice = true
	a.metaMu.Unlock()
	// Owners connect PIN-free, so the browser never typed the PIN. The share
	// menu still needs it to mint a Join link / `reminal connect` line, and
	// asks for it (handleHostInfo); the viewer holds it in memory for that
	// and never stores it. It rides the session channel, encrypted — anyone
	// who can read it already has the session key. The directory channel
	// still omits PINs (see protocol.DirSession).
	if withPIN {
		info.PIN = a.pin
	}
	raw, err := json.Marshal(info)
	if err != nil {
		return protocol.Message{}, false
	}
	enc, err := a.box.Encrypt(raw)
	if err != nil {
		return protocol.Message{}, false
	}
	return protocol.Message{Type: protocol.TypeHostInfo, Data: enc}, true
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
	if ok, wait := a.takeDir(dirActSpawn); !ok {
		a.dirBusy(conn, protocol.TypeNewSession, data, wait)
		return
	}
	// The request must decrypt under the session key: a message the relay
	// could have forged (empty, or sealed under some other key) must not fork
	// a shell on this machine.
	var req struct {
		Name  string `json:"name"`
		Cwd   string `json:"cwd"`
		ReqID string `json:"req_id"`
	}
	if data == "" {
		return
	}
	pt, err := a.box.Decrypt(data)
	if err != nil {
		return
	}
	_ = json.Unmarshal(pt, &req)
	var payload struct {
		ReqID string `json:"req_id,omitempty"`
		ID    string `json:"id,omitempty"`
		PIN   string `json:"pin,omitempty"`
		Error string `json:"error,omitempty"`
	}
	payload.ReqID = req.ReqID
	if sp, err := a.spawnSession(req.Name, req.Cwd); err != nil {
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
	Hostname string `json:"hostname"`
	// The machine's vitals — see protocol.MachineStats. Embedded so the wire
	// shape is unchanged (fields stay flat) and there is exactly one list of
	// them shared with the directory reply.
	protocol.MachineStats
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
	// Sessions is how many shells on this machine an upgrade would restart.
	// The panel promises a number before you press the button, and guessing
	// one would be worse than omitting it — an old host sends nothing and the
	// wording falls back to "every session".
	Sessions int `json:"sessions,omitempty"`
	// Attn is THIS session's current attention state (working / input / done),
	// same values as protocol.DirSession.Attn. It rides host_info so a connected
	// viewer learns the state of the session it's watching the instant it
	// changes — the agent pushes a host_info on every transition — instead of
	// waiting for the fleet directory's slower poll. Empty for a bare shell —
	// and deliberately NOT omitempty: the viewer must be told when a session
	// goes back to idle, or its pill sticks on the last non-idle state forever.
	// (That was a real bug: `omitempty` swallowed the idle transition.)
	Attn string `json:"attn"`
	// Name is the session's own name, as `reminal list` shows it ("" when it
	// has none): what the viewer calls the agent here, in voice's
	// conversation, rather than a generic "Agent".
	Name string `json:"name,omitempty"`
	// Voice says this reminal takes voice (speech.go: speech_in typed in, the
	// speak tool heard): the viewer offers its mic only where it is set. An
	// older reminal leaves it out, and the viewer says voice needs a newer
	// one there. (The other way round needs nothing: an older viewer has no
	// mic, so it never sends speech_in.)
	Voice bool `json:"voice,omitempty"`
	// Battery* are the machine's power state, as the directory reply carries
	// them (same names, same meaning), so the Host panel shows the battery the
	// machines list does. A machine with no battery, and an older host, send
	// none of them, and the panel shows no battery row.
	BatteryPct   *int   `json:"battery_pct,omitempty"`
	BatteryState string `json:"battery_state,omitempty"`
	BatteryMins  int    `json:"battery_mins,omitempty"`
}

// gatherHostInfo collects the cross-platform basics, then lets the per-OS hook
// fill in memory / uptime / load / CPU model. Never errors — missing fields are
// simply left zero.
func gatherHostInfo() HostInfo {
	h := HostInfo{
		MachineStats: protocol.MachineStats{
			OS:        friendlyOS(runtime.GOOS),
			Arch:      runtime.GOARCH,
			CPUs:      runtime.NumCPU(),
			Channel:   updater.ChannelName(),
			UpdatedBy: updater.UpdatedBy(),
		},
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
	if b := CurrentBattery(); b != nil && b.Pct != nil {
		pct := *b.Pct
		h.BatteryPct, h.BatteryState, h.BatteryMins = &pct, b.State, b.Mins
	}
	return h
}

// CPU utilization is read by a platform-specific cpuPercent() (cpustat_*.go).
// On Linux it diffs /proc/stat between calls and on Windows GetSystemTimes:
// one cheap call. On macOS reading it means running a program, so every
// reminal process reads one value that a single sampler on the machine keeps
// (machineshare.go): a session never runs one of its own. cachedCPUPercent
// memoises the result briefly so a burst of host_info polls (one per viewer,
// ~every 1.5s) is one reading, and keeps the last good value while there is
// no fresh one.
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

// PrimeCPUSample takes a throwaway CPU reading so the NEXT one has an interval
// to measure against. On Linux cpuPercent needs two /proc/stat samples — the
// first only establishes a baseline and reports "unknown" — so a one-shot
// process that wants a CPU figure (e.g. `reminal machines` reading the LOCAL
// line via LocalDirectory) must prime, wait briefly, then read. The long-lived
// daemon already holds a prior sample, so it never needs this; and it is a cheap
// no-op on macOS/Windows, whose samplers report a value on the first call.
func PrimeCPUSample() { _, _ = cachedCPUPercent() }

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
