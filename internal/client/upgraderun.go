// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"bufio"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/reminal/reminal/internal/protocol"
)

// upgradeRun is the machine's single upgrade, and everyone watching it.
//
// Two viewers pressing the button at the same moment must produce ONE install
// and TWO identical narrations. So the run owns the transcript rather than the
// connection that started it: whoever asks is added as a subscriber and
// immediately replayed every step so far, which also means a viewer that
// arrives late (or reconnects mid-upgrade) sees the whole story rather than
// joining halfway through a progress bar.
type upgradeRun struct {
	mu     sync.Mutex
	active bool
	steps  []upgradeStage    // everything emitted, for replay
	subs   []*websocket.Conn // everyone watching
}

// join registers conn as a watcher and returns the transcript to replay to it,
// plus whether a run is already under way (in which case the caller must not
// start another).
func (r *upgradeRun) join(conn *websocket.Conn) (replay []upgradeStage, alreadyRunning bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	known := false
	for _, c := range r.subs {
		if c == conn {
			known = true
			break
		}
	}
	if !known {
		r.subs = append(r.subs, conn)
	}
	// Replay only a LIVE run. The steps of a finished one are history: handing
	// them to someone starting a fresh upgrade replays the last attempt's
	// outcome — an error they already dealt with, or a "done" for a version
	// they are no longer on — immediately before the story they actually asked
	// for.
	if !r.active {
		return nil, false
	}
	return append([]upgradeStage(nil), r.steps...), true
}

// begin claims the run. False means someone else already has it.
func (r *upgradeRun) begin() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active {
		return false
	}
	r.active = true
	r.steps = nil
	return true
}

// record appends a step and returns the watchers to send it to. The write
// happens outside the lock: a stalled socket must not hold up the upgrade or
// the other watchers.
func (r *upgradeRun) record(s upgradeStage) []*websocket.Conn {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.steps = append(r.steps, s)
	return append([]*websocket.Conn(nil), r.subs...)
}

// end releases the run. Only reached on the paths that do NOT re-exec — on a
// successful upgrade this process is replaced and there is nothing to reset.
func (r *upgradeRun) end() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.active = false
	r.subs = nil
}

// upgradeStage is one step of the flow, streamed to every watcher as it happens.
type upgradeStage struct {
	Stage   string `json:"stage"` // download | verify | install | restart | done
	Pct     int    `json:"pct,omitempty"`
	Detail  string `json:"detail,omitempty"`
	Version string `json:"version,omitempty"`
	Error   string `json:"error,omitempty"`
}

// broadcastUpgrade records a step and sends it to everyone watching on this
// session. The single place a stage reaches a viewer — the driver and the
// follower both go through it, so there is one definition of "tell the
// watchers" rather than a copy per call site.
func (a *Agent) broadcastUpgrade(s upgradeStage) {
	for _, c := range a.upgrade.record(s) {
		a.sendWindowMsg(c, protocol.TypeUpgrade, s)
	}
}

// An upgrade is a property of the MACHINE, not of the session that was asked to
// perform it.
//
// upgradeRun already stops two viewers on the SAME session from racing, but
// every session is its own process with its own upgradeRun, so two viewers on
// two different sessions of one host would each run a full install: two
// downloads, and two processes writing the same reminal.app at the same time.
// That is not a theoretical race — pressing both buttons together produced two
// fetches 89ms apart, with both extractions landing on one bundle.
//
// So the run is claimed machine-wide with an advisory lock, and the winner
// publishes its steps to a file the losers tail. One install, and every viewer
// on every session reads the same narration.
//
// flock is the right primitive: the kernel releases it when the holder dies,
// which matters because the winning process ENDS by replacing itself. There is
// no "unlock" to run after a successful upgrade — syscall.Exec never returns.
// Go opens files O_CLOEXEC, so the lock is dropped by the exec itself.

// upgradeFollowPoll is how often a follower re-reads the transcript. On a LAN
// the whole upgrade can finish in a couple of seconds, so this has to be well
// under a step's lifetime or a follower relays one line and then has its own
// session restarted out from under it.
const upgradeFollowPoll = 120 * time.Millisecond

// upgradeFlushPause is the beat before a restart that would cut a watcher off:
// long enough for a follower's poll to pick the step up and for the frame to
// reach the browser, short enough that nobody reads it as the upgrade stalling.
const upgradeFlushPause = 400 * time.Millisecond

// upgradeFollowMax bounds a follower's wait. A holder that is killed releases
// the lock and the follower notices; this is the backstop for the case where it
// somehow neither finishes nor dies.
const upgradeFollowMax = 12 * time.Minute

// upgradeStaleAfter is how old a transcript may be and still be considered part
// of a live run. Guards the window between a winner taking the lock and
// truncating the file, so a follower cannot replay the PREVIOUS upgrade.
const upgradeStaleAfter = 2 * time.Minute

// upgradeLockName is the machine-wide claim on "an upgrade is running".
const upgradeLockName = "upgrade.lock"

// machineUpgradeLock is a held claim on that name. It also carries the run's
// identity, written into the lock file so followers can tell the transcript of
// the run they are watching from one left behind by an earlier upgrade.
type machineUpgradeLock struct {
	f  *os.File
	id string
}

// runID returns this run's identity, or "" if there is no usable lock.
func (l *machineUpgradeLock) runID() string {
	if l == nil {
		return ""
	}
	return l.id
}

// currentRunID reads the identity of the run that currently holds the claim.
// Readable without taking the lock — flock is advisory, and this is only ever
// read by processes that already know they are NOT the driver.
func currentRunID() string {
	path, err := lockFilePath(upgradeLockName)
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// claimMachineUpgrade claims the machine-wide right to drive an upgrade,
// without blocking. drive is false when another process on this machine already
// holds it — that caller should follow the run rather than start a second one.
//
// A machine where the lock cannot be taken at all (an unwritable home) still
// drives — see the error branch below.
func claimMachineUpgrade() (lock *machineUpgradeLock, drive bool) {
	f, held, err := tryLockFile(upgradeLockName)
	if err != nil {
		// Locking is impossible here. Drive anyway: refusing to upgrade because
		// a lock file could not be created would turn a rare filesystem problem
		// into a button that never works, which is worse than the race.
		return nil, true
	}
	if !held {
		return nil, false
	}
	// Stamp the claim. The transcript header repeats this, which is what lets a
	// viewer who presses the button MID-RUN tell the story it is watching from
	// a leftover file — a distinction no timestamp can make, because a slow
	// download legitimately keeps one run's header old for minutes.
	id := strconv.FormatInt(time.Now().UnixNano(), 36) + "-" + strconv.Itoa(os.Getpid())
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt([]byte(id+"\n"), 0)
		_ = f.Sync()
	}
	return &machineUpgradeLock{f: f, id: id}, true
}

// release drops the claim. Only reached on paths that do NOT re-exec — a
// successful upgrade replaces this process and the kernel does it for us.
func (l *machineUpgradeLock) release() {
	if l == nil {
		return
	}
	unlockFile(l.f)
}

func upgradeStepsPath() (string, error) { return lockFilePath("upgrade-steps.jsonl") }

// transcriptLine is one record in the shared transcript. The header (kind
// "start") stamps the run so a follower can tell a live run from a stale file.
type transcriptLine struct {
	Kind  string       `json:"kind"`
	Run   string       `json:"run,omitempty"` // which run this transcript belongs to
	Unix  int64        `json:"unix"`
	Step  upgradeStage `json:"step,omitzero"`
	Final bool         `json:"final,omitempty"`
}

// beginTranscript truncates the transcript and stamps it. Called by the winner,
// while holding the lock, before the first step.
func beginTranscript(runID string) {
	path, err := upgradeStepsPath()
	if err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_ = json.NewEncoder(f).Encode(transcriptLine{Kind: "start", Run: runID, Unix: time.Now().Unix()})
}

// publishStep appends one step for the followers. Best-effort: a machine that
// cannot write the transcript still upgrades, it just cannot narrate to viewers
// on other sessions.
func publishStep(s upgradeStage, final bool) {
	path, err := upgradeStepsPath()
	if err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_ = json.NewEncoder(f).Encode(transcriptLine{Kind: "step", Unix: time.Now().Unix(), Step: s, Final: final})
}

// transcriptTail is one watcher's position in the driver's transcript.
type transcriptTail struct {
	since time.Time // when following began; anchors the stale-run guard
	sent  int       // how many lines have been relayed
	live  bool      // a start header for THIS run has been seen
}

// drain relays every step published since the last call, reporting whether the
// run's final step has now been seen.
func (t *transcriptTail) drain(emit func(upgradeStage)) (done bool) {
	lines, ok := readTranscript()
	if !ok {
		return false
	}
	for i, ln := range lines {
		if ln.Kind == "start" {
			// A transcript left behind by an EARLIER upgrade must not be
			// replayed as if it were this one.
			//
			// Matched by run identity, not by age. Age cannot decide this: a
			// large bundle on a slow link keeps a live run's header old for
			// minutes, so an age test either goes silent partway through the
			// upgrade it is narrating, or — for a viewer who presses the button
			// mid-run — reports a successful upgrade as one that "stopped
			// before it reported a result".
			if run := currentRunID(); run != "" {
				if ln.Run != run {
					break
				}
			} else if time.Unix(ln.Unix, 0).Before(t.since.Add(-upgradeStaleAfter)) {
				break // no identity available: fall back to age
			}
			t.live = true
			continue
		}
		if !t.live || i < t.sent {
			continue
		}
		emit(ln.Step)
		t.sent = i + 1
		if ln.Final {
			return true
		}
	}
	return false
}

// followMachineUpgrade relays the driving process's steps to this session's
// watchers until the run ends, so pressing the button on a second session
// narrates the same upgrade instead of starting another one.
//
// Every exit path emits an ending. A watcher here is NOT the presser whose
// session gets restarted at the end of a successful run — it may be a viewer on
// a session nothing ever touches — so if this returns quietly its panel keeps
// spinning with nothing left to arrive.
func followMachineUpgrade(version string, emit func(upgradeStage)) {
	tail := transcriptTail{since: time.Now()}
	deadline := tail.since.Add(upgradeFollowMax)
	for time.Now().Before(deadline) {
		if tail.drain(emit) {
			return
		}
		time.Sleep(upgradeFollowPoll)

		l, drive := claimMachineUpgrade()
		if !drive {
			continue // still running
		}
		l.release()
		// The driver is gone. Most often it simply finished: its last act is to
		// publish the final step and replace itself, which is what dropped the
		// lock. Look once more before concluding anything.
		if tail.drain(emit) {
			return
		}
		// It really did vanish mid-upgrade — killed, or crashed. Say so rather
		// than leaving a spinner: the host may or may not have been replaced by
		// then, and that is exactly what the reader needs to go and check.
		emit(upgradeStage{Stage: "restart", Version: version,
			Error: "the upgrade stopped before it reported a result — check this host's version before retrying"})
		return
	}
	emit(upgradeStage{Stage: "restart", Version: version,
		Error: "gave up waiting for the upgrade already running on this machine"})
}

func readTranscript() ([]transcriptLine, bool) {
	path, err := upgradeStepsPath()
	if err != nil {
		return nil, false
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	var out []transcriptLine
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		var ln transcriptLine
		if err := json.Unmarshal(sc.Bytes(), &ln); err != nil {
			continue // a torn final line: the next poll will see it whole
		}
		out = append(out, ln)
	}
	return out, true
}
