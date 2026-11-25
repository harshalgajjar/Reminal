// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"fmt"
	"os"
	"sync/atomic"

	"reminal/internal/session"
)

// A session's process leaves word of how it went, in ~/.reminal/restore:
// what the Go runtime prints as it dies goes to the session's crash file
// (keepCrashOutput), and every other end is one "ended: ..." line in
// events.log (logEnded). A background session's own output goes nowhere, so
// before this a session that died left nothing to tell a fault of reminal's
// from a kill from outside. Read together with the lines reminal keeps when
// it signals a session itself (session.LogSent): a crash file that says
// something is a fault in reminal, a sender's line is reminal ending it, an
// "ended" line is the session going in order, and none of them is a kill
// from outside.

// servedSession is the id of the session this process serves, once Run has
// begun: a panic recovered anywhere in it (logPanicStack) is said in that
// session's lines too.
var servedSession atomic.Value

// keepCrashOutput points the runtime's dying words at this session's crash
// file. Run does it first thing, so the next image of a restart in place and
// a restored session do it too.
func (a *Agent) keepCrashOutput() {
	servedSession.Store(a.sessionID)
	what := "started"
	switch {
	case a.restoring:
		what = "restored"
	case a.resumed:
		what = "restarted in place"
	}
	_ = session.KeepCrashOutput(a.sessionID, fmt.Sprintf("%s, v%s", what, a.version))
}

// logEnded keeps why this session's process ended, once however many ways
// out are taken at the same time, and drops a crash file nothing died into.
func (a *Agent) logEnded(err error) {
	a.logEndedAs("%s", a.endReason(err))
}

func (a *Agent) logEndedAs(format string, args ...any) {
	a.sayEnded(format, args...)
	session.DropQuietCrashLog(a.sessionID)
}

// sayEnded keeps the line alone, once. run says it the moment the session is
// over, before its teardown: the teardown can take seconds, and a process
// ended in the middle of it (reminal kill sends terminate when a session it
// asked to quit is still there 2 s later) had left no word of its own. Its
// crash file is kept until the teardown is done, for a fault in it.
func (a *Agent) sayEnded(format string, args ...any) {
	a.endedOnce.Do(func() {
		session.LogEnded(a.sessionID, "%s (pid %d)", fmt.Sprintf(format, args...), os.Getpid())
	})
}

// endReason says why Run returned, the first of these that holds: its own
// error; put to sleep; a signal it was sent; its terminal gone; asked to
// quit; or its shell ending by itself, with how the system says that went.
func (a *Agent) endReason(err error) string {
	var why string
	sig, _ := a.endSignal.Load().(string)
	switch {
	case err != nil:
		why = fmt.Sprintf("error: %v", err)
	case a.sleeping.Load():
		why = "asleep (put to sleep)"
	case sig != "":
		why = fmt.Sprintf("signal: %s received", sig)
	case a.hungUp.Load():
		why = "hung up: its terminal went away"
	case a.quitAsked():
		why = "quit: asked to stop"
	default:
		why = "its shell ended"
		if a.term != nil {
			if how := a.term.Ended(); how != "" {
				why += ": " + how
			}
		}
	}
	if a.paused.Load() {
		why += ", after sharing was stopped"
	}
	return why
}

// quitAsked reports whether this session was asked to wind down
// (requestShutdown: Ctrl-], or reminal's own "quit").
func (a *Agent) quitAsked() bool {
	if a.hostEscape == nil {
		return false
	}
	select {
	case <-a.hostEscape:
		return true
	default:
		return false
	}
}

// shellPID is this session's shell's pid when it is known for certain, for
// its record: what runs in the session is found by it where the shell is
// not under this process (session.Active.ShellPID).
func (a *Agent) shellPID() int {
	if a.term == nil {
		return 0
	}
	return a.term.ShellPid()
}

// shellIdent says which process the shell's pid is (session.ProcessIdentity),
// taken once for each shell: later a process given the same pid is not it.
func (a *Agent) shellIdent() string {
	pid := a.shellPID()
	if pid <= 0 {
		return ""
	}
	a.shellIdentMu.Lock()
	defer a.shellIdentMu.Unlock()
	if a.shellIdentPID != pid {
		a.shellIdentPID, a.shellIdentIs = pid, session.ProcessIdentity(pid)
	}
	return a.shellIdentIs
}
