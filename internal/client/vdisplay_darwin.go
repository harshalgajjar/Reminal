// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build darwin

package client

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/reminal/reminal/internal/config"
	"github.com/reminal/reminal/internal/proc"
)

// Closed-lid ("leave & forget") mode, display half. A Mac that goes fully
// headless — lid shut, monitor unplugged or powered off in a way that drops
// EDID — loses its display coordinate space: windows migrate to a phantom
// arrangement, ScreenCaptureKit has nothing to attach to, and injected clicks
// land in a space that no longer exists. This watcher gives the machine a
// stable software display the moment the last real one goes away (the same
// trick DeskPad/BetterDisplay use), and tears it down when a real display
// returns. Sleep, the other half of the problem, is handled by the settings
// page toggling `pmset disablesleep` — an agent can't do that without root.

// vdisplayPoll is the census cadence. Each poll is one ~100ms osascript; slow
// enough to be invisible, fast enough that a yanked monitor grows a virtual
// replacement within seconds.
const vdisplayPoll = 12 * time.Second

// censusLeaseTTL is how long a census claim stands without being refreshed.
// The owner rewrites the lease every vdisplayPoll, so this is ~2.5 missed
// refreshes: long enough to ride out a slow tick, short enough that a dead
// owner is replaced before anyone notices the display is gone.
const censusLeaseTTL = 30 * time.Second

// vdisplayName must match the descriptor name in reminal-capture's vdisplay
// subcommand — it's how the census tells our software display from real ones.
const vdisplayName = "reminal"

// vdisplayLockPath coordinates multiple agents on one machine: only one needs
// to (and should) hold a virtual display. The file holds the helper child's
// PID; a live PID means someone else already provides the display.
func vdisplayLockPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".reminal", "vdisplay.pid")
}

// displayCensus returns how many REAL displays are attached (ours excluded)
// and the point size of the first real one (0,0 when none) — remembered so the
// virtual display can match the layout the windows were living in.
func displayCensus() (real int, w, h int, err error) {
	out, err := run("osascript", "-l", "JavaScript", "-e",
		`ObjC.import("AppKit");
var s = $.NSScreen.screens, out = [];
for (var i = 0; i < s.count; i++) {
  var sc = s.objectAtIndex(i), name = "";
  try { name = ObjC.unwrap(sc.localizedName); } catch (e) {}
  var f = sc.frame;
  out.push(name + "\t" + Math.round(f.size.width) + "\t" + Math.round(f.size.height));
}
out.join("\n");`)
	if err != nil {
		return 0, 0, 0, err
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimSpace(line), "\t")
		if len(f) != 3 || f[0] == vdisplayName {
			continue
		}
		if real == 0 {
			w, h = atoi(f[1]), atoi(f[2])
		}
		real++
	}
	return real, w, h, nil
}

// vdisplayLoop keeps the closed-lid promise: while settings.ClosedLid is on
// and no real display is attached, a virtual display exists. Settings are
// re-read every poll (a settings-page toggle needs no push channel), and the
// helper child carries the same stdin lifeline as capture streams, so it can't
// outlive a killed or hot-restarted process.
//
// isDaemon says whether this is the machine's daemon. Only the daemon runs the
// census; sessions defer to it, exactly as the directory host does, and while
// deferring they never spawn the osascript. Sessions still carry the loop as a
// fallback for a machine with no daemon service installed — a bare `reminal`
// on a box that never ran install.sh.
func vdisplayLoop(stop <-chan struct{}, isDaemon bool) {
	var child *exec.Cmd
	var childStdin io.WriteCloser
	var childDone chan struct{} // closed by the waiter goroutine when the child exits
	lastW, lastH := 1920, 1080

	reap := func() {
		if child == nil {
			return
		}
		if childStdin != nil {
			_ = childStdin.Close() // lifeline EOF — graceful exit
		}
		if child.Process != nil {
			_ = child.Process.Kill() // backstop
		}
		<-childDone // the waiter goroutine reaps; no zombies
		if p := vdisplayLockPath(); p != "" {
			_ = os.Remove(p)
		}
		child, childStdin, childDone = nil, nil, nil
	}
	defer reap()
	defer releaseCensusLease()

	for {
		if sleepOrStop(stop, vdisplayPoll) {
			return
		}

		// Someone else is already doing this. A deferring session spawns
		// nothing — a stat, a small read and a signal-0 probe — which is the
		// entire point. The daemon never defers: it is the machine singleton
		// and takes the census back from any session that stood in for it
		// while it was down.
		if !isDaemon && censusHeldByOther() {
			// Yield anything we were holding from before the owner appeared;
			// it picks the display up on its next tick, once the lock is free.
			reap()
			continue
		}
		claimCensusLease()

		if !config.LoadSettings().ClosedLid {
			reap()
			continue
		}
		// A child that died on its own (interface change, manual kill) must not
		// look like coverage.
		if child != nil {
			select {
			case <-childDone:
				reap()
			default:
			}
		}
		// Someone else already provides the display: nothing to decide, and no
		// reason to pay for an osascript to find that out. Only safe while we
		// hold no child of our own — if we do, the census is how we learn a
		// real display came back and we should stand down.
		if child == nil && vdisplayHeldByOther() {
			continue
		}
		real, w, h, err := displayCensus()
		if err != nil {
			continue // census is best-effort; try again next tick
		}
		if os.Getenv("REMINAL_FORCE_VDISPLAY") == "1" {
			real = 0 // test hook: behave headless with a monitor attached
		}
		if real > 0 {
			lastW, lastH = w, h
			reap()
			continue
		}
		// Headless. Covered already — by us or by another agent's live child?
		if child != nil {
			continue
		}
		if p := vdisplayLockPath(); p != "" {
			if b, err := os.ReadFile(p); err == nil {
				if pid, _ := strconv.Atoi(strings.TrimSpace(string(b))); pid > 0 && proc.Alive(pid) {
					continue
				}
			}
		}
		helper, err := captureHelperPath()
		if err != nil {
			continue // no helper installed — nothing we can do headless
		}
		// REMINAL_FORCE_VDISPLAY exists purely so this path is testable with a
		// monitor attached (census is skipped above when forcing).
		cmd := exec.Command(helper, "vdisplay", strconv.Itoa(lastW), strconv.Itoa(lastH))
		stdin, err := cmd.StdinPipe() // lifeline
		if err != nil {
			continue
		}
		if err := cmd.Start(); err != nil {
			_ = stdin.Close()
			continue
		}
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }() // reap on exit — no zombies
		child, childStdin, childDone = cmd, stdin, done
		if p := vdisplayLockPath(); p != "" {
			_ = os.WriteFile(p, []byte(fmt.Sprintf("%d\n", cmd.Process.Pid)), 0o600)
		}
	}
}

// vdisplayCensusLeasePath is where whoever runs the census says so. Distinct
// from vdisplayLockPath, which names the holder of an existing DISPLAY: the
// census runs whether or not a display currently exists (that is how a newly
// headless machine grows one), so "who is watching" needs a claim of its own.
func vdisplayCensusLeasePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".reminal", "vdisplay-census.pid")
}

// censusHeldByOther reports whether another live process has claimed the
// census recently.
//
// This exists instead of "is the daemon running", and the difference is a
// version-compatibility one. Deferring to a live daemon assumes that daemon
// does the census — true only of a daemon new enough to have this code. A
// NEW session next to an OLD daemon would have deferred to a process that
// never censuses, and nobody would have provided the virtual display:
// closed-lid mode failing silently on every upgrade window, and permanently
// if the daemon never restarted. A lease cannot lie about that. An old daemon
// writes none, so sessions see no claim and do the work themselves.
//
// It pays a second dividend on a machine with no daemon at all: the sessions
// elect ONE census owner among themselves instead of all running it, which is
// most of the saving even in the fallback path.
func censusHeldByOther() bool {
	p := vdisplayCensusLeasePath()
	if p == "" {
		return false
	}
	fi, err := os.Stat(p)
	if err != nil || time.Since(fi.ModTime()) > censusLeaseTTL {
		return false // no claim, or an abandoned one
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return false
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return pid > 0 && pid != os.Getpid() && proc.Alive(pid)
}

// claimCensusLease records that we are the one doing this, and refreshes the
// mtime the staleness check reads. Best-effort: a machine where this cannot be
// written simply falls back to every session censusing, which is what it did
// before the lease existed.
func claimCensusLease() {
	p := vdisplayCensusLeasePath()
	if p == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(p, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600)
}

// releaseCensusLease drops our claim on the way out, so a survivor takes over
// on its next poll instead of waiting out censusLeaseTTL. Only ever removes
// our OWN claim: a successor that has already taken the lease must keep it.
func releaseCensusLease() {
	p := vdisplayCensusLeasePath()
	if p == "" {
		return
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return
	}
	if pid, _ := strconv.Atoi(strings.TrimSpace(string(b))); pid == os.Getpid() {
		_ = os.Remove(p)
	}
}

// vdisplayHeldByOther reports whether another live process already owns the
// virtual display, read from the lock file. A stat plus a liveness probe —
// orders of magnitude cheaper than the census it lets us skip.
//
// Liveness is internal/proc.Alive rather than the bare signal-0 probe this
// used to carry: Alive counts EPERM as alive, and a signal-0 probe does not.
// That distinction did not matter while every lock holder was one of this
// user's own sessions. It does now the daemon holds it — a daemon running
// under another account would have read as dead, and the machine would have
// grown a second virtual display on top of the working one.
func vdisplayHeldByOther() bool {
	p := vdisplayLockPath()
	if p == "" {
		return false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return false
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return pid > 0 && pid != os.Getpid() && proc.Alive(pid)
}
