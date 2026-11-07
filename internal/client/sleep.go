// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"reminal/internal/proc"
	"reminal/internal/protocol"
	"reminal/internal/session"
)

// Sleep: a session that ends its processes the way a machine shutdown does,
// its restore record kept, and comes back on the same id, PIN and
// conversation when it is woken (RestoreSessionWith, the path a reboot
// takes). Its memory is freed meanwhile. It sleeps only when it can come
// back to exactly where it was, and never while something is going on in
// it: `reminal sleep` says why not.

// sleepFacts is what decides whether a session may sleep now.
type sleepFacts struct {
	Pinned    bool     // the person asked it to stay awake
	Headless  bool     // started by reminal (not a terminal tab, which is the session)
	Attn      string   // its agent's attention state
	Shells    int      // background shells its agent said it left running
	Agents    int      // background subagents
	Other     int      // the rest of its background work, in all (monitors, background tasks, ...)
	Scheduled int      // jobs its agent set to run later (session.Scheduled): they die with it
	BgSaid    bool     // what its agent left running is known (none, too): said by its hook, or seen on its terminal
	Draft     bool     // something typed on its input line, not sent
	Jobs      []string // other programs running in its shell
	Strays    []string // programs left running in the background on its terminal (leftRunning)
	Attached  int      // `reminal connect` terminals attached to it
	Fg        string   // its coding agent, as the saved record names it
	Conv      string   // the agent's conversation, as saved
	ConvThere bool     // its agent has Conv (convThere)
	NoTurn    bool     // the agent was never sent a message (nothing to resume)
	Turned    bool     // its agent, the one in front, had a turn since it started, or can't say (no hook in this run)
	Saved     bool     // its record was just saved
}

// sleepVerdict says whether a session may sleep now, in the words its card
// and `reminal sleep` use. block is a reason it can't, whatever the person
// says: it would not come back as itself. losses are what a sleep would end
// that a wake can't bring back: a job running in it, an unfinished turn. It
// sleeps with none, or with them when the person says "sleep anyway", and
// its row then says what that let go of. Never lost silently.
func sleepVerdict(f sleepFacts) (block string, losses []string) {
	switch {
	case f.Pinned:
		return "kept awake · you asked (reminal keep-awake --off)", nil
	case !f.Headless:
		// The terminal tab is the session: closing it is ending it.
		return "kept awake · open in a terminal", nil
	case !f.Saved:
		return "kept awake · couldn't save", nil
	case f.Fg != "" && !sleepsExactly(f.Fg):
		return "kept awake · " + f.Fg + " can't be resumed exactly yet", nil
	case f.Fg != "" && !f.NoTurn && !session.ValidConv(f.Conv):
		return "kept awake · can't tell which conversation is its own", nil
	case f.Fg != "" && !f.NoTurn && !f.ConvThere:
		// The wake checks for it too (wakeCheck): never asleep on a
		// conversation it could not be woken on.
		return "kept awake · can't find " + f.Fg + "'s conversation " + f.Conv, nil
	}
	switch f.Attn {
	case "working":
		losses = append(losses, "the agent's unfinished turn")
	case "input":
		losses = append(losses, "the question it was waiting on you for")
	}
	switch {
	case f.Shells > 0 || f.Agents > 0 || f.Other > 0:
		losses = append(losses, "its agent's background work")
	case f.Fg != "" && !f.NoTurn && f.Turned && !f.BgSaid:
		// An agent that ran a turn and never said what it left running may
		// have left something. One with no turn since it started (just
		// woken) has left nothing: what ran before went with its last run.
		losses = append(losses, "any background work its agent left (it didn't say)")
	}
	if f.Scheduled > 0 {
		// Not work running, and no reason to stay up for: a wake does not
		// bring it back, so it is said, as what a sleep would drop.
		job := "job"
		if f.Scheduled != 1 {
			job = "jobs"
		}
		losses = append(losses, fmt.Sprintf("its %d scheduled %s, which won't fire", f.Scheduled, job))
	}
	if f.Draft {
		losses = append(losses, "what was typed and not sent")
	}
	for _, j := range f.Jobs {
		losses = append(losses, j+", running in it")
	}
	for _, s := range f.Strays {
		losses = append(losses, s+", left running in the background")
	}
	if f.Attached > 0 {
		losses = append(losses, "the terminal attached to it")
	}
	if f.Fg == "" {
		// A shell's typed-by-hand state (variables, a cd, an alias) is not in
		// its record; it comes back a new shell in its folder.
		losses = append(losses, "what was typed into its shell by hand (variables, aliases)")
	}
	return "", losses
}

// sleepsExactly says agent fg comes back on exactly its own conversation:
// resumed by the id it reported (resumers' byID), the conversation's file
// checked at sleep and at wake (its store). Others ("latest in this
// folder") would wake on whatever ran there last.
func sleepsExactly(fg string) bool {
	rs := resumers[fg]
	return rs.byID != nil && rs.store.known()
}

// ownTurn says which of the processes an agent started are its own helpers
// (its MCP servers), not work: those started before its first turn (turn;
// zero when it has had none), when that is known — its hook speaks in this
// run. Not known: none are.
type ownTurn struct {
	known bool
	turn  time.Time
}

// hookSpeaks says the agent run in front (begun at fgAt) has heard from its
// hook: a turn's state (hs), or that it is up and reading its input
// (SessionStart, kept apart from a turn's state).
func (a *Agent) hookSpeaks(hs *session.HookState, fgAt time.Time) bool {
	if hookSpokeIn(hs, fgAt) {
		return true
	}
	ready := session.HookReadyAt(a.sessionID)
	return !ready.IsZero() && !ready.Before(fgAt)
}

// saysBackground are the agents whose hook says, at a turn's end, what it
// left running in the background (claude's Stop). For the others what is
// left running is read off the terminal alone (leftRunning).
var saysBackground = map[string]bool{"claude": true}

// ErrWouldLose is a sleep refused for what it would end (Losses); sleeping
// anyway lets those go.
type ErrWouldLose struct{ Losses []string }

func (e ErrWouldLose) Error() string {
	return "awake · sleeping would end " + strings.Join(e.Losses, "; ")
}

// sleepNow puts this session to sleep, or says why not (control "sleep",
// "sleep anyway"). It saves its record first and checks the save: the record
// is what it wakes from.
func (a *Agent) sleepNow(anyway bool) error {
	asked := time.Now()
	hs := session.ReadHookStateAnyAge(a.sessionID)
	a.metaMu.Lock()
	f := sleepFacts{
		Pinned: session.AwakePinned(a.sessionID), Headless: a.headless,
		Attn: a.attnState, Shells: a.attnShells, Agents: a.attnAgents,
		Other:     session.CountOther(a.attnOther),
		Scheduled: a.attnScheduled.Jobs(),
		Draft:     a.typing.draft,
	}
	fgAt := a.attnFGAt
	var base map[procKey]bool
	if a.restBaseFG.Equal(fgAt) {
		base = a.restBase
	}
	// Its first turn, as its hook says, when its hook speaks in this run (the
	// screen takes an agent starting up for a turn, and misses some): then
	// it has had none when that is zero. Without its hook that can't be told.
	own := ownTurn{known: a.hookSpeaks(hs, fgAt), turn: a.hookTurn.of(fgAt)}
	f.Turned = !own.known || !own.turn.IsZero()
	a.metaMu.Unlock()
	a.currentConnMu.Lock()
	f.Attached = len(a.localConns)
	a.currentConnMu.Unlock()
	before := time.Now()
	a.saveRestore()
	r, err := session.ReadRestore(a.sessionID)
	f.Saved = err == nil && r != nil && !r.SavedAt.Before(before)
	if f.Saved {
		f.Fg, f.Conv, f.NoTurn = r.Fg, r.Conv, r.NoTurn
		f.ConvThere, _ = convThere(f.Fg, f.Conv, r.Cwd)
	}
	// An agent that is not running (quit, something else in front) has no
	// background work of its own left to lose.
	f.BgSaid = true
	if a.term != nil {
		// After the save: only the agent it wakes with is not a job (a record
		// can hold an agent that was quit, with something else now in front).
		f.Jobs = otherJobs(a.term, f.Fg)
		var seen bool
		f.Strays, seen = leftRunning(a.term, f.Fg, base, own)
		if prog, _, _, _ := restoreForeground(a.term); f.Fg != "" && prog == f.Fg {
			// Said by its hook where it says (claude's), else read off its
			// terminal, which some machines cannot read.
			f.BgSaid = seen
			if saysBackground[f.Fg] {
				f.BgSaid = attnHookBackground(true, hs, fgAt) != nil
			}
		}
	}
	block, losses := sleepVerdict(f)
	if block != "" {
		return errors.New(block)
	}
	if len(losses) > 0 && !anyway {
		return ErrWouldLose{losses}
	}
	if err := session.MarkAsleep(a.sessionID, session.Asleep{SleptAt: time.Now(), Why: "asked", Fg: r.Fg, Dropped: losses}); err != nil {
		return fmt.Errorf("kept awake · couldn't save: %w", err)
	}
	// Going: no new terminal attaches from here. Keys that came while it
	// decided were someone using it: it stays awake, and says why.
	if !a.goToSleep(asked) {
		session.ClearAsleep(a.sessionID)
		return errors.New("awake · used while going to sleep")
	}
	return nil
}

// goToSleep marks this session gone to sleep, unless keys were typed into it
// (by a person, or for one) after asked: then it stays awake (false). One
// step under metaMu, as asleep reads it: every key is either seen here,
// keeping it awake, or comes after and is not typed in.
func (a *Agent) goToSleep(asked time.Time) bool {
	a.metaMu.Lock()
	defer a.metaMu.Unlock()
	if a.typing.at.After(asked) {
		return false
	}
	a.sleeping.Store(true)
	return true
}

// asleep says this session has gone to sleep (goToSleep): keys stamped
// before are in, ones after are not typed in.
func (a *Agent) asleep() bool {
	a.metaMu.Lock()
	defer a.metaMu.Unlock()
	return a.sleeping.Load()
}

// SleepSession puts this machine's running session id to sleep, and waits
// for it to have ended (`reminal sleep`, and Sleep in the viewer). A refusal
// for what the sleep would end is an ErrWouldLose; anyway lets that go.
func SleepSession(id string, anyway bool) error {
	a, err := session.ReadActiveByID(id)
	if err != nil {
		return fmt.Errorf("no session %s running here", strings.ToUpper(id))
	}
	cmd := "sleep"
	if anyway {
		cmd = "sleep anyway"
	}
	if _, err := sendControlToDeadline(a.PID, cmd, 20*time.Second); err != nil {
		msg := strings.TrimPrefix(err.Error(), "agent: ")
		if js, ok := strings.CutPrefix(msg, "lose:"); ok {
			var losses []string
			if json.Unmarshal([]byte(strings.TrimSpace(js)), &losses) == nil {
				return ErrWouldLose{losses}
			}
		}
		return errors.New(msg)
	}
	// It said yes, has saved its record and marked itself asleep, and is
	// ending itself. If its way out hangs (a session with a terminal attached
	// can be held up closing its terminal), it is ended from outside: first
	// asked (SIGTERM), then for sure. Either way its record and mark stay,
	// so it wakes the same; ending it hangs up its shell, as a shutdown does.
	gone := func(d time.Duration) bool {
		for deadline := time.Now().Add(d); !exited(a.PID) && time.Now().Before(deadline); {
			time.Sleep(100 * time.Millisecond)
		}
		return exited(a.PID)
	}
	if gone(sleepExitWait) {
		return nil
	}
	_ = proc.Terminate(a.PID)
	if gone(3 * time.Second) {
		return nil
	}
	_ = proc.Kill(a.PID)
	if gone(3 * time.Second) {
		return nil
	}
	return fmt.Errorf("%s said it would sleep but is still running (PID %d)", a.ID, a.PID)
}

// sleepExitWait is how long a session saying it will sleep is given to end
// itself before it is ended from outside.
const sleepExitWait = 5 * time.Second

// sleepAct does what an owner asked of session id from another device
// (dir_sleep): "sleep", "sleep_anyway", "wake", "keep_awake",
// "keep_awake_off". A wake answers with the last screen it saved, for the
// viewer to show while it comes up.
func sleepAct(id, act string) (screen string, err error) {
	id = strings.ToUpper(strings.TrimSpace(id))
	switch act {
	case "sleep", "sleep_anyway":
		return "", SleepSession(id, act == "sleep_anyway")
	case "wake":
		if !session.IsAsleep(id) {
			if SessionRunning(id) {
				return "", nil // awake already: nothing to do
			}
			return "", fmt.Errorf("no session %s asleep here", id)
		}
		r, err := session.ReadRestore(id)
		if err != nil {
			return "", fmt.Errorf("%s can't wake: its record can't be read: %w", id, err)
		}
		if text, _, ok := sleeperTranscript(id); ok {
			screen = lastLines(text, lastScreenRows)
		}
		_, err = WakeSession(*r)
		return screen, err
	case "keep_awake", "keep_awake_off":
		// Only a session there is: no mark left for an id named at random.
		if !SessionRunning(id) && !session.IsAsleep(id) {
			return "", fmt.Errorf("session %s not found", id)
		}
		return "", session.SetAwakePin(id, act == "keep_awake")
	}
	return "", fmt.Errorf("unknown sleep action %q", act)
}

// lastScreenRows is how much of a sleeping session's saved scrollback a
// viewer is shown while it wakes: about a phone's screen.
const lastScreenRows = 40

// lastLines is the last n lines of s.
func lastLines(s string, n int) string {
	s = strings.TrimRight(s, "\n")
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// wakeCheck is why sleeping session r can't wake exactly, or "": its folder
// is there, and the conversation it slept in still is.
func wakeCheck(r session.Restore) string {
	if r.Cwd != "" {
		if fi, err := os.Stat(r.Cwd); err != nil || !fi.IsDir() {
			return "can't wake · its folder is gone (" + r.Cwd + ")"
		}
	}
	// Never a second process in one conversation: one open in a live session
	// (continued there while this one slept) is that session's now.
	if r.Conv != "" {
		peers, _ := session.ReadRestores()
		for _, p := range peers {
			live, _ := session.ReadConvAt(p.ID) // its hook's word, newer than its record
			if p.ID != r.ID && p.Fg == r.Fg && (p.Conv == r.Conv || live == r.Conv) && SessionRunning(p.ID) {
				return "can't wake · its conversation is open in session " + p.ID
			}
		}
	}
	// Gone only when that can be told: an agent whose files are where this
	// process can't look, or that could not be asked, says so itself as it
	// starts, never starting afresh.
	if there, known := convThere(r.Fg, r.Conv, r.Cwd); r.Fg != "" && !r.NoTurn && known && !there {
		return "can't wake · " + r.Fg + "'s conversation " + r.Conv + " is gone"
	}
	return ""
}

// ForgetSleeper ends sleeping session id for good: its record and marks go
// (`reminal kill`, End in the viewer). Never while a wake of it is under way,
// which may be bringing it up on that record: it is ended once it is up.
func ForgetSleeper(id string) error {
	id = strings.ToUpper(strings.TrimSpace(id))
	switch err := session.ForgetAsleep(id, wakeRetryWithin); {
	case errors.Is(err, session.ErrWaking):
		return fmt.Errorf("%s is waking up; end it once it is up", id)
	case errors.Is(err, session.ErrNotAsleep):
		return fmt.Errorf("session %s not found", id)
	default:
		return err
	}
}

// noteRestBase keeps what is on this session's terminal the first time the
// agent in front comes to rest (done, or held back by its limit), once per
// agent run (attnFGAt): its MCP servers and what launched them, which a
// sleep then never takes for work it left running (leftRunning).
func (a *Agent) noteRestBase() {
	a.metaMu.Lock()
	fg := a.attnFGAt
	taken := a.restBase != nil && a.restBaseFG.Equal(fg)
	turn := a.firstTurn.of(fg) // a turn begun before it ever rested (`claude "do X"`, keys typed as it started)
	a.metaMu.Unlock()
	if taken || a.term == nil {
		return
	}
	base := restBase(a.term, turn)
	a.metaMu.Lock()
	if a.attnFGAt.Equal(fg) {
		a.restBase, a.restBaseFG = base, fg
	}
	a.metaMu.Unlock()
}

// noteFirstTurnLocked notes a turn of the agent in front begun at t, keeping
// the run's first (noteRestBase). A turn from before this run (a hook left
// by the one before) is not its. Called with metaMu held.
func (a *Agent) noteFirstTurnLocked(t time.Time) { a.firstTurn.note(t, a.attnFGAt) }

// runFirst is the first of something in an agent run: when (at), and the
// run (its attnFGAt) it was in.
type runFirst struct{ at, run time.Time }

// note keeps t as the first in run: a later one does not move it, and one
// from before run (a hook left by the run before) is not its.
func (f *runFirst) note(t, run time.Time) {
	if t.IsZero() || t.Before(run) {
		return
	}
	if !f.run.Equal(run) || t.Before(f.at) {
		f.at, f.run = t, run
	}
}

// of is the first in run, or zero when there was none.
func (f runFirst) of(run time.Time) time.Time {
	if f.run.Equal(run) {
		return f.at
	}
	return time.Time{}
}

// markAwake is a restored session saying, once it is up and reachable, that
// any sleep it was woken from is over: its own word, so a waker that ended
// before it could say so (killed mid-wake, its daemon restarted) never
// leaves a running session marked asleep, or its wake to be tried again.
func (a *Agent) markAwake() {
	if a.restoring {
		session.ClearAsleep(a.sessionID)
	}
}

// WakeSession wakes sleeping session r: back on its id, PIN and
// conversation, the way a reboot brings it back. A wake that does not come
// up leaves it asleep, to be tried again.
func WakeSession(r session.Restore) (*SpawnedSession, error) {
	if why := wakeCheck(r); why != "" {
		return nil, errors.New(why)
	}
	if err := session.BeginWake(r.ID, time.Now()); errors.Is(err, session.ErrNotAsleep) {
		return nil, fmt.Errorf("%s was ended while asleep", r.ID)
	}
	sp, err := RestoreSession(r)
	if err != nil && !SessionRunning(r.ID) {
		session.ClearWaking(r.ID)
		return nil, err
	}
	session.ClearAsleep(r.ID)
	return sp, nil
}

// sleeperIndex keeps what a directory listing shows of each sleeping
// session (its name, folder, agent), read from its sealed record once and
// again only when its asleep mark changes: the directory is polled often,
// and opening hundreds of sealed records on every poll is not free.
var sleeperIndex = struct {
	sync.Mutex
	m map[string]sleeperRow
}{m: map[string]sleeperRow{}}

type sleeperRow struct {
	marked time.Time // the asleep mark's time when this was read
	ds     protocol.DirSession
}

// sleeperDirSessions are the directory entries of the sessions asleep.
func sleeperDirSessions() []protocol.DirSession {
	marks := session.AsleepIDs()
	sleeperIndex.Lock()
	defer sleeperIndex.Unlock()
	for id := range sleeperIndex.m {
		if _, ok := marks[id]; !ok {
			delete(sleeperIndex.m, id) // woke, or was ended
		}
	}
	var out []protocol.DirSession
	for id, at := range marks {
		row, ok := sleeperIndex.m[id]
		if !ok || !row.marked.Equal(at) {
			r, err := session.ReadRestore(id)
			a := session.ReadAsleep(id)
			if err != nil || r == nil || a == nil {
				continue // locked, or half written: the next poll tries again
			}
			row = sleeperRow{marked: at, ds: protocol.DirSession{
				ID: r.ID, Name: r.Name, Cwd: r.Cwd, Headless: true, Fg: r.Fg,
				Asleep: true, SleptAt: a.SleptAt.Unix(), Dropped: a.Dropped,
			}}
			sleeperIndex.m[id] = row
		}
		if SessionRunning(id) {
			continue // waking: it lists as itself
		}
		out = append(out, row.ds)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SleptAt > out[j].SleptAt })
	return out
}

// sleeperTranscript is a sleeping session's scrollback as text, from what
// it saved as it went to sleep: read_transcript and search_sessions answer
// from it without waking the session. ok is false when it saved none.
func sleeperTranscript(id string) (text string, truncated, ok bool) {
	p, err := session.RestoreScrollbackPath(id)
	if err != nil {
		return "", false, false
	}
	dump := readScrollbackDump(p, func(b []byte) ([]byte, error) { return session.OpenScrollback(id, b) })
	if dump == nil {
		return "", false, false
	}
	var b strings.Builder
	for _, h := range dump.History {
		b.WriteString(h)
		b.WriteByte('\n')
	}
	for _, e := range dump.Entries {
		b.Write(e.Data)
	}
	text, truncated = clipTranscript(stripANSI(b.String()), maxTranscriptBytes)
	return text, truncated, true
}

// SearchSleeperTranscript is search_sessions' hits in a sleeping session's
// saved scrollback (nil for none, or a session not asleep).
func SearchSleeperTranscript(id, pattern string) []string {
	if !session.IsAsleep(id) {
		return nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil
	}
	text, _, ok := sleeperTranscript(id)
	if !ok {
		return nil
	}
	return transcriptHits(text, re)
}

// wakeRetryWithin: a wake the daemon was stopped in the middle of is tried
// again at its start if it began this recently; an older one sleeps on.
const wakeRetryWithin = 10 * time.Minute

// sleepsOn says restore record r is a session asleep, not one a restart
// ended: left alone at start, unless its wake was cut short just now.
func sleepsOn(id string, now time.Time) bool {
	if !session.IsAsleep(id) {
		return false
	}
	at, ok := session.WakingSince(id)
	return !ok || now.Sub(at) > wakeRetryWithin
}
