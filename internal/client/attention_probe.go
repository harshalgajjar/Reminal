package client

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

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
	a.attnMu.Unlock()
	go a.runAttention(os.Getenv("REMINAL_ATTENTION_PROBE"), stop)
}

// stopAttention ends the detector: the session is going, and a probe that
// outlives its PTY polls a closed file (a data race the tests caught).
func (a *Agent) stopAttention() {
	a.attnMu.Lock()
	defer a.attnMu.Unlock()
	if a.attnStop != nil {
		close(a.attnStop)
		a.attnStop = nil
	}
}

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

	ticker := time.NewTicker(attnInterval)
	defer ticker.Stop()

	var lastTail string
	lastChange := time.Now()
	var progCache foregroundProgCache

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
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
		// Whether the agent here can work at all — its login. Read from the
		// same screen, before the attention state, because a logged-out agent
		// takes a message, ends its turn and looks done.
		a.noteHarnessHealth(render)
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
		screenState := classifyAttn(agentActive, tail, settledMs)
		// The hook is precise; the screen is universal. resolveAttn puts the
		// two together — including the cases where the hook is not to be
		// believed: written by a harness that has since exited, left at
		// "working" by a turn that died, or overtaken by output since.
		a.metaMu.Lock()
		fgAt := a.attnFGAt
		a.metaMu.Unlock()
		bottom := attentionProbeTail(render, attnPromptRows)
		// How many shells the harness still has running, reported alongside the
		// state rather than folded into it: a finished turn that left a build
		// going is honestly "done", and "done · 1 shell" is the thing worth
		// knowing before deciding it needs nothing from you.
		a.noteShells(attnShellCount(agentActive, bottom))
		state, source := resolveAttn(screenState, session.ReadHookStateAnyAge(a.sessionID), last, fgAt, idleMs,
			attnLooksLikePrompt(bottom), attnLooksBusy(bottom), hookTurns[prog])
		if a.harnessDown() {
			// It cannot work at all: that is what to report, not what it
			// looked like it was doing when it took the message.
			state, source = harnessLoggedOut, "screen(login)"
		}
		a.setAttnState(state)

		if enc != nil {
			_ = enc.Encode(attentionProbeSample{
				TS: now.UnixMilli(), FG: fg, Alt: alt, Source: source,
				IdleMs: idleMs, SettledMs: settledMs, State: state, Tail: tail,
			})
		}
	}
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

// noteForeground records the foreground command and, when it changes, flushes
// the session record — `claude` starting or exiting is what turns a terminal
// into an agent and back, and `reminal list` should see it within a tick.
// noteShells records how many shells the harness still has running, and flushes
// the record when it changes so `reminal list` sees it within a tick.
func (a *Agent) noteShells(n int) {
	a.metaMu.Lock()
	changed := n != a.attnShells
	a.attnShells = n
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
	changed := fg != a.attnFG
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
	a.metaMu.Lock()
	changed := state != a.attnState
	a.attnState = state
	if changed {
		a.attnSince = time.Now()
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
	promptOnScreen, busyOnScreen, turnsByHook bool) (state, source string) {
	state, source = screenState, "screen"
	switch {
	case hs == nil:
	case !fgAt.IsZero() && hs.TS.Before(fgAt):
		// Written by a harness that has since exited; the one running now has
		// not said anything yet. A "working" left behind by a turn that died
		// (a failed login, a crash) held every message for this session until
		// the TTL ran out.
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
// BeforeAgent, Antigravity's PreInvocation), by the program name in front.
var hookTurns = map[string]bool{"claude": true, "qwen": true, "gemini": true, "agy": true}

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

// attnShellCount reads that count off the bottom of the screen, or 0.
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
	t := strings.ToLower(stripANSI(tail))
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
