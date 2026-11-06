// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"reminal/internal/proc"
	"reminal/internal/protocol"
	"reminal/internal/session"
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
		// A daemon runs here, no service manager behind it: it serves the
		// machine, or is about to (re-exec'd in place, the same pid). A
		// session never takes the channel from under it.
		if !isDaemon && daemonRunning() {
			if sleepOrStop(stop, dirHostLockPoll) {
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
	startFP := effectiveOwnersFingerprint(of0)
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
		// A daemon here now (installed, or started by hand since): it serves
		// the machine, this session no longer.
		if !isDaemon && (DaemonServiceInstalled() || daemonRunning()) {
			return
		}
		of, err := loadOwners()
		if noOwners(of, err) {
			return // every owner revoked — stop hosting and release the lock
		}
		if effectiveOwnersFingerprint(of) != startFP {
			return // owner set changed (add/revoke/self-revoke) — rebuild to re-key
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

// effectiveOwnersFingerprint is ownersFingerprint of the owners that can
// connect PIN-free right now: owners.json less the devices that have
// self-revoked (revoked_owners.json). A self-revoke changes it as a revoke
// does. An unreadable tombstone store counts every enrolled owner as
// revoked — a change, so whatever compares it re-keys rather than trusts.
func effectiveOwnersFingerprint(of *ownersFile) string {
	if of == nil {
		return ""
	}
	rev, err := loadRevoked()
	if err != nil {
		return "unreadable revoked_owners.json"
	}
	kept := &ownersFile{}
	for _, o := range of.Owners {
		if !rev[o.Pubkey] {
			kept.Owners = append(kept.Owners, o)
		}
	}
	return ownersFingerprint(kept)
}

// ownersChangedSince reports whether either owner store — owners.json or
// the self-revoke tombstones — was written after t, or in the same whole
// second: a filesystem that keeps whole-second times (HFS+) gives a write
// just after t the second t is in, and a write missed is a revoked device
// kept, where a write counted twice is one restart more.
func ownersChangedSince(t time.Time) bool {
	from := t.Truncate(time.Second)
	for _, path := range []func() (string, error){ownersPath, revokedPath} {
		p, err := path()
		if err != nil {
			continue
		}
		if fi, err := os.Stat(p); err == nil && !fi.ModTime().Before(from) {
			return true
		}
	}
	return false
}

// RekeySessions tells every session of this user that the owner set may
// have changed (`reminal owners revoke`, a self-revoke). A session whose
// owner stores were written since it started restarts in place — same PTY,
// same programs — under a new session key and a new PIN, so a revoked
// device's open viewer is cut off and it can come back in neither as an
// owner nor with a PIN it learned; one begun after (a burst of revokes
// already covered by its restart) does nothing.
// Best-effort, a short cap per session; returns the sessions that restart,
// each with a new PIN, as they were before (their old PIN, to tell the new
// one from).
func RekeySessions() []session.Active {
	recs, err := session.ReadAllActive()
	if err != nil {
		return nil
	}
	var out []session.Active
	for _, r := range recs {
		if r.PID <= 0 {
			continue
		}
		if reply, err := sendControlToDeadline(r.PID, "rekey", 2*time.Second); err == nil && reply == "" {
			out = append(out, r) // a plain "ok": restarting with a new PIN
		}
	}
	return out
}

// tokenBucket is a leaky-bucket limiter: burst up to max, refilling perSec.
type tokenBucket struct {
	mu     sync.Mutex
	tokens float64
	max    float64
	perSec float64
	last   time.Time
	// owed is how many callers have been told a time to come back that has
	// not come yet: each one refused is given the next free time, after
	// those already told one, not all the same one (twenty told "62 ms" all
	// came back at once, one was taken, and the rest were refused again,
	// until even the busy allowance ran out). It falls as the tokens they
	// were promised come in, and is never more than max.
	owed float64
}

func newTokenBucket(max, perSec float64) *tokenBucket {
	return &tokenBucket{tokens: max, max: max, perSec: perSec, last: time.Now()}
}

// allow is take for a caller that is not told when to come back: one it
// refuses is owed nothing.
func (tb *tokenBucket) allow(now time.Time) bool {
	ok, _ := tb.take1(now, false)
	return ok
}

// take is allow, and when it refuses, how long until a token is back for
// this caller: what a caller over the allowance is told to wait (retry_ms),
// so it comes back when it will be answered rather than timing out on a
// silence. Callers refused together are told times one token apart, in
// the order they asked.
func (tb *tokenBucket) take(now time.Time) (bool, time.Duration) {
	return tb.take1(now, true)
}

func (tb *tokenBucket) take1(now time.Time, owe bool) (bool, time.Duration) {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	came := now.Sub(tb.last).Seconds() * tb.perSec
	tb.tokens += came
	if tb.tokens > tb.max {
		tb.tokens = tb.max
	}
	tb.owed = max(0, tb.owed-came)
	tb.last = now
	if tb.tokens < 1 {
		if tb.perSec <= 0 {
			return false, dirBusyMaxWait // never refills: asked the longest wait
		}
		wait := min(time.Duration(math.Ceil((1-tb.tokens+tb.owed)/tb.perSec*1000))*time.Millisecond, dirBusyMaxWait)
		// No more owed than the bucket holds: a caller that asks again
		// without waiting is refused each time, and each refusal would push
		// everyone else's time further out (up to dirBusyMaxWait). Past
		// max owed, a refused caller is told the last time given.
		if owe && wait < dirBusyMaxWait && tb.owed < tb.max {
			tb.owed++
		}
		return false, wait
	}
	tb.tokens--
	return true, 0
}

// refund puts a token back, for work that turned out to be an owner's own —
// see refundOwnerHandshake.
func (tb *tokenBucket) refund() {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	if tb.tokens < tb.max {
		tb.tokens++
	}
}

// applyLocalOpen answers a query that named one exposed port: its public link
// and the PIN its gate asks for. Ports only — a shell session's PIN stays out of
// the directory channel, which is the promise protocol.DirSession makes.
//
// A --public tunnel has no gate: its link alone. Its PIN opens nothing, and
// on the link (the viewer puts it in the fragment for the gate) it would
// stay in the forwarded app's own address, for its page to read.
func applyLocalOpen(resp *protocol.DirResponse, sessionID string) {
	if resp == nil {
		return
	}
	id := strings.ToUpper(strings.TrimSpace(sessionID))
	all, err := session.ReadAllActive()
	if err != nil {
		resp.OpenError = "cannot read the session registry"
		return
	}
	for _, a := range all {
		if a.ID != id {
			continue
		}
		if !a.IsPort() {
			resp.OpenError = "session " + id + " is not an exposed port"
			return
		}
		if a.OpenURL == "" {
			resp.OpenError = "session " + id + " has no public link"
			return
		}
		resp.OpenURL = a.OpenURL
		if !a.Public {
			resp.OpenPIN = a.PIN
		}
		return
	}
	resp.OpenError = "no session " + id + " on this machine"
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
		_ = session.ClearRestore(a.ID) // killed on purpose: never to be restored
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
	// Asleep: nothing is running to stop; ending it is forgetting it.
	if session.IsAsleep(id) {
		return ForgetSleeper(id)
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
			Fg:       a.Fg,
			Shells:   a.Shells,
			Agents:   a.Agents,
			Other:    a.Other,
		}
		if !a.FgSince.IsZero() {
			ds.FgSince = a.FgSince.Unix()
		}
		if a.Attn == "limited" && !a.ResetsAt.IsZero() {
			ds.ResetsAt = a.ResetsAt.Unix()
		}
		if la := a.LastActive(); !la.IsZero() {
			if secs := int64(now.Sub(la).Seconds()); secs > 0 {
				ds.IdleSecs = secs
			}
		}
		out = append(out, ds)
	}
	// The sessions asleep, after the running ones: still this machine's, woken
	// from here (sleep.go).
	out = append(out, sleeperDirSessions()...)
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
