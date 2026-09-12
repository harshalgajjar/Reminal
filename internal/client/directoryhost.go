// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/reminal/reminal/internal/proc"
	"github.com/reminal/reminal/internal/protocol"
	"github.com/reminal/reminal/internal/session"
)

// The directory host is a machine's presence responder for `reminal machines`.
// It registers the machine's owner-derived directory channel on the relay and,
// once an owner proves ownership there with the same own_init/own_resp handshake
// used for a PIN-free connect, answers "what sessions are you running?" with an
// end-to-end-encrypted list — so the relay never learns what's running, and only
// an enrolled owner can read the reply.
//
// Every agent runs one host goroutine, but exactly one serves per machine: a
// machine-local flock (see dirhostlock.go) elects the single host, and the
// others stand by and take over promptly when it exits. We do NOT lean on the
// relay's own election, because it differs by relay — the production relay
// supersedes a same-credential agent while the local relay rejects it, so
// without the lock sibling sessions would ping-pong the channel and the machine
// would flap online/offline. Whichever session holds the lock answers from the
// shared local session registry (the same one `reminal list` reads), so it
// doesn't matter which won. A machine with no owners never opens the channel.

const (
	// With single-host locking there's no sibling to fight, so a dropped
	// connection is always a genuine network blip — reclaim fast. Cap the backoff
	// low (not tens of seconds): after a wake-from-sleep or a network flap we want
	// the machine's presence back within seconds, so the phone stops showing it
	// gray. A failed dial is a cheap TCP attempt, so polling every few seconds
	// while the network is still down costs little.
	dirHostRetry    = 2 * time.Second
	dirHostRetryMax = 8 * time.Second
	// How often a standing-by agent re-checks to take over the lock, and how
	// often an unowned machine re-checks for newly-enrolled owners.
	dirHostLockPoll     = 3 * time.Second
	dirHostOwnerRecheck = 60 * time.Second
)

// runDirectoryHost keeps this machine's directory channel served for as long as
// stop is open. Safe to run on every agent; a no-op while unowned. isDaemon is
// true only for the standalone background-host daemon (see RunDaemon); false for
// session-embedded hosts.
func runDirectoryHost(stop <-chan struct{}, isDaemon bool, version string) {
	for {
		if stopped(stop) {
			return
		}
		if !isDaemon && DaemonServiceInstalled() {
			if !daemonAlive() {
				if exe, err := os.Executable(); err == nil {
					respawnDaemonAfterUpgrade(exe)
				}
			}
			if sleepOrStop(stop, dirHostOwnerRecheck) {
				return
			}
			continue
		}
		if of, err := loadOwners(); noOwners(of, err) {
			if sleepOrStop(stop, dirHostOwnerRecheck) {
				return
			}
			continue
		}
		lock, ok := tryLockDirHost()
		if !ok {
			if sleepOrStop(stop, dirHostLockPoll) {
				return
			}
			continue
		}
		serveDirectoryLocked(stop, isDaemon, version)
		unlockDirHost(lock)
	}
}

// serveDirectoryLocked runs the machine channel — a machine-mode Agent, see
// machineagent.go — for as long as this process should be the one serving it:
// until stop closes, every owner is revoked, or (for a session-embedded host) a
// daemon appears to take over. The Agent owns its relay reconnects and backoff.
func serveDirectoryLocked(stop <-chan struct{}, isDaemon bool, version string) {
	// Snapshot the owner set BEFORE minting the channel's key, and refuse to
	// serve without a baseline. A revoked device can't do a NEW handshake
	// (handleOwnerInit re-checks IsOwner), but one that already handshook keeps
	// a working wrapped key for the life of this agent — and the daemon never
	// exits — so the re-key-on-change check below is what bounds that window.
	// A blank baseline (a transient read error) must not silently disable it,
	// so treat a failed load as "retry" rather than serving unprotected. The
	// outer loop only reaches here after its own successful load, so this is
	// rarely hit.
	of0, err := loadOwners()
	if noOwners(of0, err) {
		_ = sleepOrStop(stop, dirHostRetry)
		return
	}
	startFP := ownersFingerprint(of0)
	agent, err := NewAgentWith(version, AgentOptions{Machine: true, DaemonHost: isDaemon})
	if err != nil {
		_ = sleepOrStop(stop, dirHostRetry)
		return
	}
	inner := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = agent.RunMachine(inner)
	}()
	defer func() { close(inner); <-done }()
	for {
		if sleepOrStop(stop, dirHostOwnerRecheck) {
			return
		}
		if !isDaemon && DaemonServiceInstalled() {
			return
		}
		of, err := loadOwners()
		if noOwners(of, err) {
			return // every owner revoked — stop hosting and release the lock
		}
		if ownersFingerprint(of) != startFP {
			return // owner set changed (add/revoke) — rebuild to re-key
		}
	}
}

// noOwners reports whether a loadOwners() result means there is no enrolled
// owner to serve — a read error, a missing store, or an empty list. The
// directory host never opens the channel for such a machine.
func noOwners(of *ownersFile, err error) bool {
	return err != nil || of == nil || len(of.Owners) == 0
}

// ownersFingerprint is a stable digest of the owner set that changes whenever an
// owner is added or revoked (order-independent). serveDirectoryLocked uses it to
// detect a change and re-key the machine channel; see there.
func ownersFingerprint(of *ownersFile) string {
	if of == nil {
		return ""
	}
	pubs := make([]string, 0, len(of.Owners))
	for _, o := range of.Owners {
		pubs = append(pubs, o.Pubkey)
	}
	sort.Strings(pubs)
	return strings.Join(pubs, "\n")
}

// tokenBucket is a leaky-bucket limiter: burst up to max, refilling perSec.
type tokenBucket struct {
	mu     sync.Mutex
	tokens float64
	max    float64
	perSec float64
	last   time.Time
}

func newTokenBucket(max, perSec float64) *tokenBucket {
	return &tokenBucket{tokens: max, max: max, perSec: perSec, last: time.Now()}
}

func (tb *tokenBucket) allow(now time.Time) bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	tb.tokens += now.Sub(tb.last).Seconds() * tb.perSec
	if tb.tokens > tb.max {
		tb.tokens = tb.max
	}
	tb.last = now
	if tb.tokens < 1 {
		return false
	}
	tb.tokens--
	return true
}

func killLocalSession(id string) error {
	id = strings.ToUpper(strings.TrimSpace(id))
	all, err := session.ReadAllActive()
	if err != nil {
		return err
	}
	for _, a := range all {
		if a.ID != id {
			continue
		}
		pid := a.PID
		if err := proc.Terminate(pid); err != nil {
			if errors.Is(err, proc.ErrGone) {
				_ = session.ClearActive(a.ID) // already gone
				return nil
			}
			return fmt.Errorf("terminate %d: %w", pid, err)
		}
		// Drop the registry entry immediately so `reminal list` and the Machines
		// panel stop showing it the moment the kill is acknowledged — not after
		// the process finishes dying (which left a killed session lingering in
		// the list until the next refresh). The goroutine below still escalates
		// to SIGKILL if the shell ignores SIGTERM.
		_ = session.ClearActive(a.ID)
		go func(pid int) {
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				if !proc.Alive(pid) {
					return
				}
				time.Sleep(100 * time.Millisecond)
			}
			if proc.Alive(pid) {
				_ = proc.Kill(pid)
			}
		}(pid)
		return nil
	}
	return fmt.Errorf("session not found")
}

// renameLocalSession finds a live shell session by id and asks its agent to
// rename itself via the control socket.
func renameLocalSession(id, name string) error {
	id = strings.ToUpper(strings.TrimSpace(id))
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("name is empty")
	}
	all, err := session.ReadAllActive()
	if err != nil {
		return err
	}
	for _, a := range all {
		if a.ID == id {
			if a.IsPort() {
				return fmt.Errorf("can't rename a port forward")
			}
			_, err := sendControlTo(a.PID, "rename "+name)
			return err
		}
	}
	return fmt.Errorf("session not found")
}

// localSessions projects the local session registry to the wire DTO, dropping
// the PIN (owners reach sessions without it) and anything sensitive.
// LocalDirectory returns THIS machine's directory response — hostname + live
// sessions — read straight from the local registry, with no relay round-trip
// and no ownership handshake. `reminal machines` uses it for the machine it's
// running on: you always own the machine you're sitting at, so it should show up
// instantly and never depend on being enrolled as an owner of yourself.
func LocalDirectory() protocol.DirResponse {
	resp := protocol.DirResponse{Sessions: localDirSessions()}
	if host, err := os.Hostname(); err == nil {
		resp.Hostname = host
	}
	fillBattery(&resp)
	// The machine's vitals, from the same sampler the Host panel reads, so a
	// card on the homepage and the panel never disagree about a machine.
	// Version and update are the caller's to add: this path does not know
	// which binary is answering.
	stats := gatherHostInfo().MachineStats
	resp.Stats = &stats
	return resp
}

// fillBattery attaches this machine's power state to a directory reply, and
// leaves every field zero on a machine that has no battery. Shared by the
// local path above and the remote one an owner queries, so the two can't drift.
func fillBattery(resp *protocol.DirResponse) {
	b := CurrentBattery()
	if b == nil || b.Pct == nil {
		return
	}
	pct := *b.Pct
	resp.BatteryPct = &pct
	resp.BatteryState = b.State
	resp.BatteryMins = b.Mins
}

// localDirSessions is the shared projection used by both the directory host (for
// remote owners) and LocalDirectory (for the local CLI).
func localDirSessions() []protocol.DirSession {
	all, err := session.ReadAllActive()
	if err != nil {
		return nil
	}
	now := time.Now()
	out := make([]protocol.DirSession, 0, len(all))
	for _, a := range all {
		ds := protocol.DirSession{
			ID:       a.ID,
			Name:     a.Name,
			Cwd:      a.Cwd,
			Title:    a.Title,
			Kind:     a.Kind,
			Port:     a.Port,
			Headless: a.Headless,
			Viewers:  a.Viewers,
			Attn:     a.Attn,
		}
		if la := a.LastActive(); !la.IsZero() {
			if secs := int64(now.Sub(la).Seconds()); secs > 0 {
				ds.IdleSecs = secs
			}
		}
		out = append(out, ds)
	}
	return out
}

func stopped(stop <-chan struct{}) bool {
	select {
	case <-stop:
		return true
	default:
		return false
	}
}

// sleepOrStop waits for d or until stop fires; returns true if stop fired.
func sleepOrStop(stop <-chan struct{}, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-stop:
		return true
	case <-t.C:
		return false
	}
}
