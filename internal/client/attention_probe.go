package client

import (
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/reminal/reminal/internal/session"
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
	// A screen unchanged for at least this long counts as "settled". Agent
	// spinners/elapsed-time counters repaint faster than this, so an actively
	// working agent stays "working"; a blocked or finished one goes settled.
	attnSettleMs = 1200
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
	go a.runAttention(os.Getenv("REMINAL_ATTENTION_PROBE"))
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

func (a *Agent) runAttention(logPath string) {
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

	for range ticker.C {
		a.screenMu.Lock()
		scr := a.screen
		if scr == nil { // snapshot disabled / torn down — nothing to read
			a.screenMu.Unlock()
			return
		}
		alt := scr.IsAltScreen()
		render := scr.Render()
		a.screenMu.Unlock()

		now := time.Now()
		tail := attentionProbeTail(render, attnTailRows)
		if tail != lastTail {
			lastTail = tail
			lastChange = now
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
		if a.term != nil {
			if fgPgrp := a.term.ForegroundPgrp(); fgPgrp > 0 {
				fg = attentionForegroundName(fgPgrp)
				if fg != "" && !isLoginShell(fg) {
					agentActive = true
				} else if shell := a.term.Pid(); shell > 0 && fgPgrp != shell {
					agentActive = true
				}
			}
		}

		// Prefer the agent's own hook-reported state when it's fresh (an
		// integrated harness reporting via `reminal hook`); otherwise fall back to
		// the screen inference. The hook is precise; the screen is universal.
		state := classifyAttn(agentActive, tail, settledMs)
		source := "screen"
		if hs := session.ReadHookState(a.sessionID); hs != nil {
			state, source = hs.State, "hook"
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

// setAttnState stores the state and, on a change, marks the record dirty and
// kicks an immediate meta-flush (same pattern as commitOscCwd) so `reminal list`
// reflects the new state within ~a tick instead of at the next slow flush.
func (a *Agent) setAttnState(state string) {
	a.metaMu.Lock()
	changed := state != a.attnState
	a.attnState = state
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
var attnPromptCues = []string{
	"(y/n)", "[y/n]", "y/n)", "yes/no",
	"do you want", "would you like", "allow this", "approve",
	"proceed?", "continue?", "confirm", "press enter", "press any key",
	"waiting for your", "1. yes", "1) yes", "❯ 1.", "> 1.",
}

func attnLooksLikePrompt(tail string) bool {
	low := strings.ToLower(tail)
	for _, cue := range attnPromptCues {
		if strings.Contains(low, cue) {
			return true
		}
	}
	return false
}

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
