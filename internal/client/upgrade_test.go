// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// host_info is polled every 1.5s while the Host panel is open, and both of the
// fields this feature added are computed there. ReadAllActive parses one file
// per session, so an uncached count is hundreds of file reads a minute on a
// machine with a dozen sessions. This pins the caching, by counting the reads.
func TestCountRestartableSessionsIsCached(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".reminal")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		rec := `{"id":"SESS000` + strconv.Itoa(i) + `","pid":` + strconv.Itoa(os.Getpid()) + `}`
		if err := os.WriteFile(filepath.Join(dir, "active-SESS000"+strconv.Itoa(i)+".json"), []byte(rec), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Force a cold read for this test's HOME.
	sessCountMu.Lock()
	sessCountRead = time.Time{}
	sessCountMu.Unlock()

	first := countRestartableSessions()
	if first != 5 {
		t.Fatalf("counted %d sessions, want 5", first)
	}
	// Delete every record. A cached answer must survive; an uncached one would
	// drop to 0 and the panel would say "every session" mid-poll.
	matches, _ := filepath.Glob(filepath.Join(dir, "active-*.json"))
	for _, m := range matches {
		_ = os.Remove(m)
	}
	if again := countRestartableSessions(); again != 5 {
		t.Errorf("second call returned %d — it re-read the disk instead of using the cache", again)
	}

	// And once the TTL lapses it must notice the change rather than being stuck.
	sessCountMu.Lock()
	sessCountRead = time.Now().Add(-sessionCountTTL - time.Second)
	sessCountMu.Unlock()
	if fresh := countRestartableSessions(); fresh != 0 {
		t.Errorf("after the TTL the count is %d, want 0 — the cache never expires", fresh)
	}
}

// A read error must not silently report "no sessions": the subtitle would
// claim an upgrade restarts nothing.
func TestCountKeepsLastGoodAnswerOnError(t *testing.T) {
	sessCountMu.Lock()
	sessCountVal, sessCountRead = 7, time.Now().Add(-sessionCountTTL-time.Second)
	sessCountMu.Unlock()
	// A HOME that exists but holds no readable registry: ReadAllActive errors
	// rather than returning an empty list. (A NUL byte in the value is
	// rejected by setenv itself, so point at a path that cannot be a dir.)
	f := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", f)
	if got := countRestartableSessions(); got != 7 {
		t.Errorf("on a read error the count is %d, want the last good 7", got)
	}
}

// Two viewers pressing "Upgrade & restart" at the same moment must produce ONE
// install and TWO identical narrations — not an error for the loser, and
// certainly not two installs racing the same binary.
func TestUpgradeRunSharesOneRun(t *testing.T) {
	var r upgradeRun

	// First caller claims it.
	replayA, runningA := r.join(nil)
	if runningA {
		t.Fatal("a fresh run reported itself as already running")
	}
	if len(replayA) != 0 {
		t.Fatalf("nothing has happened yet, but %d steps were replayed", len(replayA))
	}
	if !r.begin() {
		t.Fatal("the first caller could not claim the run")
	}

	// It emits a couple of steps.
	r.record(upgradeStage{Stage: "download", Pct: 5})
	r.record(upgradeStage{Stage: "install", Pct: 80})

	// Second viewer arrives mid-flight.
	replayB, runningB := r.join(nil)
	if !runningB {
		t.Error("the second caller was not told a run is already under way — it would start a second install")
	}
	if len(replayB) != 2 {
		t.Errorf("the second caller was replayed %d steps, want 2 — it would join halfway through with no context", len(replayB))
	}
	if replayB[0].Stage != "download" || replayB[1].Stage != "install" {
		t.Errorf("replay is out of order: %+v", replayB)
	}
	if r.begin() {
		t.Error("the second caller was allowed to claim a run already in progress — two installs would race")
	}

	// Later steps reach every watcher, not just the one that started it.
	subs := r.record(upgradeStage{Stage: "restart", Pct: 90})
	if len(subs) != 1 {
		// join(nil) deduplicates by conn, and both calls passed nil, so one
		// entry is correct here; the point is that record returns the whole
		// watcher set rather than a single connection.
		t.Errorf("record returned %d watchers, want the full set", len(subs))
	}

	// A finished run releases, so a later upgrade can start.
	r.end()
	if _, running := r.join(nil); running {
		t.Error("the run still reports active after end()")
	}
	if !r.begin() {
		t.Error("a new run could not start after the previous one ended")
	}
}

// Distinct watchers all get every step.
func TestUpgradeRunFansOutToEveryWatcher(t *testing.T) {
	var r upgradeRun
	a, b := &websocket.Conn{}, &websocket.Conn{}
	r.join(a)
	r.begin()
	r.join(b)
	subs := r.record(upgradeStage{Stage: "install"})
	if len(subs) != 2 {
		t.Fatalf("record returned %d watchers, want 2 — one viewer would go silent mid-upgrade", len(subs))
	}
	// Joining twice must not double-subscribe (a viewer that re-presses the
	// button would otherwise get every later step twice).
	r.join(a)
	if subs = r.record(upgradeStage{Stage: "restart"}); len(subs) != 2 {
		t.Errorf("re-joining duplicated a watcher: %d", len(subs))
	}
}

// The whole point of the lock is that "press at the same moment" is a real
// simultaneity, not a turn-taking. Hammer it.
func TestUpgradeRunOnlyOneWinnerUnderRace(t *testing.T) {
	var r upgradeRun
	const racers = 64
	var wins int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // release them all at once
			if _, running := r.join(nil); running {
				return
			}
			if r.begin() {
				atomic.AddInt32(&wins, 1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if got := atomic.LoadInt32(&wins); got != 1 {
		t.Fatalf("%d callers claimed the run — that many installs would race the same binary", got)
	}
}

// Two viewers on two DIFFERENT sessions are served by two processes, each with
// its own upgradeRun. Only the machine-wide claim stops them both installing.
func TestClaimMachineUpgradeIsExclusive(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	first, drive := claimMachineUpgrade()
	if !drive {
		t.Fatal("the first claimant must be allowed to drive the upgrade")
	}
	if _, drive2 := claimMachineUpgrade(); drive2 {
		t.Fatal("a second process claimed the same upgrade — two installs would race on one binary")
	}
	first.release()
	third, drive3 := claimMachineUpgrade()
	if !drive3 {
		t.Fatal("the claim must be takeable again once released")
	}
	third.release()
}

// The loser does not refuse — it replays the winner's steps, so pressing the
// button on a second session narrates the same upgrade instead of an error.
func TestFollowerReplaysTheDriversTranscript(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// Hold the claim, as the driving process would.
	lock, drive := claimMachineUpgrade()
	if !drive {
		t.Fatal("could not take the claim")
	}
	defer lock.release()

	beginTranscript(lock.runID())
	publishStep(upgradeStage{Stage: "download", Pct: 5, Detail: "Fetching the latest release"}, false)
	publishStep(upgradeStage{Stage: "install", Pct: 80, Detail: "Binary replaced"}, false)
	publishStep(upgradeStage{Stage: "restart", Pct: 97, Detail: "Restarting this session"}, true)

	var got []upgradeStage
	done := make(chan struct{})
	go func() {
		followMachineUpgrade("3.5.5", func(s upgradeStage) { got = append(got, s) })
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the follower never finished; a viewer would spin forever")
	}

	if len(got) != 3 {
		t.Fatalf("follower saw %d steps, want the driver's 3: %+v", len(got), got)
	}
	if got[0].Stage != "download" || got[2].Pct != 97 {
		t.Fatalf("the narration does not match the driver's: %+v", got)
	}
}

// A transcript left behind by an earlier upgrade must not be replayed as if it
// were the run the viewer just asked about — but the viewer must still be told
// something, or its panel spins forever.
func TestFollowerIgnoresAStaleTranscript(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	path, err := upgradeStepsPath()
	if err != nil {
		t.Fatal(err)
	}
	// ~/.reminal is created by the lock claim, which this test deliberately
	// skips: the point is a follower meeting a transcript with no live run.
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * upgradeStaleAfter).Unix()
	body := `{"kind":"start","unix":` + strconv.FormatInt(old, 10) + "}\n" +
		`{"kind":"step","step":{"stage":"done","pct":100},"final":true}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	got := followForTest(t, "3.5.5")
	for _, s := range got {
		if s.Stage == "done" || s.Pct == 100 {
			t.Fatalf("replayed a step from a previous upgrade: %+v", got)
		}
	}
	if len(got) != 1 || got[0].Error == "" {
		t.Fatalf("want exactly one ending that explains itself, got %+v", got)
	}
}

// A viewer who presses the button PART WAY through a long upgrade must be
// narrated that upgrade. Its header is old by then — a big bundle on a slow
// link takes minutes — so run identity, not age, has to decide.
func TestFollowerRelaysARunItJoinedLate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	lock, drive := claimMachineUpgrade()
	if !drive {
		t.Fatal("could not take the claim")
	}
	defer lock.release()

	path, err := upgradeStepsPath()
	if err != nil {
		t.Fatal(err)
	}
	long := time.Now().Add(-10 * time.Minute).Unix()
	header := `{"kind":"start","run":"` + lock.runID() + `","unix":` + strconv.FormatInt(long, 10) + "}\n"
	if err := os.WriteFile(path, []byte(header), 0o600); err != nil {
		t.Fatal(err)
	}
	publishStep(upgradeStage{Stage: "install", Pct: 80, Detail: "Binary replaced"}, false)
	publishStep(upgradeStage{Stage: "restart", Pct: 97, Detail: "Restarting this session"}, true)

	got := followForTest(t, "3.5.5")
	if len(got) != 2 || got[0].Stage != "install" || got[1].Pct != 97 {
		t.Fatalf("a viewer joining a long run mid-flight saw %+v", got)
	}
}

// A driver that dies mid-upgrade leaves no final step. The watcher's own
// session is never restarted, so nothing else will arrive to end its spinner.
func TestFollowerReportsADriverThatVanished(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	lock, drive := claimMachineUpgrade()
	if !drive {
		t.Fatal("could not take the claim")
	}
	beginTranscript(lock.runID())
	publishStep(upgradeStage{Stage: "download", Pct: 5, Detail: "Fetching the latest release"}, false)
	lock.release() // killed: no final step, and the lock is gone

	got := followForTest(t, "3.5.5")
	if len(got) != 2 {
		t.Fatalf("want the real step then an ending, got %+v", got)
	}
	if got[0].Stage != "download" {
		t.Fatalf("lost the step the driver did publish: %+v", got)
	}
	if got[1].Error == "" {
		t.Fatalf("a vanished driver ended without saying so: %+v", got)
	}
}

// followForTest runs a follower to completion, failing the test if it hangs.
func followForTest(t *testing.T, version string) []upgradeStage {
	t.Helper()
	var got []upgradeStage
	done := make(chan struct{})
	go func() {
		followMachineUpgrade(version, func(s upgradeStage) { got = append(got, s) })
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the follower never finished; a viewer would spin forever")
	}
	return got
}

// Pressing the button after an earlier attempt must narrate the NEW attempt,
// not replay the old one's outcome first.
func TestJoinDoesNotReplayAFinishedRun(t *testing.T) {
	var r upgradeRun
	if !r.begin() {
		t.Fatal("could not begin a run")
	}
	r.record(upgradeStage{Stage: "download", Error: "download: 404 File not found"})
	r.end()

	replay, running := r.join(nil)
	if running {
		t.Fatal("a finished run still reports as running")
	}
	if len(replay) != 0 {
		t.Fatalf("replayed %d steps from a finished run: %+v", len(replay), replay)
	}
}
