// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/gorilla/websocket"

	"github.com/reminal/reminal/internal/protocol"
	"github.com/reminal/reminal/internal/updater"
)

// handleChangelog answers a viewer's request for the release notes from this
// host's own version through the newest published one.
//
// Fetched, not embedded: a host on 3.5.4 has no 3.5.6 file, so most of what is
// worth reading about is not carried by the binary asking. Runs on
// its own goroutine (the caller dispatches it that way) because it is a
// network round trip and must not stall the shell stream.
func (a *Agent) handleChangelog(conn *websocket.Conn) {
	if a.box == nil {
		return
	}
	var payload struct {
		Current  string            `json:"current"`
		Releases []updater.Release `json:"releases,omitempty"`
		Error    string            `json:"error,omitempty"`
	}
	payload.Current = a.version

	ctx, cancel := context.WithTimeout(context.Background(), updater.ReleaseNotesTimeout)
	defer cancel()
	rels, err := updater.ReleasesSince(ctx, a.version, 20)
	if err != nil {
		// Say what went wrong rather than showing an empty sheet: "could not
		// reach the notes" and "there are no notes" look identical otherwise,
		// and only one of them means anything.
		payload.Error = err.Error()
	} else {
		payload.Releases = rels
	}
	a.sendWindowMsg(conn, protocol.TypeChangelog, payload)
}

// handleUpgrade upgrades the host's binary and hot-restarts every session on
// it, reporting each step to the viewer that asked.
//
// No new capability: a viewer that can send this already has shell access and
// could type `reminal upgrade && reminal restart --all`. What it adds is that
// the restart is survivable to watch — a hot restart re-execs each agent onto
// the new binary while keeping its PTY, so the shells and everything running
// in them come back.
//
// Guarded by upgrading so two viewers cannot race the same install.
// daemonServiceManaged reports whether an OS service manager automatically
// restarts the background daemon when it exits: launchd's KeepAlive on macOS and
// systemd's Restart=always on Linux both do. Windows' HKCU Run key has no
// keepalive (it fires only at logon), and the other platforms have no service
// integration at all — there the daemon must NOT exit expecting a restart.
func daemonServiceManaged() bool {
	return runtime.GOOS == "darwin" || runtime.GOOS == "linux"
}

func (a *Agent) handleUpgrade(conn *websocket.Conn, data string) {
	if a.box == nil {
		return
	}
	// Authorisation first, before anything is joined, replayed or started.
	//
	// This is NOT the button being hidden in the panel: every viewer on a
	// session shares one encryption key, so a PIN guest can craft this message
	// byte for byte. The request has to prove, with a key only an enrolled
	// device holds, that whoever sent it owns this machine.
	//
	// The blast radius is why it matters even though a guest already has a
	// shell here: a PIN grants one session, and this replaces the binary and
	// restarts EVERY session on the machine.
	var proof ownerProof
	if data != "" {
		if pt, err := a.box.Decrypt(data); err == nil {
			_ = json.Unmarshal(pt, &proof)
		}
	}
	if why := a.verifyOwnerAction(proof, "upgrade"); why != "" {
		a.sendWindowMsg(conn, protocol.TypeUpgrade, upgradeStage{
			Stage: "download", Error: why, Version: a.version,
		})
		return
	}
	// Watch first, decide second. Whoever asks is replayed the transcript so
	// far, so a second viewer sees the same steps from the beginning instead
	// of an error or a progress bar already half-finished.
	replay, running := a.upgrade.join(conn)
	for _, s := range replay {
		a.sendWindowMsg(conn, protocol.TypeUpgrade, s)
	}
	if running {
		return // someone else is driving; this viewer just watches
	}
	if !a.upgrade.begin() {
		return // lost the race between join and begin — the winner drives
	}
	defer a.upgrade.end()

	// Every step goes to everyone watching, not just whoever pressed first.
	// stepf also publishes to the machine-wide transcript so viewers on OTHER
	// sessions — served by other processes — read the same narration.
	stepf := func(s upgradeStage, final bool) {
		a.broadcastUpgrade(s)
		publishStep(s, final)
	}
	step := func(s upgradeStage) { stepf(s, false) }
	fail := func(stage, msg string) {
		// Every failure path leaves the host on its current version with every
		// session untouched, and says so — the thing a user needs to know
		// after a failed upgrade is whether they are in a half-state.
		stepf(upgradeStage{Stage: stage, Error: msg, Version: a.version}, true)
	}

	// Claim the upgrade for the whole MACHINE, not just this session. Two
	// viewers on two different sessions are served by two processes, each with
	// its own upgradeRun, so without this they would both install: two
	// downloads, and two extractions racing on one reminal.app.
	//
	// The loser does not refuse — it tails the winner's transcript and relays
	// it, so pressing the button on a second session shows the same steps
	// rather than an error about someone else's upgrade.
	lock, drive := claimMachineUpgrade()
	if !drive {
		followMachineUpgrade(a.version, a.broadcastUpgrade)
		return
	}
	defer lock.release()
	beginTranscript(lock.runID())

	// Refuse builds that must not be replaced in place, rather than relying on
	// the panel having hidden the button — this arrives as a message, and the
	// sender is not the only thing that can send one.
	if why := updater.UpgradeBlockedReason(a.version); why != "" {
		fail("download", why)
		return
	}

	step(upgradeStage{Stage: "download", Pct: 5, Detail: "Fetching the latest release", Version: a.version})

	updated, err := updater.UpgradeQuiet(a.version)
	if err != nil {
		fail("download", err.Error())
		return
	}
	if !updated {
		stepf(upgradeStage{Stage: "done", Pct: 100, Version: a.version,
			Detail: "Already on the latest version — nothing to do"}, true)
		return
	}
	step(upgradeStage{Stage: "install", Pct: 80, Detail: "Binary replaced", Version: a.version})

	// The daemon is a separate process and would otherwise pick the new binary
	// up on its own — watchBinaryAndExit notices the swap and exits, and the
	// service manager restarts it. But that poll is coarse, so left implicit
	// the machine spends up to half a minute with its sessions on the new
	// version and the daemon (which does capture, input and notes for all of
	// them) still on the old one. Bouncing it explicitly makes "upgraded" mean
	// the whole machine, and makes it something we can report rather than
	// something the user has to trust.
	//
	// Best-effort and non-fatal: a machine with no daemon service installed
	// returns nil here, and a failure to bounce one must not fail an upgrade
	// that has already replaced the binary.
	//
	// Gated on running from the installed bundle. RestartDaemonService resolves
	// the LOGGED-IN user's daemon from the passwd database — it does not follow
	// $HOME — so a dev build, or an agent running out of a scratch directory,
	// would otherwise kickstart the machine's real daemon after replacing a
	// binary that daemon has never run. Only the install the daemon actually
	// executes gets to bounce it.
	if daemonBounceApplies() {
		step(upgradeStage{Stage: "restart", Pct: 85, Version: a.version, Detail: "Restarting the background service"})
		if err := RestartDaemonService(); err != nil {
			step(upgradeStage{Stage: "restart", Pct: 85, Version: a.version,
				Detail: "Background service will pick up the new version shortly"})
		}
	}

	// Count what is about to move so the viewer can say "9 sessions" instead
	// of a spinner with no scale.
	//
	// This is the transcript's LAST line, and it has to be: viewers on other
	// sessions are relayed the transcript by their own agent, and every one of
	// those agents is re-exec'd on the next line. A step published after that
	// point can only reach them by winning a race against their own restart —
	// which is exactly what happened here, passing on a fast machine and
	// truncating a watcher's story on a slower one. So the ending is published
	// BEFORE the restarts, and reads as an ending for everyone watching.
	n := countRestartableSessions()
	stepf(upgradeStage{Stage: "restart", Pct: 90, Version: a.version,
		Detail: fmt.Sprintf("Restarting %d session%s — reconnecting shortly", n, plural(n))}, true)

	// Let it reach the watchers before their connections go.
	time.Sleep(upgradeFlushPause)

	// Restart every OTHER session first and this one last: hot-restarting our
	// own agent re-execs the process running this handler, so anything after
	// it would never run. `reminal restart --all` has the same ordering rule
	// for the same reason.
	if err := restartOtherSessions(); err != nil {
		fail("restart", err.Error())
		return
	}
	// The last line for the viewers on THIS session, who are still connected
	// because this agent restarts last. It goes to them directly rather than
	// through the transcript, whose ending was published above — a follower
	// whose own session has already been restarted cannot receive it, and
	// pretending otherwise is what made the narration machine-speed-dependent.
	// They reconnect to the same session ID and re-read host_info, which is how
	// they learn the new version.
	step(upgradeStage{Stage: "restart", Pct: 97, Version: a.version,
		Detail: "Restarting this session — reconnecting shortly"})
	time.Sleep(upgradeFlushPause) // let the frame flush before we exec
	if a.machine && a.daemonHost {
		if daemonServiceManaged() {
			// launchd (KeepAlive) / systemd (Restart=always) restarts the daemon
			// onto the new binary the instant it exits — immediate and
			// single-instance. It has no PTY to hand across and no session to
			// resume, so exiting is the whole restart.
			os.Exit(0)
		}
		// No service manager (Windows' Run key fires only at logon): do NOT exit
		// or respawn here. watchBinaryAndExit is already polling the binary and
		// owns the single "hand off to a fresh copy and exit" ritual; doing it
		// here too would race that goroutine and start two daemons. The daemon
		// stays online on the current binary until its next tick
		// (≤ binaryWatchInterval), so the machine never drops offline meanwhile.
		return
	}
	// A session, OR a machine channel served in-process by a session/tunnel host
	// (a.machine && !a.daemonHost, when no daemon is installed): os.Exit here
	// would hard-kill the user's live process with nothing to restart it, so
	// hot-restart it in place instead — it re-execs onto the new binary.
	if _, err := sendControlTo(os.Getpid(), "restart"); err != nil {
		// Our own restart failed but the others already went. Say so plainly
		// rather than pretending the upgrade completed.
		fail("restart", "the other sessions restarted, but this one did not: "+err.Error())
	}
}
