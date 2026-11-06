package client

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"reminal/internal/session"
)

// attention_probe.go detects a session's "attention state" — is the foreground
// agent working, blocked awaiting the user, or done — and writes it onto the
// session record so `reminal list` (and, later, the fleet view) can show which
// session needs you WITHOUT attaching to each one.
//
// It is deliberately signal-light and cooperation-free (works with any harness):
//   - alt:        emulator alt-screen flag — a full-screen TUI (claude/agy/…) is live
//   - settled_ms: how long the visible bottom rows have been byte-for-byte unchanged
//   - tail:       the bottom rows themselves (for the awaiting-input vs done split)
//   - fg:         foreground command name (fallback agent-detection for line-mode CLIs)
//
// The bet: settled_ms + alt split working (screen churning — spinners/animation)
// from settled (screen static); the tail text then splits "awaiting input"
// (an approval/question prompt) from "done". This is heuristic and errs toward
// "done" rather than a false "needs you". Hook-based ground truth (a harness's
// own Stop/Notification event) can override this later; the timing split is the
// drift-proof part, the tail patterns the maintainable part.
//
// Setting REMINAL_ATTENTION_PROBE=<file> additionally appends one JSON sample per
// tick to that file — the Phase-0 measurement log used to tune the thresholds.

const (
	// attnInterval is the closest two reads of the screen come: output
	// arriving faster than this is read at this pace, not per chunk.
	attnInterval = 300 * time.Millisecond
	attnTailRows = 12
	// attnPromptRows is how much of the bottom of the screen may overrule a
	// harness that has said "done". A real chooser is right at the prompt.
	attnPromptRows = 5
	// A screen unchanged for at least this long counts as "settled". Agent
	// spinners/elapsed-time counters repaint faster than this, so an actively
	// working agent stays "working"; a blocked or finished one goes settled.
	attnSettleMs = 1200
	// attnResizeGrace is how long after a resize a changing screen is the
	// program redrawing for it rather than working.
	attnResizeGrace = 1500 * time.Millisecond
	// attnHookGrace is how long after a hook fires we still attribute terminal
	// output to that hook's own event — the agent painting the prompt it just
	// announced. Past it, continued output means the agent resumed and the
	// resting state it reported is stale (see the hook block in runAttention).
	attnHookGrace = 3 * time.Second
)

// startAttention launches the attention detector for this session. The detector
// itself always runs (it's the feature); REMINAL_ATTENTION_PROBE only adds the
// raw-sample log on top. Called from initScreen once a.screen is live.
func (a *Agent) startAttention() {
	a.screenMu.Lock()
	have := a.screen != nil
	a.screenMu.Unlock()
	if !have {
		return
	}
	a.attnMu.Lock()
	if a.attnStop == nil {
		a.attnStop = make(chan struct{})
	}
	stop := a.attnStop
	if a.attnDone != nil {
		a.attnMu.Unlock()
		return // already running
	}
	done := make(chan struct{})
	a.attnDone = done
	a.attnMu.Unlock()
	go func() {
		defer close(done)
		a.runAttention(os.Getenv("REMINAL_ATTENTION_PROBE"), stop)
	}()
}

// kickAttention tells the detector something it reads may have changed: PTY
// output, a resize, a hook's new state. Cheap and never blocks — kicks
// arriving together are one read.
func (a *Agent) kickAttention() {
	select {
	case a.attnKicks() <- struct{}{}:
	default:
	}
}

func (a *Agent) attnKicks() chan struct{} {
	a.attnKickOnce.Do(func() { a.attnKick = make(chan struct{}, 1) })
	return a.attnKick
}

// stopAttention ends the detector and returns once it has: the session is
// going, and a probe still mid-read when the PTY closes polls a closed file
// (a data race the tests caught). Told to stop, it finishes the read it is
// in and returns; this waits for that, so whatever closes the PTY next is
// after it.
func (a *Agent) stopAttention() {
	a.attnMu.Lock()
	if a.attnStop != nil {
		close(a.attnStop)
		a.attnStop = nil
	}
	done := a.attnDone
	a.attnDone = nil
	a.attnMu.Unlock()
	if done != nil {
		<-done
	}
}

// releaseAfterBurstBytes is how much output makes a burst worth handing its
// memory back for once it is over.
const releaseAfterBurstBytes = 32 << 20

// releaseAfterBurst hands memory back to the OS once a burst of output is
// over (the detector finds the screen settled). Taking in a burst makes
// the emulator and the scrollback churn through far more memory than they
// keep; Go keeps the difference for reuse and returns it only slowly — an
// agent that printed for a minute sat at more than twice its live heap for
// minutes after. One collection and scavenge, once per burst, on its own
// goroutine: the detector does not wait for it.
func (a *Agent) releaseAfterBurst() {
	if a.outSinceFree.Load() < releaseAfterBurstBytes {
		return
	}
	a.outSinceFree.Store(0)
	go freeOSMemory()
}

// freeOSMemory is debug.FreeOSMemory; a variable for tests.
var freeOSMemory = debug.FreeOSMemory

// attnReadHook, for tests only, runs as the detector starts a read of the
// screen and the terminal.
var attnReadHook func()

type attentionProbeSample struct {
	TS        int64  `json:"ts"`         // unix millis
	FG        string `json:"fg"`         // foreground command name
	Alt       bool   `json:"alt"`        // alt-screen (full-screen TUI) active
	IdleMs    int64  `json:"idle_ms"`    // ms since last PTY output
	SettledMs int64  `json:"settled_ms"` // ms the visible tail has been unchanged
	State     string `json:"state"`      // final attention state (hook or screen)
	Source    string `json:"source"`     // "hook" (agent-reported) or "screen" (inferred)
	Tail      string `json:"tail"`       // bottom rows of the rendered screen
}

func (a *Agent) runAttention(logPath string, stop <-chan struct{}) {
	var enc *json.Encoder
	if logPath != "" {
		if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			defer f.Close()
			enc = json.NewEncoder(f)
		}
	}

	// The screen is read when something says it may have changed — a kick
	// (output, a resize, a hook) — and when a reading made earlier comes due
	// (a screen settling, a hook's "working" going stale), never on a clock
	// of its own: a session nobody is touching costs nothing. A kick within
	// attnInterval of the last read waits out the rest of it.
	timer := time.NewTimer(0)
	defer timer.Stop()
	var due time.Time // when timer fires; zero when it is not set
	at := func(t time.Time) {
		if !due.IsZero() && !t.Before(due) {
			return
		}
		due = t
		timer.Reset(time.Until(t))
	}
	due = time.Now()
	var lastRead time.Time
	kicks := a.attnKicks()

	var lastTail string
	lastChange := time.Now()
	var progCache foregroundProgCache
	// The programs whose own hook has spoken in this session (a state
	// written while that program was in front): one of them started again
	// says, by its hook, when its turn begins (resolveAttn).
	hookSpoke := map[string]bool{}
	// promptFGAt is the attnFGAt of the run in front whose prompt has been
	// seen drawn (attnPromptDrawn).
	var promptFGAt time.Time

	for {
		select {
		case <-stop:
			return
		case <-kicks:
			if next := lastRead.Add(attnInterval); time.Now().Before(next) {
				at(next)
				continue
			}
		case <-timer.C:
		}
		due = time.Time{}
		timer.Stop()
		lastRead = time.Now()
		if attnReadHook != nil {
			attnReadHook()
		}
		a.screenMu.Lock()
		scr := a.screen
		if scr == nil { // snapshot disabled / torn down — nothing to read
			a.screenMu.Unlock()
			return
		}
		alt := scr.IsAltScreen()
		render := scr.Render()
		resizedAt := a.resizedAt
		a.screenMu.Unlock()

		now := time.Now()
		tail := attentionProbeTail(render, attnTailRows)
		if tail != lastTail {
			lastTail = tail
			// Not a redraw the resize asked for: opening a finished session's
			// terminal on a phone resized it, and the session read as
			// "working" for a beat.
			if now.Sub(resizedAt) > attnResizeGrace {
				lastChange = now
			}
		}
		settledMs := now.Sub(lastChange).Milliseconds()

		a.metaMu.Lock()
		last := a.lastActivity
		a.metaMu.Unlock()
		var idleMs int64
		if !last.IsZero() {
			idleMs = now.Sub(last).Milliseconds()
		}
		if idleMs >= attnSettleMs {
			a.releaseAfterBurst()
		}

		// "An agent is running here" = a full-screen TUI took the alt screen, OR
		// a program other than the login shell holds the tty's foreground. The
		// latter is what catches inline TUIs like Claude Code, which never take
		// the alt screen — so alt alone would miss them.
		//
		// We decide it from the foreground command's NAME, not a pid compare,
		// because a pid compare breaks after a hot-restart: the resumed agent no
		// longer knows the shell's own pid, so Pid() falls back to the SAME
		// TIOCGPGRP as ForegroundPgrp — making `fgPgrp != shell` never true and an
		// idle Claude look like a bare shell, which dropped its badge the instant
		// the hook state aged out. The name check works either way; the pid
		// compare stays as a backstop for when the name can't be read.
		agentActive := alt
		fg := ""
		fgPgrpSeen := 0
		if a.term != nil {
			if fgPgrp := a.term.ForegroundPgrp(); fgPgrp > 0 {
				fgPgrpSeen = fgPgrp
				fg = attentionForegroundName(fgPgrp)
				if fg != "" && !isLoginShell(fg) {
					agentActive = true
				} else if shell := a.term.Pid(); shell > 0 && fgPgrp != shell {
					agentActive = true
				}
			}
		}

		// A console with no foreground process group to ask (Windows): the
		// program is found among the shell's children instead, now and then.
		if fgPgrpSeen == 0 && fg == "" {
			if prog := a.consoleForeground(now); prog != "" {
				fg, agentActive = prog, true
			}
		}
		prog := progCache.resolve(fgPgrpSeen, fg)
		a.noteForeground(prog, fgPgrpSeen)

		// Prefer the agent's own hook-reported state when it's fresh (an
		// integrated harness reporting via `reminal hook`); otherwise fall back to
		// the screen inference. The hook is precise; the screen is universal.
		a.metaMu.Lock()
		fgAt := a.attnFGAt
		a.metaMu.Unlock()
		// Whether prog has drawn its prompt since it came to the front: before
		// then a settled screen is it starting (attnPromptMarks), within
		// attnStartCap; after, a screen without it is a dialog over it.
		if attnPromptDrawn(prog, tail) {
			promptFGAt = fgAt
		}
		var sinceFG time.Duration
		if !fgAt.IsZero() {
			sinceFG = now.Sub(fgAt)
		} else {
			sinceFG = attnStartCap
		}
		screenState := classifyAttnFor(prog, agentActive, tail, settledMs, sinceFG, !fgAt.IsZero() && promptFGAt.Equal(fgAt))
		// The hook is precise; the screen is universal. resolveAttn puts the
		// two together — including the cases where the hook is not to be
		// believed: written by a harness that has since exited, left at
		// "working" by a turn that died, or overtaken by output since.
		bottom := attentionProbeTail(render, attnPromptRows)
		// How many shells the harness still has running, reported alongside the
		// state rather than folded into it: a finished turn that left a build
		// going is honestly "done", and "done · 1 shell" is the thing worth
		// knowing before deciding it needs nothing from you.
		hs := session.ReadHookStateAnyAge(a.sessionID)
		// With what the harness's own hook said it left running: the footer
		// counts only what it shows, the hook what it lists; each kind the
		// larger of the two (attn_background.go).
		shells, agents, other := attnBackground(attnShellsOnScreen(agentActive, render), attnOtherOnScreen(prog, agentActive, render), attnHookBackground(agentActive, hs, fgAt))
		a.noteShells(shells, agents)
		a.noteOther(other)
		// Whether the agent here can work at all — its login. Read from the
		// same screen, before the attention state, because a logged-out agent
		// takes a message, ends its turn and looks done; by the program that
		// runs here, and its own hook, never by any login text on the screen.
		atWork := harnessAtWork(hs, now, settledMs)
		a.noteHarnessHealth(render, prog, atWork)
		// And whether it is held back by its service's limit, from the same
		// rows (harnesslimit.go). A new reset time read off the same notice
		// is news for the record even when the state stays "limited".
		// When its hook last said a turn began: kept beside the state, as a
		// short turn's "done" overwrites its "working" before a read.
		// And when it said that turn ended: what a notice's reset time is
		// read against, and what says a turn begun at the limit was answered.
		// And what its hook said ended it: an error, in Stop's place.
		var turnAt, endAt time.Time
		var failure string
		if hs != nil {
			turnAt = hs.TurnAt
			if hs.State == "working" && hs.TS.After(turnAt) {
				turnAt = hs.TS // a hook too old to keep it
			}
			if hs.State != "working" {
				endAt, failure = hs.TS, hs.Failure
			}
		}
		a.noteTurn(turnAt)
		if a.noteHarnessLimit(harnessTail(stripANSI(render), harnessRowsFor(prog)), prog, atWork, turnAt, endAt, failure) {
			a.metaDirty.Store(true)
		}
		if prog != "" && hookSpokeIn(hs, fgAt) {
			hookSpoke[prog] = true
		}
		state, source := resolveAttn(screenState, hs, last, fgAt, idleMs,
			attnLooksLikePrompt(bottom), attnLooksBusy(bottom), hookTurns[prog], hookSpoke[prog])
		if a.harnessDown() {
			// It cannot work at all: that is what to report, not what it
			// looked like it was doing when it took the message.
			state, source = harnessLoggedOut, "screen(login)"
		} else if limited, _ := a.harnessLimitedNow(); limited {
			state, source = harnessLimited, "screen(limit)"
		}
		a.setAttnState(state)
		next, ok := attnNextRead(now, lastChange, last, hs, fgPgrpSeen == 0 && a.term != nil)
		// And when the login state waits on a read to settle (harnessReadAt):
		// a screen that has gone still must still clear it.
		for _, t := range []time.Time{a.harnessReadAt(), a.harnessLimitReadAt()} {
			if t.After(now) && (!ok || t.Before(next)) {
				next, ok = t.Add(time.Millisecond), true
			}
		}
		if ok {
			at(next)
		}

		if enc != nil {
			_ = enc.Encode(attentionProbeSample{
				TS: now.UnixMilli(), FG: fg, Alt: alt, Source: source,
				IdleMs: idleMs, SettledMs: settledMs, State: state, Tail: tail,
			})
		}
	}
}

// attnNextRead is when a reading made at now goes out of date with nothing
// else happening — no output, no resize, no hook — or false if it stays
// true until one of those comes:
//   - a changing screen settles attnSettleMs after its last change;
//   - output may be followed by a program taking the foreground, which says
//     nothing (the shell echoes the command, then starts it), so it is read
//     again once that output has settled too;
//   - a hook's "working" stops being believed past its TTL, or once the
//     screen has been still for hookWorkingSilentMs;
//   - a console whose foreground cannot be asked (Windows) is read at
//     attnInterval, as its program is found among the shell's children.
func attnNextRead(now, lastChange, lastActivity time.Time, hs *session.HookState, blindForeground bool) (time.Time, bool) {
	var next time.Time
	soonest := func(t time.Time) {
		if t.After(now) && (next.IsZero() || t.Before(next)) {
			next = t
		}
	}
	settle := time.Duration(attnSettleMs) * time.Millisecond
	soonest(lastChange.Add(settle))
	if !lastActivity.IsZero() {
		soonest(lastActivity.Add(settle))
	}
	if hs != nil && hs.State == "working" {
		soonest(hs.TS.Add(session.HookStateTTL))
		if !lastActivity.IsZero() {
			soonest(lastActivity.Add(hookWorkingSilentMs * time.Millisecond))
		}
	}
	if blindForeground {
		soonest(now.Add(attnInterval))
	}
	// A millisecond past the threshold, so the read lands after it, not on it.
	if next.IsZero() {
		return next, false
	}
	return next.Add(time.Millisecond), true
}

// hookWorkingSilentMs is how long a screen may stay completely still before a
// hook's "working" stops being believed.
const hookWorkingSilentMs = 90_000

// foregroundProgCache resolves the foreground PROGRAM — what a person would
// call the thing running there — once per (process group, command name), since
// on macOS it costs a fork.
type foregroundProgCache struct {
	pgrp       int
	comm, prog string
}

func (c *foregroundProgCache) resolve(pgrp int, comm string) string {
	if pgrp == c.pgrp && comm == c.comm {
		return c.prog
	}
	c.pgrp, c.comm, c.prog = pgrp, comm, foregroundProgram(pgrp, comm)
	return c.prog
}

// agentPrograms are the coding agents reminal knows by their command name.
//
// Being on this list is what tells reminal to read a session's SCREEN rather
// than its scrollback (see programScreenText). An agent that draws a TUI on the
// main screen — no alternate screen to give it away — reads as a plain terminal
// until it is named here, and `read_transcript` returns the stream it painted
// with instead of what the screen says.
var agentPrograms = map[string]bool{
	"claude": true, "cursor-agent": true, "codex": true, "gemini": true, "aider": true,
	"goose": true, "crush": true, "qwen": true, "opencode": true, "amp": true,
	"copilot": true, "droid": true, "cline": true, "kiro": true, "agy": true,
	"pi": true,
}

// isAgentProgram reports whether a command name is a known coding agent.
func isAgentProgram(name string) bool { return agentPrograms[name] }

// ambiguousPrograms are agent names common enough to appear in a command line
// that has nothing to do with the agent. They are matched only as the program
// being run — the kernel's name for it, or the first argument — never as a path
// component or a later argument, where "pi" also means pi.py, ./pi, -m pi, and
// any directory somebody called pi. Reading one of those as an agent would put
// the wrong program on the machines list and start dumping a plain script's
// screen into read_transcript.
var ambiguousPrograms = map[string]bool{"pi": true}

// npmPackagePrograms are the npm packages agents ship in, by the folder their
// scripts live under, and the program each is. Unambiguous as a path
// component: no other program lives in node_modules/@qwen-code/qwen-code.
var npmPackagePrograms = map[string]string{
	"claude-code":     "claude",
	"qwen-code":       "qwen",
	"gemini-cli":      "gemini",
	"pi-coding-agent": "pi",
	"opencode-ai":     "opencode",
}

// foregroundProgram names the program behind a command name. The kernel's
// name is often not it: Node renames its main thread, so cursor-agent shows up
// as "MainThread", and other agents run as plain "node" or "python3". The
// command line says what was actually launched.
func foregroundProgram(pid int, comm string) string {
	if pid <= 0 || isLoginShell(comm) || isAgentProgram(comm) {
		return comm
	}
	if prog := programFromArgs(processArgs(pid), comm); prog != "" {
		return prog
	}
	return comm
}

// programFromArgs picks the program out of a command line: a known agent named
// anywhere in the first few arguments (as a file or as a directory on the way
// to one), else argv[0] when the kernel's name is meaningless.
func programFromArgs(args []string, comm string) string {
	if len(args) > 4 {
		args = args[:4]
	}
	for i, arg := range args {
		if strings.HasPrefix(arg, "-") {
			continue
		}
		// Either separator, whatever the OS the record is read on.
		segs := strings.FieldsFunc(arg, func(r rune) bool { return r == '/' || r == '\\' })
		for _, seg := range segs {
			seg = strings.TrimSuffix(seg, filepath.Ext(seg))
			// On Windows npm's shim runs the package's own script —
			// …\@qwen-code\qwen-code\cli-entry.js — so the package folder
			// is where the program's name is; Unix's shim path ends in it.
			if p, ok := npmPackagePrograms[seg]; ok {
				return p
			}
			if !isAgentProgram(seg) {
				continue
			}
			if ambiguousPrograms[seg] && i != 0 {
				continue // a short name anywhere but argv[0] is probably a file
			}
			return seg
		}
	}
	if (comm == "" || comm == "MainThread") && len(args) > 0 {
		return filepath.Base(args[0])
	}
	return ""
}

// processArgs is a process's command line, each argument whole: /proc on
// Linux, the kernel's own copy on macOS and Windows (see processargs_*.go),
// ps elsewhere — where an argument with a space in it comes apart.
func processArgs(pid int) []string {
	if runtime.GOOS == "linux" {
		b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
		if err != nil {
			return nil
		}
		return strings.FieldsFunc(string(b), func(r rune) bool { return r == 0 })
	}
	if args, ok := processArgsNative(pid); ok {
		return args
	}
	out, err := exec.Command("ps", "-o", "args=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return nil
	}
	return strings.Fields(string(out))
}

// hookSpokeIn says hook state hs was written in the agent run that began at
// fgAt (any run, when that is not known): its hook speaks in this run.
func hookSpokeIn(hs *session.HookState, fgAt time.Time) bool {
	return hs != nil && (fgAt.IsZero() || !hs.TS.Before(fgAt))
}

// noteForeground records the foreground command and, when it changes, flushes
// the session record — `claude` starting or exiting is what turns a terminal
// into an agent and back, and `reminal list` should see it within a tick.
// attnHookBackground is the background work the harness's own hook said it
// left running, when that can be believed: said at rest (a turn that begins
// says nothing of it, and a new one takes its place), by the harness in the
// foreground now and not one that has since exited. nil otherwise, or when
// the hook said nothing about it.
func attnHookBackground(agentActive bool, hs *session.HookState, fgAt time.Time) *session.Background {
	if !agentActive || hs == nil || hs.Background == nil || hs.State == "working" {
		return nil
	}
	if !fgAt.IsZero() && hs.TS.Before(fgAt) {
		return nil
	}
	return hs.Background
}

// noteShells records how many shells and subagents the harness still has
// running, and flushes the record when that changes so `reminal list` sees it
// within a tick.
func (a *Agent) noteShells(n, agents int) {
	a.metaMu.Lock()
	changed := n != a.attnShells || agents != a.attnAgents
	a.attnShells, a.attnAgents = n, agents
	a.metaMu.Unlock()
	if !changed {
		return
	}
	a.metaDirty.Store(true)
	if a.metaKick != nil {
		select {
		case a.metaKick <- struct{}{}:
		default:
		}
	}
}

// noteForeground records the program in the foreground, and when it STARTED
// (attnFGAt): the kernel's start time of its process group's leader, when
// that can be read and it is not a shell, else now. A hook written before that program started is
// another program's word (resolveAttn) — but one written by the program that
// is still running is its own, however recently this process began watching.
// Taking "now" there made every hook stale after a restart of reminal itself,
// and the screen's guess stood in for the harness's word indefinitely.
func (a *Agent) noteForeground(fg string, pgrp int) {
	a.metaMu.Lock()
	prev := a.attnFG
	changed := fg != a.attnFG
	if _, was := resumers[a.attnFG]; changed && was {
		if _, still := resumers[fg]; !still {
			a.agentGoneAt = time.Now() // see typedToQuit
		}
	}
	if changed && a.attnFG != "" {
		a.attnPrevFG = a.attnFG
	}
	a.attnFG = fg
	if changed {
		a.attnFGAt = time.Now()
		// Not for a shell: a harness that exited leaves its login shell in the
		// foreground, which started before the harness wrote its last hook —
		// that hook must read as another program's, not the shell's.
		if t, ok := session.ProcStartTime(pgrp); ok && pgrp > 0 && fg != "" && !isLoginShell(fg) && t.Before(a.attnFGAt) {
			a.attnFGAt = t
		}
	}
	a.metaMu.Unlock()
	if !changed {
		return
	}
	a.noteAgentVersion(fg, pgrp) // what version the agent now in front is (hookheal.go)
	a.healHooksFor(prev, fg)     // its hooks as its version can run them (hookheal.go)
	// An agent came to the front, or left it: its restore record says so
	// now, not at the next timed save — a machine that goes down seconds
	// after an agent started must still bring the agent back.
	if isResumer(prev) || isResumer(fg) {
		go a.saveRestore()
	}
	a.metaDirty.Store(true)
	if a.metaKick != nil {
		select {
		case a.metaKick <- struct{}{}:
		default:
		}
	}
}

// setAttnState stores the state and, on a change, marks the record dirty and
// kicks an immediate meta-flush (same pattern as commitOscCwd) so `reminal list`
// reflects the new state within ~a tick instead of at the next slow flush.
func (a *Agent) setAttnState(state string) {
	if state == "done" || state == harnessLimited {
		// Before the state is written, and so before anything reads it
		// (a flush, the viewer's held keys): a no-op once taken.
		a.noteRestBase()
	}
	a.metaMu.Lock()
	changed := state != a.attnState
	a.attnState = state
	if changed {
		a.attnSince = time.Now()
		if state == "working" {
			a.attnTurnAt = a.attnSince
			a.noteFirstTurnLocked(a.attnSince)
		}
	}
	a.metaMu.Unlock()
	if !changed {
		return
	}
	a.metaDirty.Store(true)
	if a.metaKick != nil {
		select {
		case a.metaKick <- struct{}{}:
		default: // a kick is already pending — coalesce
		}
	}
	// Push the new state straight to the connected viewer so the pill for the
	// session it's watching updates immediately, instead of trailing the fleet
	// view's slower directory poll. On its own goroutine — gatherHostInfo
	// samples the machine and must not stall the detector tick. No-op when
	// there's no viewer.
	go a.pushHostInfo()
}

// resolveAttn decides the state a session reports, combining the screen verdict
// with the harness's own hook report. The hook is precise about WHAT happened;
// the screen and the output stream are the evidence of what is happening NOW,
// and they correct the hook where it cannot see:
//
//   - A resting hook state (input/done) claims the agent STOPPED. Terminal
//     output arriving well after that hook fired disproves it — the agent
//     resumed and the hook never said so. That is the gap no harness covers:
//     answering an in-chat question (AskUserQuestion, a tool permission)
//     continues the SAME turn without firing UserPromptSubmit, so "input" would
//     otherwise stick until Stop or the 15-minute TTL, showing "needs you" over
//     a session that is busily working. Output-after-hook needs no screen
//     inspection, so it is the one invalidator that also works on Windows, where
//     the foreground detector is blind. It is safe because an agent that is
//     genuinely waiting emits nothing — its prompt is painted and sits still, so
//     lastActivity freezes; attnHookGrace covers that prompt being drawn.
//
//   - A prompt visible on a settled screen beats a "done" hook. Claude Code
//     fires Stop (→ done) when it yields for a question, since it has no
//     distinct "I'm asking you" event — so the hook alone reads "done" while
//     you are in fact being asked to choose.
//
// source is for the probe log only; it names which signal decided.
//
// turnsByHook says the harness in front reports each turn's start by its own
// hook (hookTurns). For one that does, output after it said "done" or
// "needs you" is not a turn resuming: a turn comes with its "working", or
// shows on the busy footer. Its screen moves at rest all the same — a viewer
// connecting or resizing makes it repaint, its footer swaps one tip or
// notice for another — and each move read as "working", then "done": a turn
// nobody took, filed for review.
func resolveAttn(screenState string, hs *session.HookState, lastActivity, fgAt time.Time, idleMs int64,
	promptOnScreen, busyOnScreen, turnsByHook, spokeHere bool) (state, source string) {
	state, source = screenState, "screen"
	switch {
	case hs == nil:
	case !fgAt.IsZero() && hs.TS.Before(fgAt):
		// Written by a harness that has since exited; the one running now has
		// not said anything yet. A "working" left behind by a turn that died
		// (a failed login, a crash) held every message for this session until
		// the TTL ran out.
		//
		// Not said anything yet is itself a state, though, for a harness
		// whose hook marks each turn's start and has spoken in this session
		// before (spokeHere): it has had no turn. Its screen still moves —
		// it draws itself as it starts, and repaints at rest — and each move
		// read as "working", then "done": a turn nobody took, filed for
		// review, over and over, in a session where a second copy of the
		// harness was started and left at its prompt. Its first turn comes
		// with its own "working", or shows on the busy footer (below).
		if turnsByHook && spokeHere && screenState == "working" {
			state, source = "done", "screen(no turn yet)"
		}
	case hs.State == "working" && time.Since(hs.TS) > session.HookStateTTL:
		// A turn longer than this would have said something since. A resting
		// state (done, input) has no such limit: the harness that wrote it is
		// still the one in the foreground (above), and at rest it says nothing
		// more for as long as it is left there.
	case hs.State == "working" && idleMs > hookWorkingSilentMs:
		// A working harness animates; this screen has not moved at all. Its
		// "done" never came — believe the screen.
	default:
		state, source = hs.State, "hook"
		if !turnsByHook && (hs.State == "input" || hs.State == "done") && lastActivity.After(hs.TS.Add(attnHookGrace)) {
			state, source = screenState, "screen(resumed)"
			if state == "" {
				// No screen verdict (a bare-shell read, or Windows where the
				// foreground cannot be seen). Output is still arriving, so the one
				// thing we know is that something is running.
				state, source = "working", "output(resumed)"
			}
		}
	}
	// A prompt visible on screen wins over a "done" hook. Claude Code fires
	// Stop (→ done) when it yields for an AskUserQuestion or ends a turn on a
	// question — there's no distinct "I'm asking you" event — so the hook
	// alone reads "done" while you are actually being asked to choose. Only
	// the BOTTOM of the screen may overrule it: a chooser sits at the prompt,
	// while rows of transcript above it are full of agents discussing
	// approvals in prose.
	if state == "done" && source == "hook" && screenState == "input" && promptOnScreen {
		state, source = "input", "hook+screen"
	}
	// A harness that says, at the bottom of its screen, how to interrupt it
	// is mid-turn, whatever else has been read. Claude Code fires Stop (→
	// done) as soon as its words end, before a tool it called has finished —
	// and a long tool call may draw nothing for a while, so neither the hook
	// nor the screen's stillness says "working". The footer does.
	//
	// So does one answered at a permission prompt and carrying on: no hook
	// marks that, and the hook stays at "needs you" until the turn's Stop —
	// unless a prompt is still on screen, which is the question itself.
	if (state == "done" || (state == "input" && source == "hook" && !promptOnScreen)) && busyOnScreen {
		state, source = "working", "screen(busy)"
	}
	return state, source
}

// hookTurns are the harnesses whose own hook marks the start of every turn
// (`reminal integrate`: Claude Code's and Qwen's UserPromptSubmit, Gemini's
// BeforeAgent, Antigravity's PreInvocation, OpenCode's plugin), by the
// program name in front.
var hookTurns = map[string]bool{"claude": true, "opencode": true, "qwen": true, "gemini": true, "agy": true}

// attnBusyCues are lowercase substrings a harness draws at the bottom of its
// screen only while a turn is running. Claude Code's footer reads "esc to
// interrupt" from the prompt's submission to its Stop; at rest the same
// footer has no such words. Matched the way attnLooksLikePrompt matches —
// as text, with the styling and the spaces taken out.
var attnBusyCues = []string{"esc to interrupt", "ctrl+c to interrupt", "to run in background"}

// attnShellPattern finds a harness's count of the shells it still has running.
//
// Claude Code puts it in the footer as "· 1 shell" / "· 4 shells", and keeps it
// there after the turn has ended — which is the case worth reporting, since the
// hook says done while the work carries on. The leading separator is what makes
// this safe to match: transcripts are full of "Ran 2 shell commands", which is a
// count of commands already finished and means the opposite.
var attnShellPattern = regexp.MustCompile(`·\s*([0-9]+)\s*shells?\b`)

// attnFooter is the harness's footer at the bottom of the screen: the rows
// under its input box. The count is read there and nowhere above: what the
// agent wrote above its prompt — a turn's summary "· 1 shell still running"
// long after that shell ended, a status line "● Status · 3 shells · all
// green" — kept a session that had stopped reading as working on its shells
// for half an hour. The box is only one whose rules are seen as one (a
// prompt row just under its top rule, attnBoxBottom): a rule the agent drew
// in its own output — a markdown "---" rendered as dashes — is not one.
// tail may be taller than the footer and the box together (attnTailRows);
// with no box seen in it, its last attnPromptRows rows, as before.
func attnFooter(tail string) string {
	rows := strings.Split(strings.TrimRight(tail, "\n"), "\n")
	if i := attnBoxBottom(rows); i >= 0 {
		return strings.Join(rows[i+1:], "\n")
	}
	if len(rows) > attnPromptRows {
		rows = rows[len(rows)-attnPromptRows:]
	}
	return strings.Join(rows, "\n")
}

// attnBoxRows is the most rows an input box takes between its rules (a long
// line typed in wraps).
const attnBoxRows = 12

// attnBoxBottom is the row of the lowest input box's bottom rule: a rule row
// with another above it, within attnBoxRows, the row under that one a
// prompt's ("❯", or ">" as older versions draw it); -1 when there is none.
func attnBoxBottom(rows []string) int {
	for i := len(rows) - 1; i > 0; i-- {
		if !attnRuleRow(rows[i]) {
			continue
		}
		for j := i - 1; j >= 0 && i-j <= attnBoxRows+1; j-- {
			if attnRuleRow(rows[j]) {
				if j+1 < i && attnPromptRow(rows[j+1]) {
					return i
				}
				break
			}
		}
	}
	return -1
}

// attnRuleRow says whether a row is an input box's rule: box-drawing dashes
// across it, or a rounded box's top or bottom (╭──╮, ╰──╯) with its corners.
func attnRuleRow(row string) bool {
	r := strings.TrimSpace(stripANSI(row))
	if utf8.RuneCountInString(r) < 10 {
		return false
	}
	r = strings.TrimRight(strings.TrimLeft(r, "╭╰"), "╮╯")
	return r != "" && strings.Trim(r, "─━") == ""
}

// attnPromptRow says whether a row is an input box's first: its prompt,
// inside a rounded box's side (│) where it has one.
func attnPromptRow(row string) bool {
	r := strings.TrimSpace(stripANSI(row))
	r = strings.TrimSpace(strings.TrimPrefix(r, "│"))
	return strings.HasPrefix(r, "❯") || strings.HasPrefix(r, ">")
}

// attnShellsOnScreen is the count of shells still running a screen's footer
// shows (attnShellCount), its input box looked for in the taller tail: a box
// of a few rows (a long line typed in wraps) over a footer of a few does not
// fit in the prompt rows, and what was typed in the box was read as the
// footer's count.
func attnShellsOnScreen(agentActive bool, render string) int {
	return attnShellCount(agentActive, attentionProbeTail(render, attnTailRows))
}

// attnShellCount reads that count off the harness's footer, or 0.
//
// agentActive is what makes the reading trustworthy, and it is not a detail: a
// count is only as live as the program that drew it. When a harness exits, its
// last footer stays in the scrollback until something scrolls past it, so a
// session sitting back at its own shell would go on reporting the shells the
// dead harness had — the stuck pill again, in the count instead of the state.
// No agent in the foreground, no count, whatever is still painted.
func attnShellCount(agentActive bool, tail string) int {
	if !agentActive {
		return 0
	}
	t := strings.ToLower(stripANSI(attnFooter(tail)))
	// By match position, not by searching for the matched text: two matches can
	// render the identical string, and looking the text up again finds the first
	// one every time. A footer under a decoy of the same shape was read against
	// the decoy's suffix and thrown away, leaving the session with no count.
	for _, loc := range attnShellPattern.FindAllStringSubmatchIndex(t, -1) {
		// "· 2 shell commands" would be a report of finished work, not of work
		// still running; the footer never says that, but a transcript line ending
		// in the same shape could.
		if strings.HasPrefix(strings.TrimSpace(t[loc[1]:]), "command") {
			continue
		}
		if n, err := strconv.Atoi(t[loc[2]:loc[3]]); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// attnLooksBusy reports whether the bottom of the screen says a turn is
// running.
func attnLooksBusy(tail string) bool {
	t := strings.ToLower(stripANSI(tail))
	flat := strings.Join(strings.Fields(t), "")
	for _, cue := range attnBusyCues {
		if strings.Contains(t, cue) || strings.Contains(flat, strings.ReplaceAll(cue, " ", "")) {
			return true
		}
	}
	return false
}

// classifyAttn maps the raw signals to an attention state:
//   - ""        nothing running in the foreground (a bare shell) — no badge
//   - "working" the foreground program's screen is actively changing
//   - "input"   the screen has settled on an approval/question prompt
//   - "done"    the screen has settled with no prompt cue
//
// agentActive is "a program (agent) is in the foreground" — see the caller.
func classifyAttn(agentActive bool, tail string, settledMs int64) string {
	if !agentActive {
		return ""
	}
	if settledMs < attnSettleMs {
		return "working"
	}
	if attnLooksLikePrompt(tail) {
		return "input"
	}
	return "done"
}

// classifyAttnFor is classifyAttn for the program prog, in front for sinceFG,
// drawn already (its prompt seen since it came to the front) or not: a
// screen without its prompt is not done. Before it has drawn, that is prog
// still starting, for at most attnStartCap; then the screen is read as any
// program's (one that never draws, hung, is not "at work" for ever). Once it
// has drawn, a screen without its prompt is a dialog over it (the model
// list, the command menu): at work, and nothing is typed into it.
func classifyAttnFor(prog string, agentActive bool, tail string, settledMs int64, sinceFG time.Duration, drawn bool) string {
	st := classifyAttn(agentActive, tail, settledMs)
	if st == "done" && !attnPromptDrawn(prog, tail) && (drawn || sinceFG < attnStartCap) {
		return "working"
	}
	return st
}

// attnStartCap is how long a program in attnPromptMarks may take to draw its
// prompt before its screen is read as any program's. OpenCode starting on a
// quarter of a CPU took 20 to 35 s; this is several times that.
const attnStartCap = 2 * time.Minute

// attnPromptMarks are, by program, a row its screen has whenever it is at its
// prompt: for agents that sit a long while before they draw anything. OpenCode
// starting on a small machine (a quarter of a CPU) leaves the screen as the
// shell left it for 20 to 35 s, its command line the last row — settled, so
// read as done — and keys typed then are lost: a message sent it then was
// never seen. Its prompt box ends in a row of its own ("╹▀▀▀…"), in its home
// view and in a conversation, at any width. Its dialogs (the model list, the
// command menu) are drawn over that row: it reads as at work while one is
// open, and nothing is typed into it.
var attnPromptMarks = map[string]*regexp.Regexp{
	"opencode": regexp.MustCompile(`(?m)^\s*╹▀{8,}`),
}

// attnPromptDrawn reports whether prog's screen shows it at its prompt, as far
// as its mark tells (always, for a program with none).
func attnPromptDrawn(prog, tail string) bool {
	re := attnPromptMarks[prog]
	return re == nil || re.MatchString(stripANSI(tail))
}

// attnPromptCues are lowercase substrings that mark a screen asking the user to
// act — tool/permission approvals and interactive questions across harnesses.
// Kept deliberately small and data-like so a harness UI change is a one-line fix.
// Every cue has to be PROMPT-SHAPED, not a word a prompt might use. Bare
// "confirm" matched "this just confirms the build is green" in an agent's own
// prose, and bare "approve" matched "Approve only what the task requires" in
// text it was reading — so a session that had plainly finished sat there
// reading "needs you". Agents talk about approvals constantly; only the shape
// of an actual question can be trusted.
var attnPromptCues = []string{
	"(y/n)", "[y/n]", "y/n)", "yes/no", "(y)",
	"do you want to", "would you like to", "allow this", "approve?", "approve this",
	"needs your permission", "permission to use", "permission to run",
	"proceed?", "continue?", "confirm?", "press enter", "press any key",
	"waiting for your", "1. yes", "1) yes", "❯ 1.", "> 1.",
	// Claude Code's interactive choosers (AskUserQuestion, permission prompts)
	// carry these footer hints under a numbered/❯ option list. NOT "esc to
	// interrupt" — that's the WORKING spinner's footer, not a prompt.
	"enter to select", "esc to cancel",
	// cursor-agent's approval prompt. It has no lifecycle hooks, so the screen
	// is the ONLY signal it gives, and none of the cues above appear on it —
	// which made a session stuck on this read as "done". NOT "run everything":
	// that is also the label of its yolo mode, printed in the footer of every
	// screen, so a session launched with -f read as stuck from its first second.
	"run this command?", "run (once)", "not in allowlist", "(esc or n)",
	// Gemini CLI's first screen in a folder it has not seen: whether to trust
	// it, a choice only the person can make (its options are marked "●", not
	// "❯", so no cue above matched and the session read as done). By its
	// question, and by its first choice, which stays in the rows read when
	// the question has scrolled above them.
	"do you trust the files in this folder?", "1. trust folder (",
	// Claude Code's first screen on a machine it has not run on: its theme
	// picker ("Let's get started. Choose the text style…"), a choice only the
	// person can make. Its options sit above a dashed preview, so the only
	// row under that last rule is the preview's own footer, matched by its
	// hint: shown by the theme picker alone (its /theme command's too, which
	// asks the same of the person), whether the highlighting is on or off.
	"(ctrl+t to disable)", "(ctrl+t to enable)",
}

// The screen is read as text: the render carries its styling, and Claude
// Code's trust-this-folder dialog reads "\x1b[38;5;246mEsc\x1b[m
// \x1b[38;5;246mto…" — no cue matched it, the session read as done, and a
// message typed into it confirmed "No, exit". Matched with whitespace
// removed from both, too: some screens draw the spaces as cursor moves.
//
// Only the harness's own question counts, not its conversation. A harness at
// rest draws its empty input box at the bottom (attnPromptRegion), and every
// row above that box is what the agent said — "press Enter once the typed
// text appears", "do you want to…" — so the cues are looked for below it
// only. A chooser or a permission prompt replaces the box, and is read whole.
func attnLooksLikePrompt(tail string) bool {
	low := attnFlat(stripANSI(attnPromptRegion(tail)))
	for _, cue := range attnPromptCuesFlat {
		if strings.Contains(low, cue) {
			return true
		}
	}
	return false
}

// attnPromptRegion is the part of the screen's bottom rows a prompt can be
// in. When they end in a harness's own input box — Claude Code's
// "────" / "❯ …" / "────", Gemini's "╭──╮" / "│ > … │" / "╰──╯" — the agent
// is waiting at its prompt, and only the rows below the box (its footer) are
// returned; the rows above are the agent's own words. A box whose top rule
// has scrolled out of the rows still counts when its prompt line is empty.
// Anything else — no box, a chooser drawn instead of one — comes back whole.
func attnPromptRegion(tail string) string {
	rows := strings.Split(stripANSI(tail), "\n")
	last := -1
	for i := len(rows) - 1; i >= 0; i-- {
		if attnIsRule(rows[i]) {
			last = i
			break
		}
	}
	if last <= 0 {
		return tail
	}
	footer := strings.Join(rows[last+1:], "\n")
	// The box's prompt line, just above its bottom rule: empty, the box is
	// at rest even if its top rule is out of view.
	if attnIsEmptyInput(rows[last-1]) {
		return footer
	}
	for top := last - 1; top >= 0 && last-top <= attnInputBoxRows; top-- {
		if attnIsRule(rows[top]) {
			if top+1 < last && attnIsInputLine(rows[top+1]) {
				return footer
			}
			break
		}
	}
	return tail
}

// attnInputBoxRows bounds how tall an input box with text typed into it is
// taken to be.
const attnInputBoxRows = 8

// attnIsRule: a row drawn as a horizontal line (box-drawing characters, at
// least a few of them, and nothing else).
func attnIsRule(row string) bool {
	n := 0
	for _, r := range strings.TrimSpace(row) {
		switch r {
		case '─', '━', '═', '╌', '╍', '┄', '┅', '-':
			n++
		case '╭', '╮', '╰', '╯', '┌', '┐', '└', '┘':
		default:
			return false
		}
	}
	return n >= 8
}

// attnInputLine strips what frames an input line: a box's side bars, spaces
// (non-breaking ones too).
func attnInputLine(row string) string {
	return strings.TrimSpace(strings.Trim(strings.ReplaceAll(row, "\u00a0", " "), " │┃|"))
}

// attnIsInputLine: the row starts with a prompt marker, typed text or not.
// A chooser's option ("❯ 1. Yes") is not one.
func attnIsInputLine(row string) bool {
	l := attnInputLine(row)
	for _, m := range []string{"❯", ">", "›"} {
		if strings.HasPrefix(l, m) {
			rest := strings.TrimSpace(strings.TrimPrefix(l, m))
			return !attnOptionRe.MatchString(rest)
		}
	}
	return false
}

// attnIsEmptyInput: a prompt marker and nothing else.
func attnIsEmptyInput(row string) bool {
	switch attnInputLine(row) {
	case "❯", ">", "›":
		return true
	}
	return false
}

// attnOptionRe is a numbered option, as a chooser draws them.
var attnOptionRe = regexp.MustCompile(`^[0-9]+[.)]`)

func attnFlat(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), "")) }

var attnPromptCuesFlat = func() []string {
	out := make([]string, len(attnPromptCues))
	for i, c := range attnPromptCues {
		out[i] = attnFlat(c)
	}
	return out
}()

// attentionProbeTail returns the last n non-blank rows of a rendered screen,
// each right-trimmed (so trailing-space padding never counts as a change) and
// joined by "\n". Trailing empty rows are dropped first so the "tail" tracks the
// actual bottom of the content, not the bottom of the padded viewport.
func attentionProbeTail(render string, n int) string {
	rows := strings.Split(render, "\n")
	for i := range rows {
		rows[i] = strings.TrimRight(rows[i], " ")
	}
	end := len(rows)
	for end > 0 && rows[end-1] == "" {
		end--
	}
	rows = rows[:end]
	if len(rows) > n {
		rows = rows[len(rows)-n:]
	}
	return strings.Join(rows, "\n")
}

// isLoginShell reports whether a foreground command name is an interactive
// shell — i.e. the session is sitting at the prompt with nothing launched. A
// login shell shows up with a leading '-' in argv[0] ("-zsh"), so strip it first.
func isLoginShell(name string) bool {
	switch strings.TrimPrefix(name, "-") {
	case "sh", "bash", "zsh", "fish", "dash", "ash", "ksh", "tcsh", "csh", "pwsh", "powershell":
		return true
	}
	return false
}

// attentionForegroundName resolves a pid to its short command name: /proc/<pid>/comm
// on Linux (no fork), `ps -o comm=` elsewhere. Empty string if it can't be read.
func attentionForegroundName(pid int) string {
	if pid <= 0 {
		return ""
	}
	if runtime.GOOS == "linux" {
		if b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/comm"); err == nil {
			return strings.TrimSpace(string(b))
		}
	}
	out, err := exec.Command("ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	name := strings.TrimSpace(string(out))
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:] // ps may print the full path; keep the basename
	}
	return name
}
