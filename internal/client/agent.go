package client

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/mdp/qrterminal/v3"
	"github.com/reminal/reminal/internal/config"
	"github.com/reminal/reminal/internal/crypto"
	"github.com/reminal/reminal/internal/protocol"
	"github.com/reminal/reminal/internal/pty"
	"github.com/reminal/reminal/internal/session"
	xterm "golang.org/x/term"
)

// scrollbackBytes caps the in-memory replay buffer. 2 MiB is enough for a
// full screen of escape codes plus thousands of lines of plain output.
const scrollbackBytes = 2 * 1024 * 1024

// reconnect timing
const (
	initialBackoff = 1 * time.Second
	maxBackoff     = 30 * time.Second
	stableThresh   = 30 * time.Second
	// readDeadlineAgent bounds how long an agent-side WS read can sit idle
	// before we declare the connection dead and reconnect. With viewer
	// pings every 30s under normal operation this is well above the noise
	// floor; it mainly catches half-open TCP that the OS hasn't noticed.
	readDeadlineAgent = 60 * time.Second
)

type Agent struct {
	sessionID string
	pin       string
	pinHash   string
	webURL    string
	shell     string
	version   string // running binary's version, shown in banner + exit summary
	box       *crypto.Box
	buf       *scrollback
	term      *pty.Session

	writeMu sync.Mutex // serializes WS writes; safe across sender/reader goroutines

	// localActive gates whether pumpPTY echoes shell output to the host's
	// stdout. Set when Run() puts the local terminal into raw-attached mode
	// so the user can drive the shell from the agent's terminal directly.
	localActive bool
	// hostEscape closes when the host user presses Ctrl-]; the main loop
	// treats it the same as a graceful shutdown signal so defers still run.
	hostEscape chan struct{}

	// startedAt is set once in Run() and used to keep session.Active's
	// started_at stable when we rewrite the file on viewer-count changes.
	startedAt time.Time
	// viewersMu guards viewers — the in-memory list of connect timestamps,
	// one per currently-attached viewer. We can't perfectly identify which
	// viewer disconnected (relay only sends a count delta), so we use a
	// truncate-from-end approximation: append on connect, drop newest on
	// disconnect. Imperfect but useful enough for `reminal clients`.
	viewersMu sync.Mutex
	viewers   []time.Time
	// pendingUploads holds in-flight chunked uploads keyed by upload_id.
	// Each viewer's upload arrives as a sequence of TypeUpload messages
	// (chunked because Cloudflare DOs cap WS frames at 1 MiB); we
	// reassemble in this map and finalize when all chunks have arrived.
	uploadsMu      sync.Mutex
	pendingUploads map[string]*pendingUpload
	// paused is set by `reminal stop` (via SIGUSR1). When set, the main
	// reconnect loop stops trying to reach the relay and the local shell
	// keeps running on the host terminal as a plain interactive session.
	paused atomic.Bool
	// currentConnMu guards currentConn so the SIGUSR1 handler can close
	// the live WS the moment a pause is requested, instead of waiting up
	// to readDeadline (60s) for the read to time out.
	currentConnMu sync.Mutex
	currentConn   *websocket.Conn

	// viewerSizeMu guards viewerMinCols / viewerMinRows / viewerCount —
	// the size-tracking state recordViewerSize uses to decide whether to
	// mirror a viewer's reported size verbatim (single-viewer case) or
	// monotonically shrink (multi-viewer case, where any one viewer
	// growing must not drag everyone else's display past their own
	// smallest). Zero size = "no viewer size known", host wins.
	viewerSizeMu   sync.Mutex
	viewerMinCols  uint16
	viewerMinRows  uint16
	viewerCount    int
	lastAppliedCol uint16
	lastAppliedRow uint16

	// headless disables every interaction with the host terminal: no raw
	// mode, no host indicator (cursor color / window title / banner),
	// no host stdin pump, no Ctrl-] escape. The agent still owns its
	// PTY and broadcasts to viewers — it just runs invisibly. Set by
	// `reminal new` (which forks a detached child) and `reminal --headless`.
	headless bool
	// handshakeFD, if non-zero, is an fd inherited from the parent that
	// `reminal new` is reading from to learn the spawned session's
	// credentials. The headless agent writes a one-line JSON banner to
	// this fd and closes it once startup is complete, then the parent
	// process exits and the child detaches.
	handshakeFD int
	// resumed marks this Agent as taking over an already-running PTY
	// from a previous binary image via syscall.Exec hot-restart. When
	// true, Run() skips spawning a fresh shell and uses a.term as-is.
	resumed bool
	// hostOldState is the cooked-mode terminal state captured by
	// xterm.MakeRaw when Run() entered raw mode. Used by the hot-restart
	// path to restore the terminal before Exec'ing the new binary, so
	// the new agent's MakeRaw catches the right "previous" state for
	// its own restore-on-exit.
	hostOldState *xterm.State
	// stopControlFn is the cancel function returned by listenControl().
	// Hot-restart calls it explicitly so the new image can re-bind the
	// same control socket (PID is preserved across Exec, so the path
	// is the same).
	stopControlFn func()
}

// AgentOptions configures startup behaviour. Zero-value runs the
// classic foreground agent (the default for plain `reminal`).
type AgentOptions struct {
	// Headless skips every host-terminal interaction (raw mode, host
	// indicator, host stdin pump). Set by `reminal new` for detached
	// background sessions.
	Headless bool
	// HandshakeFD is an inherited fd to which the headless agent writes
	// a one-line JSON `{"id","pin","open_url","pid"}` once startup is
	// complete, then closes. Used by `reminal new` to learn the
	// spawned child's credentials before exiting. Zero means none.
	HandshakeFD int
	// Resume, if non-nil, signals that this Agent is taking over an
	// already-running PTY from a previous binary image via the
	// hot-restart path. The session ID, PIN, and PTY are reused
	// verbatim instead of being freshly generated, so viewers
	// reconnect to the same session URL.
	Resume *ResumeState
}

// NewAgent builds a foreground agent (no host-terminal isolation).
// Equivalent to NewAgentWith(version, AgentOptions{}).
func NewAgent(version string) (*Agent, error) {
	return NewAgentWith(version, AgentOptions{})
}

// NewAgentWith builds an agent with the given options. Headless mode is
// the only non-foreground variant today.
func NewAgentWith(version string, opts AgentOptions) (*Agent, error) {
	if opts.Resume != nil {
		// Hot-restart path: reuse the previous binary's session ID,
		// PIN, AND the previously-registered pin_hash so the relay
		// recognises us as the same agent. Re-hashing would generate
		// a different bcrypt digest (random salt per call) which the
		// relay would reject with "session credentials mismatch".
		r := opts.Resume
		box, err := crypto.NewBox(r.SessionID, r.PIN)
		if err != nil {
			return nil, err
		}
		return &Agent{
			sessionID:      r.SessionID,
			pin:            r.PIN,
			pinHash:        r.PinHash,
			webURL:         config.WebURL(),
			shell:          config.Shell(),
			version:        version,
			box:            box,
			buf:            newScrollback(scrollbackBytes),
			hostEscape:     make(chan struct{}),
			pendingUploads: make(map[string]*pendingUpload),
			term:           r.PTY,
			startedAt:      r.StartedAt,
			resumed:        true,
			headless:       opts.Headless,
			handshakeFD:    opts.HandshakeFD,
		}, nil
	}
	id, err := session.NewID(8)
	if err != nil {
		return nil, err
	}
	pin, err := session.NewPIN(6)
	if err != nil {
		return nil, err
	}
	box, err := crypto.NewBox(id, pin)
	if err != nil {
		return nil, err
	}
	pinHash, err := session.HashPIN(pin)
	if err != nil {
		return nil, err
	}

	return &Agent{
		sessionID:      id,
		pin:            pin,
		pinHash:        pinHash,
		webURL:         config.WebURL(),
		shell:          config.Shell(),
		version:        version,
		box:            box,
		buf:            newScrollback(scrollbackBytes),
		hostEscape:     make(chan struct{}),
		pendingUploads: make(map[string]*pendingUpload),
		headless:       opts.Headless,
		handshakeFD:    opts.HandshakeFD,
	}, nil
}

func (a *Agent) Run() error {
	// Banner goes to the host terminal only in foreground mode. Headless
	// agents have no terminal — credentials are delivered to the parent
	// `reminal new` process via the handshake fd and from there printed
	// to the user's calling shell.
	if !a.headless && !a.resumed {
		fmt.Println()
		// Green "HOST" badge sits inline at the top so even on macOS
		// Terminal.app — where we deliberately don't tint the cursor
		// (window-wide leak) — there's still an unmistakable visual cue that
		// THIS terminal is the source agent, not a viewer attached to one.
		fmt.Printf("  reminal — remote terminal · v%s · \x1b[1;32m[HOST]\x1b[0m %s\n",
			a.version, a.sessionID)
		fmt.Println()
		fmt.Printf("  Session:  %s\n", a.sessionID)
		fmt.Printf("  PIN:      %s\n", a.pin)
		fmt.Printf("  Open:     %s/?s=%s\n", a.webURL, a.sessionID)
		fmt.Printf("  Connect:  reminal connect %s %s\n", a.sessionID, a.pin)
		fmt.Println()
		a.printQR()
		fmt.Println("  \x1b[1;32mHOST:\x1b[0m This terminal IS the shared shell — type away. Remote viewers join in parallel.")
		fmt.Println("  Press Ctrl-] to stop sharing (shell keeps running · Ctrl-] again to fully exit) · `reminal info` reprints join info")
		fmt.Println()
	}

	// Record this session for `reminal info`. Best-effort: failures here
	// shouldn't break agent startup. startedAt is stored on the Agent
	// (preserved across hot-restart) so later viewer-count rewrites
	// keep the same value.
	if !a.resumed {
		a.startedAt = time.Now()
	}
	_ = session.WriteActive(a.activeRecord(0))
	defer func() { _ = session.ClearActive(a.sessionID) }()

	// Start the per-agent control socket so `reminal send <file>` (and any
	// other future sibling commands) can talk to us locally without going
	// through the relay.
	stopControl := a.listenControl()
	a.stopControlFn = stopControl
	defer stopControl()

	// Pass REMINAL_SESSION into the spawned shell so `reminal info` run
	// from inside this session can show THIS session's details (rather
	// than fall back to ~/.reminal/active.json, which gets ambiguous with
	// multiple agents or attach-vs-source contexts).
	// Skipped on hot-restart resume: the shell is already running inside
	// the inherited PTY; we just need to take over reading + writing.
	if !a.resumed {
		// Inject the session ID, PIN, and join URL into the shell's
		// env so `reminal info` works from anywhere — including the
		// (rare) case where the shell is on a different machine than
		// the agent (e.g. someone SSH-ed into the agent's host, started
		// reminal there, then ran a viewer that lands them in yet
		// another nested shell). PIN-in-env is fine: anyone in the
		// shell already has full shell access; the PIN is for joining
		// the shell they're already in.
		openURL := fmt.Sprintf("%s/?s=%s", a.webURL, a.sessionID)
		term, err := pty.Start(a.shell,
			"REMINAL_SESSION="+a.sessionID,
			"REMINAL_SESSION_PIN="+a.pin,
			"REMINAL_SESSION_URL="+openURL,
		)
		if err != nil {
			return fmt.Errorf("start shell: %w", err)
		}
		defer term.Close()
		a.term = term
	} else {
		// term was set in NewAgentWith from ResumeState.PTY.
		defer a.term.Close()
	}

	pty.HandleSignals()

	// Attach the host's local terminal to the PTY: raw mode, mirror PTY
	// output to stdout, pump host stdin to the PTY, follow SIGWINCH. This
	// turns `reminal` from a "display only" host into the same kind of
	// interactive shell `reminal connect` provides — no second terminal
	// needed, and remote viewers still join the same PTY in parallel.
	//
	// Skipped entirely in headless mode: the spawned background agent
	// has no terminal to attach to (its stdin is /dev/null) so anything
	// touching the host TTY would race with whatever shell the user
	// spawned us from.
	if !a.headless && xterm.IsTerminal(int(os.Stdin.Fd())) {
		oldState, terr := xterm.MakeRaw(int(os.Stdin.Fd()))
		if terr == nil {
			a.hostOldState = oldState
			defer xterm.Restore(int(os.Stdin.Fd()), oldState)
			setHostIndicator(a.sessionID)
			defer clearHostIndicator()
			a.localActive = true
			a.syncSizeToPTY()
			go a.pumpHostStdin()
			winCh := make(chan os.Signal, 1)
			signal.Notify(winCh, syscall.SIGWINCH)
			defer signal.Stop(winCh)
			go func() {
				for range winCh {
					a.syncSizeToPTY()
				}
			}()
		}
	}

	// Headless startup handshake: write the spawned session's credentials
	// to the inherited fd 3 and close it, signalling the parent
	// (`reminal new`) that we're ready and it can print + exit.
	if a.headless && a.handshakeFD != 0 {
		a.writeHandshake()
	}

	sessionStart := time.Now()
	// Deferred so it runs on every clean exit path — shell exit, agent
	// errors, signal-driven shutdown. Neutral wording ("session ended")
	// covers both shell-typed-exit and user-killed-reminal cases.
	// Skipped in headless mode (no host terminal to print to).
	if !a.headless {
		defer func() {
			agentNotify("\n  [%s] Session ended (v%s) · ran for %v\n  Run `reminal` again to start a new session.\n",
				time.Now().Format("15:04:05"),
				a.version,
				time.Since(sessionStart).Round(time.Second))
		}()
	}

	shellExit := make(chan struct{})
	go func() {
		a.pumpPTY()
		close(shellExit)
	}()

	// Trap SIGINT/SIGTERM so the process exits via the normal return path
	// (defers fire: ClearActive, exit summary, keepawake stop). Default Go
	// behavior is to die immediately on these signals, skipping defers and
	// leaving stale ~/.reminal/active.json + orphaned caffeinate.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGUSR1)
	defer signal.Stop(sigCh)
	go func() {
		first := true
		for sig := range sigCh {
			if sig == syscall.SIGUSR1 {
				// `reminal stop` — stop broadcasting, keep the local
				// shell running. Doesn't count against the force-exit
				// budget; SIGINT/SIGTERM still need a double-tap.
				a.pause()
				continue
			}
			if !first {
				fmt.Fprintln(os.Stderr, "\n  Force exit.")
				os.Exit(130)
			}
			first = false
			agentNotify("\n  [%s] %s received, shutting down… (press again to force exit)\n",
				time.Now().Format("15:04:05"), sig)
			_ = a.term.Close()
		}
	}()

	backoff := initialBackoff
	for {
		select {
		case <-shellExit:
			return nil
		case <-a.hostEscape:
			return nil
		default:
		}

		// Paused via `reminal stop`: don't touch the relay; just sit and
		// let the PTY pumps continue serving the host terminal as a
		// plain local shell until shell exit / Ctrl-].
		if a.paused.Load() {
			select {
			case <-shellExit:
				return nil
			case <-a.hostEscape:
				return nil
			}
		}

		start := time.Now()
		err := a.runConnection(shellExit)
		select {
		case <-shellExit:
			return nil
		case <-a.hostEscape:
			return nil
		default:
		}

		// runConnection returned because pause() closed the WS — don't
		// log "Reconnecting…" or burn through backoff cycles.
		if a.paused.Load() {
			continue
		}

		if err == nil {
			err = errors.New("connection closed")
		}
		if time.Since(start) > stableThresh {
			backoff = initialBackoff
		}

		// Rate-limited path: the relay's edge (Cloudflare) has flagged
		// this client. Retrying at the normal 1–30s cadence just keeps
		// the throttle hot — switch to a 10-minute pause (or whatever
		// Retry-After advised) so the bucket can drain. We DON'T grow
		// `backoff` past initial on this branch; once the throttle
		// clears we want fast reconnects again.
		var rl *rateLimitedError
		if errors.As(err, &rl) {
			wait := rl.retryAfter
			if wait < rateLimitMinWait {
				wait = rateLimitMinWait
			}
			agentNotify("  reminal: %s\n", humanize(err))
			select {
			case <-shellExit:
				return nil
			case <-a.hostEscape:
				return nil
			case <-time.After(wait):
			}
			backoff = initialBackoff
			continue
		}

		agentNotify("  reminal: %s Reconnecting in %v…\n", humanize(err), backoff)

		select {
		case <-shellExit:
			return nil
		case <-a.hostEscape:
			return nil
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// rateLimitMinWait is the floor for how long we wait after a 429.
// Cloudflare's throttle window is opaque; 10 min is long enough to
// drain in practice without making the user wait too long when they
// happen to start a session right at the tail of a previous burst.
const rateLimitMinWait = 10 * time.Minute

// syncSizeToPTY copies the host terminal's current size into the PTY so the
// shell sees the correct cols/rows. Called on startup and on every SIGWINCH.
// Falls through applyEffectiveSize so the viewer-min logic still clamps
// the PTY to the smallest attached viewer.
func (a *Agent) syncSizeToPTY() {
	a.applyEffectiveSize()
}

// recordViewerSize tracks the size we should clamp the PTY to.
//
//   - When there's exactly one viewer (the common case — your phone),
//     we track its current size verbatim. That lets the PTY grow back
//     when the soft keyboard collapses or the device rotates wider —
//     no ratchet, no "freed space below stays unused" bug.
//   - When 2+ viewers are attached, we fall back to the smallest seen
//     since the multi-viewer state started: any one viewer growing its
//     own viewport mustn't drag everyone else's display larger than the
//     smallest can render.
//
// resetViewerSize() clears state when the viewer count drops to 0.
func (a *Agent) recordViewerSize(cols, rows uint16) {
	if cols == 0 || rows == 0 {
		return
	}
	a.viewerSizeMu.Lock()
	defer a.viewerSizeMu.Unlock()
	if a.viewerCount <= 1 {
		// Single viewer (or unknown count, treated the same) — mirror
		// its current size, including grows.
		a.viewerMinCols = cols
		a.viewerMinRows = rows
		return
	}
	// Multi-viewer: monotonically shrink only.
	if a.viewerMinCols == 0 || cols < a.viewerMinCols {
		a.viewerMinCols = cols
	}
	if a.viewerMinRows == 0 || rows < a.viewerMinRows {
		a.viewerMinRows = rows
	}
}

// resetViewerSize clears the viewer min — called when the last viewer
// leaves, so a fresh single viewer joining later can grow the PTY back
// up to its size instead of being pinned at whatever a long-gone phone
// once requested.
func (a *Agent) resetViewerSize() {
	a.viewerSizeMu.Lock()
	a.viewerMinCols = 0
	a.viewerMinRows = 0
	a.viewerCount = 0
	a.viewerSizeMu.Unlock()
}

// applyEffectiveSize resizes the PTY to min(host_size, viewer_min).
// Safe to call from any path that thinks the size might have changed
// (host SIGWINCH, viewer resize, viewer count drop). It dedups against
// the last applied size so repeated calls are cheap.
func (a *Agent) applyEffectiveSize() {
	if a.term == nil {
		return
	}
	var hostCols, hostRows uint16
	if c, r, err := xterm.GetSize(int(os.Stdout.Fd())); err == nil {
		hostCols, hostRows = uint16(c), uint16(r)
	}
	a.viewerSizeMu.Lock()
	vc, vr := a.viewerMinCols, a.viewerMinRows
	a.viewerSizeMu.Unlock()

	cols, rows := hostCols, hostRows
	if vc > 0 && (cols == 0 || vc < cols) {
		cols = vc
	}
	if vr > 0 && (rows == 0 || vr < rows) {
		rows = vr
	}
	if cols == 0 || rows == 0 {
		return
	}
	a.viewerSizeMu.Lock()
	last := a.lastAppliedCol == cols && a.lastAppliedRow == rows
	a.lastAppliedCol, a.lastAppliedRow = cols, rows
	a.viewerSizeMu.Unlock()
	if last {
		return
	}
	_ = a.term.Resize(cols, rows)
	// Tell every viewer the new PTY size so their xterm.js matches.
	// Without this, the agent shrinks the PTY for a small viewer (e.g.,
	// phone joining a desktop session), the shell formats output for
	// the new size, but the original-large viewer is still rendering
	// at its old size — cursor-positioning escape codes land in the
	// wrong column and the display goes garbled. Broadcasting keeps
	// every screen in sync with what the shell actually thinks.
	a.broadcastSize(cols, rows)
}

// broadcastSize publishes the PTY's current (cols, rows) to every
// attached viewer. They mirror it via term.resize() so cursor escape
// sequences land at the right positions. Best-effort: no return value,
// no retry — if the WS is mid-reconnect the next applyEffectiveSize()
// (triggered on resume) will catch them up.
func (a *Agent) broadcastSize(cols, rows uint16) {
	if cols == 0 || rows == 0 {
		return
	}
	payload, err := json.Marshal(protocol.Message{Cols: cols, Rows: rows})
	if err != nil {
		return
	}
	enc, err := a.box.Encrypt(payload)
	if err != nil {
		return
	}
	a.currentConnMu.Lock()
	conn := a.currentConn
	a.currentConnMu.Unlock()
	if conn == nil {
		return
	}
	_ = a.writeMsg(conn, protocol.Message{Type: protocol.TypeResize, Data: enc})
}

// writeHandshake emits the agent's credentials to the parent process's
// inherited pipe (fd 3) and closes it. `reminal new` blocks on this pipe
// so the user's prompt only returns once the background session is up
// and ready to accept viewers. Best-effort — a closed pipe (parent
// already exited) is logged but doesn't break the headless agent.
func (a *Agent) writeHandshake() {
	if a.handshakeFD == 0 {
		return
	}
	payload := map[string]any{
		"id":       a.sessionID,
		"pin":      a.pin,
		"open_url": fmt.Sprintf("%s/?s=%s", a.webURL, a.sessionID),
		"pid":      os.Getpid(),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	f := os.NewFile(uintptr(a.handshakeFD), "handshake")
	if f == nil {
		return
	}
	_, _ = f.Write(append(data, '\n'))
	_ = f.Close()
}

// activeRecord builds the session.Active value the agent writes to
// ~/.reminal/active.json. Used at startup and on every viewer-count
// transition so `reminal info` and the attach-to-existing prompt see a
// live count.
func (a *Agent) activeRecord(viewers int) session.Active {
	return session.Active{
		ID:        a.sessionID,
		PIN:       a.pin,
		OpenURL:   fmt.Sprintf("%s/?s=%s", a.webURL, a.sessionID),
		PID:       os.Getpid(),
		StartedAt: a.startedAt,
		Headless:  a.headless,
		Viewers:   viewers,
	}
}

// updateActiveViewers refreshes the on-disk viewer count. No-op when paused
// (active.json is intentionally absent during pause).
func (a *Agent) updateActiveViewers(viewers int) {
	if a.paused.Load() {
		return
	}
	_ = session.WriteActive(a.activeRecord(viewers))
}

// syncViewerList reconciles the in-memory viewer connect-timestamp list to
// the new count broadcast by the relay. We can't perfectly identify which
// viewer left (the relay only sends a delta), so the approximation:
//   - on a connect event, append now() and truncate from the end if we
//     somehow over-counted
//   - on a disconnect event, drop newest entries until len matches count
// The oldest connect timestamps stay accurate; the newest may be a bit off
// but `reminal connections` is "good enough for the host to see who's
// watching", not a precise audit log.
func (a *Agent) syncViewerList(targetCount int, isConnect bool) {
	a.viewersMu.Lock()
	defer a.viewersMu.Unlock()
	if isConnect {
		a.viewers = append(a.viewers, time.Now())
	}
	if targetCount < 0 {
		targetCount = 0
	}
	if len(a.viewers) > targetCount {
		a.viewers = a.viewers[:targetCount]
	}
	// On disconnect events, len could already be < targetCount if we missed
	// a connect — pad with now() so the count still matches. Better than
	// lying about it being empty.
	for len(a.viewers) < targetCount {
		a.viewers = append(a.viewers, time.Now())
	}
}

// snapshotViewers returns a copy of the viewer connect-timestamp list.
func (a *Agent) snapshotViewers() []time.Time {
	a.viewersMu.Lock()
	defer a.viewersMu.Unlock()
	out := make([]time.Time, len(a.viewers))
	copy(out, a.viewers)
	return out
}

// pendingUpload buffers in-flight chunks for a single multi-part upload.
// One entry per upload_id; deleted on completion or after the staleTimer
// fires (no new chunk within uploadStaleTimeout).
type pendingUpload struct {
	name       string
	ttlSeconds int
	total      int               // total chunks expected
	chunks     map[int][]byte    // index -> decoded bytes
	startedAt  time.Time
	staleTimer *time.Timer
	// progress notice is sent at the start; final notice on completion
}

// uploadStaleTimeout drops an upload that goes more than this long
// without receiving another chunk. Generous enough to survive slow
// mobile uploads but short enough that crashed viewers don't pin memory.
const uploadStaleTimeout = 60 * time.Second

// uploadChunkMaxBytes caps decoded chunk size. Web client picks 256 KB
// chunks to stay well under the Cloudflare DO 1 MiB WS message limit
// (with base64 + encryption overhead).
const uploadChunkMaxBytes = 768 * 1024

// handleUpload decrypts a TypeUpload message and either finalizes a
// single-shot upload (no upload_id) or routes a chunk into the
// pendingUploads buffer. On the final chunk we write the assembled
// bytes to ~/Downloads/reminal/ and broadcast a notice to every viewer
// (including the host terminal). All errors surface via broadcastNotice
// so the user can see what went wrong.
func (a *Agent) handleUpload(encData string) {
	plaintext, err := a.box.Decrypt(encData)
	if err != nil {
		a.broadcastNotice(fmt.Sprintf("upload failed: decrypt: %v", err))
		return
	}
	var payload struct {
		UploadID   string `json:"upload_id"`
		Index      int    `json:"index"`
		Total      int    `json:"total"`
		Name       string `json:"name"`
		Content    string `json:"content"` // base64
		TTLSeconds int    `json:"ttl_seconds"`
	}
	if err := json.Unmarshal(plaintext, &payload); err != nil {
		a.broadcastNotice(fmt.Sprintf("upload failed: parse: %v", err))
		return
	}
	if payload.Name == "" {
		a.broadcastNotice("upload failed: missing filename")
		return
	}
	safe := filepath.Base(payload.Name)
	if safe == "." || safe == "/" || safe == "" {
		a.broadcastNotice("upload failed: invalid filename")
		return
	}
	chunk, err := base64.StdEncoding.DecodeString(payload.Content)
	if err != nil {
		a.broadcastNotice(fmt.Sprintf("upload failed: bad base64: %v", err))
		return
	}
	if len(chunk) > uploadChunkMaxBytes {
		a.broadcastNotice(fmt.Sprintf("upload failed: chunk too large (%d bytes)", len(chunk)))
		return
	}

	// Single-shot path: no upload_id (or total==1). Bypass the
	// pendingUploads map entirely so old clients keep working.
	if payload.UploadID == "" || payload.Total <= 1 {
		a.finalizeUpload(payload.UploadID, safe, chunk, payload.TTLSeconds)
		return
	}

	// Chunked path. Validate then add to pending map.
	if payload.Index < 0 || payload.Index >= payload.Total {
		a.broadcastNotice(fmt.Sprintf("upload failed: bad chunk index %d/%d", payload.Index, payload.Total))
		return
	}

	a.uploadsMu.Lock()
	up, ok := a.pendingUploads[payload.UploadID]
	if !ok {
		up = &pendingUpload{
			name:       safe,
			ttlSeconds: payload.TTLSeconds,
			total:      payload.Total,
			chunks:     make(map[int][]byte, payload.Total),
			startedAt:  time.Now(),
		}
		id := payload.UploadID
		up.staleTimer = time.AfterFunc(uploadStaleTimeout, func() {
			a.uploadsMu.Lock()
			stale, still := a.pendingUploads[id]
			if still {
				delete(a.pendingUploads, id)
			}
			a.uploadsMu.Unlock()
			if still {
				a.broadcastNotice(fmt.Sprintf("upload %q timed out after %s (%d/%d chunks)",
					stale.name, uploadStaleTimeout, len(stale.chunks), stale.total))
			}
		})
		a.pendingUploads[payload.UploadID] = up
		a.broadcastNotice(fmt.Sprintf("receiving %s (%d chunks)…", safe, payload.Total))
	}
	// Late chunks reset the stale timer. Duplicates are ignored.
	if _, dup := up.chunks[payload.Index]; !dup {
		up.chunks[payload.Index] = chunk
	}
	up.staleTimer.Reset(uploadStaleTimeout)
	complete := len(up.chunks) == up.total
	if complete {
		up.staleTimer.Stop()
		delete(a.pendingUploads, payload.UploadID)
	}
	a.uploadsMu.Unlock()

	if !complete {
		return
	}

	// Assemble in index order. Total decoded size has already been
	// bounded per-chunk above, and the web client enforces a max-file
	// cap — but we still protect against pathological totals.
	totalBytes := 0
	for _, c := range up.chunks {
		totalBytes += len(c)
	}
	assembled := make([]byte, 0, totalBytes)
	for i := 0; i < up.total; i++ {
		assembled = append(assembled, up.chunks[i]...)
	}
	a.finalizeUpload(payload.UploadID, up.name, assembled, up.ttlSeconds)
}

// finalizeUpload writes the assembled bytes to ~/Downloads/reminal/ and
// schedules TTL deletion. Shared by the single-shot and chunked paths.
// uploadID is the originating viewer's upload ID — when present, we
// broadcast a TypeUploadAck so the viewer can auto-paste the path into
// the shell at the cursor.
func (a *Agent) finalizeUpload(uploadID, safe string, raw []byte, ttlSeconds int) {
	home, err := os.UserHomeDir()
	if err != nil {
		a.broadcastNotice(fmt.Sprintf("upload failed: %v", err))
		return
	}
	dir := filepath.Join(home, "Downloads", "reminal")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		a.broadcastNotice(fmt.Sprintf("upload failed: mkdir: %v", err))
		return
	}
	path := uniquePath(filepath.Join(dir, safe))
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		a.broadcastNotice(fmt.Sprintf("upload failed: write: %v", err))
		return
	}

	// Best-effort: copy the absolute path to the host's clipboard so the
	// user can paste it straight into whatever app needs it (Claude Code,
	// `cat`, image-handling commands, etc.). Silent no-op if no clipboard
	// tool is available (headless Linux without xclip/wl-copy).
	clipNote := ""
	if copyToClipboard(path) {
		clipNote = " · path copied to host clipboard"
	}

	// Tell the originating viewer the resolved path so it can auto-type
	// it at the shell cursor — the part the user is actually here for
	// (uploading from a phone, then needing the path in `cat`, `claude`,
	// etc., is otherwise painful to copy off the terminal on a small
	// screen). Broadcast — non-originating viewers ignore via upload_id.
	a.broadcastUploadAck(uploadID, path)

	if ttlSeconds > 0 {
		ttl := time.Duration(ttlSeconds) * time.Second
		a.broadcastNotice(fmt.Sprintf("uploaded %s (%s) · auto-delete in %s%s",
			path, humanByteSize(len(raw)), ttl, clipNote))
		// AfterFunc runs in its own goroutine; if the agent exits before
		// it fires, the file remains — the dedicated ~/Downloads/reminal/
		// directory makes orphans obvious.
		time.AfterFunc(ttl, func() {
			if err := os.Remove(path); err != nil {
				if os.IsNotExist(err) {
					return
				}
				a.broadcastNotice(fmt.Sprintf("auto-delete failed for %s: %v", path, err))
				return
			}
			a.broadcastNotice(fmt.Sprintf("auto-deleted %s (TTL %s expired)", path, ttl))
		})
	} else {
		a.broadcastNotice(fmt.Sprintf("uploaded %s (%s) · kept forever%s",
			path, humanByteSize(len(raw)), clipNote))
	}
}

// stopControlListener tears down the per-agent Unix control socket so a
// follow-up process (the new image after a hot-restart Exec) can re-bind
// the same path. Safe to call multiple times.
func (a *Agent) stopControlListener() {
	if a.stopControlFn != nil {
		a.stopControlFn()
		a.stopControlFn = nil
	}
}

// broadcastUploadAck tells viewers an upload completed, scoped by the
// originating upload_id. Sent over the same encrypted channel as
// TypeNotify / TypeDownload. Non-originating viewers will see the
// message but ignore it (their pendingUploadIDs set doesn't contain
// this ID). Best-effort: no error path — if we can't send, the viewer
// just sees the in-band "[reminal] uploaded ..." notice and selects
// the path manually as before.
func (a *Agent) broadcastUploadAck(uploadID, path string) {
	if uploadID == "" {
		return
	}
	payload, err := json.Marshal(struct {
		UploadID string `json:"upload_id"`
		Path     string `json:"path"`
	}{UploadID: uploadID, Path: path})
	if err != nil {
		return
	}
	enc, err := a.box.Encrypt(payload)
	if err != nil {
		return
	}
	a.currentConnMu.Lock()
	conn := a.currentConn
	a.currentConnMu.Unlock()
	if conn == nil {
		return
	}
	_ = a.writeMsg(conn, protocol.Message{Type: protocol.TypeUploadAck, Data: enc})
}

// copyToClipboard puts `text` onto the host's clipboard via whichever
// tool the platform provides. Returns true on success, false otherwise
// (no tool found, exec error). Best-effort by design — clipboard is a
// nice-to-have for uploads, not a hard requirement.
func copyToClipboard(text string) bool {
	var candidates [][]string
	switch runtime.GOOS {
	case "darwin":
		candidates = [][]string{{"pbcopy"}}
	case "linux":
		// Wayland first (`wl-copy`), then X11 (`xclip`), then `xsel`.
		candidates = [][]string{
			{"wl-copy"},
			{"xclip", "-selection", "clipboard"},
			{"xsel", "--clipboard", "--input"},
		}
	default:
		return false
	}
	for _, cmd := range candidates {
		if _, err := exec.LookPath(cmd[0]); err != nil {
			continue
		}
		c := exec.Command(cmd[0], cmd[1:]...)
		stdin, err := c.StdinPipe()
		if err != nil {
			continue
		}
		if err := c.Start(); err != nil {
			continue
		}
		_, _ = stdin.Write([]byte(text))
		_ = stdin.Close()
		if err := c.Wait(); err == nil {
			return true
		}
	}
	return false
}

// uniquePath returns the first non-existing variant of p — appends -2, -3,
// etc. before the extension if the file already exists.
func uniquePath(p string) string {
	if _, err := os.Stat(p); err != nil {
		return p
	}
	ext := filepath.Ext(p)
	base := strings.TrimSuffix(p, ext)
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d%s", base, i, ext)
		if _, err := os.Stat(candidate); err != nil {
			return candidate
		}
	}
}

// humanByteSize renders an int as 1.2 KB / 47.93 MB style for human display.
func humanByteSize(n int) string {
	const (
		kb = 1000
		mb = 1000 * kb
		gb = 1000 * mb
	)
	switch {
	case n < kb:
		return fmt.Sprintf("%d B", n)
	case n < mb:
		return fmt.Sprintf("%.2f KB", float64(n)/kb)
	case n < gb:
		return fmt.Sprintf("%.2f MB", float64(n)/mb)
	default:
		return fmt.Sprintf("%.2f GB", float64(n)/gb)
	}
}

// broadcastNotice writes a one-line dim status to the host's stdout AND
// appends it to the scrollback (so every connected viewer sees it too).
// Used for out-of-band agent events like file uploads.
func (a *Agent) broadcastNotice(text string) {
	line := fmt.Sprintf("\r\n\x1b[2m[reminal] %s\x1b[0m\r\n", text)
	if a.localActive {
		_, _ = os.Stdout.Write([]byte(line))
	}
	if enc, err := a.box.Encrypt([]byte(line)); err == nil {
		a.buf.Append(enc)
	}
}

// pause is invoked from the SIGUSR1 handler when the user runs
// `reminal stop`. It closes the live WS (if any) so the relay sees us go
// offline immediately, clears the on-disk session record so attach can't
// reach a non-broadcasting agent, and flips the paused flag so the main
// reconnect loop stops trying. The PTY pumps keep running, so the host
// terminal continues working as a plain local shell.
func (a *Agent) pause() {
	if !a.paused.CompareAndSwap(false, true) {
		return // already paused
	}
	_ = session.ClearActive(a.sessionID)
	a.currentConnMu.Lock()
	if a.currentConn != nil {
		_ = a.currentConn.Close()
	}
	a.currentConnMu.Unlock()
	// Drop the [HOST] terminal chrome — Run()'s defer only fires on real
	// shutdown, but here Run() keeps going (local shell continues), so
	// without this the green cursor + "(host)" title would lie about
	// our state until the user finally types `exit`.
	if a.localActive {
		clearHostIndicator()
	}
	agentNotify("\n  [%s] Sharing stopped. Local shell continues — type `exit` or Ctrl-] to quit.\n",
		time.Now().Format("15:04:05"))
}

// agentNotify writes a one-line dim status to stdout. Used for operational
// events (viewer connect/disconnect, reconnect retries, shutdown signals,
// exit summary) so they read as background metadata against the shell's own
// output rather than competing content. Initial banner stays at full
// brightness because that's the info the user actively needs to see.
//
// Always emits CRLF line endings — most agentNotify calls happen while
// the host terminal is in raw mode (set by Run's MakeRaw), where a bare
// LF moves the cursor down without returning to column 0, producing a
// staircase where each subsequent line is indented further right than
// the last. The defer order also means the exit-summary notify runs
// BEFORE xterm.Restore, so even the final "session ended" line needs
// this treatment. Writing CRLF in cooked mode is harmless — the tty
// driver tolerates the extra CR.
func agentNotify(format string, args ...interface{}) {
	s := fmt.Sprintf(format, args...)
	// Normalise any pre-existing \r\n to \n first, then expand back to
	// \r\n, so callers can write either and we always emit the right one.
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\n", "\r\n")
	fmt.Print("\x1b[2m" + s + "\x1b[0m")
}

// printQR renders a scannable QR for the join URL with the PIN in the URL
// fragment (#p=...). The fragment never leaves the phone — it's read by the
// page's JS to autofill the PIN field, giving a one-tap join from mobile.
func (a *Agent) printQR() {
	joinURL := fmt.Sprintf("%s/?s=%s#p=%s", a.webURL, a.sessionID, a.pin)
	fmt.Println("  Scan to join from your phone:")
	fmt.Println()
	qrterminal.GenerateHalfBlock(joinURL, qrterminal.L, os.Stdout)
	fmt.Println()
}

// pumpPTY reads the PTY forever, encrypts each chunk and stores it in the
// scrollback. The sender goroutine drains the scrollback over the websocket.
func (a *Agent) pumpPTY() {
	buf := make([]byte, 4096)
	for {
		n, err := a.term.Read(buf)
		if n > 0 {
			// Mirror to the host's stdout so the user typing at the agent
			// terminal sees the shell's output as if they had run it
			// directly. The same bytes go into scrollback for remote viewers.
			if a.localActive {
				_, _ = os.Stdout.Write(buf[:n])
			}
			enc, encErr := a.box.Encrypt(buf[:n])
			if encErr == nil {
				a.buf.Append(enc)
			}
		}
		if err != nil {
			return
		}
	}
}

// pumpHostStdin reads the agent's local terminal stdin and writes each chunk
// straight into the PTY — making the host terminal an interactive shell, same
// as a `reminal connect` session would be. Ctrl-] (telnet's traditional
// escape) is intercepted: the byte itself is not forwarded, and the first
// press pauses sharing (closes the WS, kicks viewers) while keeping the
// local shell intact. A second press while already paused fully exits
// reminal. This way the user can stop broadcasting without losing the
// long-running command they have going inside the shell.
func (a *Agent) pumpHostStdin() {
	buf := make([]byte, 4096)
	for {
		n, err := os.Stdin.Read(buf)
		if n > 0 {
			data := buf[:n]
			if i := bytes.IndexByte(data, escapeKey); i >= 0 {
				// Flush bytes before the escape to the PTY.
				if i > 0 {
					_, _ = a.term.Write(data[:i])
				}
				if a.paused.Load() {
					// Second Ctrl-] (already paused) — fully exit.
					select {
					case <-a.hostEscape:
					default:
						close(a.hostEscape)
					}
					return
				}
				// First Ctrl-]: stop broadcasting but keep the
				// host terminal + PTY + running processes alive.
				a.pause()
				// Continue pumping any bytes that came AFTER the
				// escape (likely none — Ctrl-] is usually pressed
				// solo — but handle the edge case anyway).
				if rest := data[i+1:]; len(rest) > 0 {
					_, _ = a.term.Write(rest)
				}
			} else {
				_, _ = a.term.Write(data)
			}
		}
		if err != nil {
			return
		}
	}
}

func (a *Agent) runConnection(shellExit <-chan struct{}) error {
	wsURL := config.SessionWS(a.sessionID, string(protocol.RoleAgent))
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		// Cloudflare throttles workers.dev WS upgrades aggressively and
		// returns 429 (a fresh HTML page, no Worker invocation at all).
		// Surface that distinctly so the user sees "throttled — back off
		// 10 min" instead of the misleading "session ended". Re-attempts
		// at sub-minute cadence keep the IP/domain on the bad list.
		if resp != nil && resp.StatusCode == 429 {
			return &rateLimitedError{retryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
		}
		return fmt.Errorf("dial relay: %w", err)
	}
	defer conn.Close()
	// Track the live conn so `reminal stop` (SIGUSR1) can close it
	// immediately rather than waiting for the next read deadline.
	a.currentConnMu.Lock()
	a.currentConn = conn
	a.currentConnMu.Unlock()
	defer func() {
		a.currentConnMu.Lock()
		a.currentConn = nil
		a.currentConnMu.Unlock()
	}()

	if err := a.authenticate(conn); err != nil {
		return err
	}

	cursorCh := make(chan uint64, 4)
	stop := make(chan struct{})
	var stopOnce sync.Once
	closeStop := func() { stopOnce.Do(func() { close(stop) }) }

	readerDone := make(chan error, 1)
	go func() { readerDone <- a.runReader(conn, cursorCh) }()

	senderDone := make(chan error, 1)
	go func() { senderDone <- a.runSender(conn, cursorCh, stop) }()

	pingStop := make(chan struct{})
	var pingOnce sync.Once
	closePing := func() { pingOnce.Do(func() { close(pingStop) }) }
	go a.runPing(conn, pingStop)

	defer closeStop()
	defer closePing()

	select {
	case <-shellExit:
		return nil
	case <-a.hostEscape:
		// Host pressed Ctrl-]; close the live conn so the reader goroutine
		// returns immediately rather than blocking on its next read until
		// readDeadline fires (which would otherwise freeze pumpHostStdin
		// has already exited, and the user's local terminal would feel
		// dead for up to 60s).
		_ = conn.Close()
		return nil
	case err := <-readerDone:
		return err
	case err := <-senderDone:
		return err
	}
}

func (a *Agent) authenticate(conn *websocket.Conn) error {
	if err := a.writeMsg(conn, protocol.Message{
		Type:    protocol.TypeAuth,
		PinHash: a.pinHash,
	}); err != nil {
		return err
	}

	_, raw, err := conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("auth: %w", err)
	}

	var msg protocol.Message
	if err := json.Unmarshal(raw, &msg); err != nil {
		return fmt.Errorf("auth response: %w", err)
	}
	if msg.Type == protocol.TypeError {
		return fmt.Errorf("%s", msg.Error)
	}
	if msg.Type != protocol.TypeAuthOK {
		return fmt.Errorf("unexpected auth response: %s", msg.Type)
	}
	return nil
}

// runReader processes messages from the relay: viewer input, resize, resume
// requests, and connection notifications.
func (a *Agent) runReader(conn *websocket.Conn, cursorCh chan uint64) error {
	for {
		_ = conn.SetReadDeadline(time.Now().Add(readDeadlineAgent))
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return err
		}

		var msg protocol.Message
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}

		switch msg.Type {
		case protocol.TypeData:
			data, err := a.box.Decrypt(msg.Data)
			if err != nil {
				continue
			}
			if _, err := a.term.Write(data); err != nil {
				return err
			}
		case protocol.TypeResize:
			plaintext, err := a.box.Decrypt(msg.Data)
			if err != nil {
				continue
			}
			var rs protocol.Message
			if json.Unmarshal(plaintext, &rs) != nil || rs.Cols == 0 || rs.Rows == 0 {
				continue
			}
			// "Smallest viewer wins" — every web client sends its own
			// size on every visualViewport change (iOS keyboard toggle,
			// orientation, etc.). If we honoured each one, the PTY
			// would flap between viewer sizes on every keystroke and
			// corrupt the host's display. Instead we shrink the PTY
			// monotonically to fit the smallest attached viewer; on the
			// host's local terminal that means some right-edge whitespace,
			// but everyone (phone + laptop) sees correctly formatted
			// content. Reset happens when viewer count drops to 0
			// (handled in TypeClosed below).
			a.recordViewerSize(rs.Cols, rs.Rows)
			a.applyEffectiveSize()
			// If the PTY size didn't actually change but the sender
			// asked for something different, still tell them (and
			// everyone) the authoritative size — otherwise a viewer
			// that grew its own viewport keeps rendering at the wrong
			// width while the shell formats for the unchanged PTY.
			a.viewerSizeMu.Lock()
			pc, pr := a.lastAppliedCol, a.lastAppliedRow
			a.viewerSizeMu.Unlock()
			if pc > 0 && pr > 0 && (pc != rs.Cols || pr != rs.Rows) {
				a.broadcastSize(pc, pr)
			}
		case protocol.TypeResume:
			// FromSeq is the highest seq the viewer has received. We replay
			// everything with Seq > FromSeq, so the next seq to send is FromSeq+1.
			// If the viewer's seq is past anything we've ever emitted (most
			// commonly after a hot-restart, where this process started with a
			// zero seq counter while the viewer kept its tally from the
			// previous incarnation), reset to 0 so the viewer sees all our
			// new output instead of being silently filtered.
			cursor := msg.FromSeq + 1
			if cursor > a.buf.NextSeq() {
				cursor = 0
			}
			pushCursor(cursorCh, cursor)
		case protocol.TypeUpload:
			a.handleUpload(msg.Data)
		case protocol.TypeConnected:
			if msg.Count > 0 {
				agentNotify("  [%s] Viewer connected (%d active)\n",
					time.Now().Format("15:04:05"), msg.Count)
			} else {
				agentNotify("  [%s] Remote viewer connected\n",
					time.Now().Format("15:04:05"))
			}
			a.updateActiveViewers(msg.Count)
			a.syncViewerList(msg.Count, true)
			a.viewerSizeMu.Lock()
			a.viewerCount = msg.Count
			pc, pr := a.lastAppliedCol, a.lastAppliedRow
			a.viewerSizeMu.Unlock()
			// Re-publish the PTY size so the freshly-joined viewer
			// can size its xterm.js to match. Existing viewers
			// no-op this since they're already at the right size.
			if pc > 0 && pr > 0 {
				a.broadcastSize(pc, pr)
			}
		case protocol.TypeClosed:
			if msg.Count > 0 {
				agentNotify("  [%s] Viewer disconnected (%d still active)\n",
					time.Now().Format("15:04:05"), msg.Count)
			} else {
				agentNotify("  [%s] Last viewer disconnected\n",
					time.Now().Format("15:04:05"))
				// Reset the viewer-min so a fresh viewer joining
				// later can grow the PTY back to its size, instead
				// of staying pinned at whatever a long-gone phone
				// once asked for.
				a.resetViewerSize()
				a.applyEffectiveSize()
			}
			a.viewerSizeMu.Lock()
			a.viewerCount = msg.Count
			// Going from 2+ → 1 also unlocks the ratchet: the lone
			// remaining viewer should be able to grow back. We don't
			// know its size yet, but on its next resize message
			// recordViewerSize will mirror it verbatim because
			// viewerCount == 1.
			if msg.Count == 1 {
				a.viewerMinCols = 0
				a.viewerMinRows = 0
			}
			a.viewerSizeMu.Unlock()
			a.updateActiveViewers(msg.Count)
			a.syncViewerList(msg.Count, false)
		case protocol.TypeAgentOffline, protocol.TypeAgentOnline:
			// Informational only on the agent side.
		case protocol.TypeError:
			return fmt.Errorf("%s", msg.Error)
		case protocol.TypePing:
			_ = a.writeMsg(conn, protocol.Message{Type: protocol.TypePong})
		}
	}
}

// runSender waits for a resume from the viewer, then streams buffered output
// from that point. It keeps streaming new chunks as they're appended.
func (a *Agent) runSender(conn *websocket.Conn, cursorCh <-chan uint64, stop <-chan struct{}) error {
	notify := a.buf.Notify()
	var cursor uint64
	sending := false

	for {
		if !sending {
			select {
			case <-stop:
				return nil
			case c := <-cursorCh:
				cursor = c
				sending = true
			}
		}

		entries := a.buf.From(cursor)
		for _, e := range entries {
			if err := a.writeMsg(conn, protocol.Message{
				Type: protocol.TypeData,
				Data: e.Data,
				Seq:  e.Seq,
			}); err != nil {
				return err
			}
			cursor = e.Seq + 1
		}

		select {
		case <-stop:
			return nil
		case c := <-cursorCh:
			cursor = c
		case <-notify:
		}
	}
}

func (a *Agent) writeMsg(conn *websocket.Conn, msg protocol.Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	return conn.WriteMessage(websocket.TextMessage, data)
}

func (a *Agent) runPing(conn *websocket.Conn, stop <-chan struct{}) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			if err := a.writeMsg(conn, protocol.Message{Type: protocol.TypePing}); err != nil {
				return
			}
		}
	}
}

// pushCursor non-blockingly delivers the latest from_seq to the sender,
// replacing any older queued value so the sender always reacts to the newest
// resume request.
func pushCursor(ch chan uint64, seq uint64) {
	for {
		select {
		case ch <- seq:
			return
		default:
			// Drain the oldest entry to make room, then try again.
			select {
			case <-ch:
			default:
				return
			}
		}
	}
}

func encryptResize(box *crypto.Box, cols, rows uint16) (string, error) {
	payload, err := json.Marshal(protocol.Message{Cols: cols, Rows: rows})
	if err != nil {
		return "", err
	}
	return box.Encrypt(payload)
}
