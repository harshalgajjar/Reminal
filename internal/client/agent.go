// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

	"github.com/charmbracelet/x/vt"
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
	// pinHash is the legacy bcrypt(PIN) credential. Still generated (and carried
	// across hot-restart) so we can prove control of a pre-existing session while
	// migrating it, but it is only sent to the relay when sendPinHash is set.
	pinHash string
	// token is the high-entropy reattach credential (Level B). It replaces
	// pinHash so the relay never holds any PIN-derived, offline-crackable value.
	// Always sent on auth for new-format sessions.
	token string
	// sendPinHash is true only while migrating a legacy (pin_hash-registered)
	// session: the agent presents pinHash once alongside its new token to prove
	// control, then clears this so later reconnects are token-only and the
	// bcrypt value stops crossing the wire.
	sendPinHash bool
	webURL      string
	shell       string
	version     string // running binary's version, shown in banner + exit summary
	box         *crypto.Box
	sessionKey  []byte // 32-byte AES key wrapped per-viewer via EKE; see crypto/kex.go
	buf         *scrollback
	term        *pty.Session

	// screen is a headless terminal emulator fed the same plaintext output
	// that goes to viewers. On a fresh attach we serialize its current state
	// (screen + scrollback) into a single snapshot paint instead of replaying
	// the whole raw output history — so joining is instant regardless of how
	// long the session has run, and the viewer can still scroll back. Guarded
	// by screenMu (composite reads during snapshot must not race Write/Resize).
	// nil when snapshots are disabled (REMINAL_SNAPSHOT=0) — then we fall back
	// to raw replay.
	screen          *vt.Emulator
	screenMu        sync.Mutex
	scrollbackLines int // history lines included in a snapshot (0 = screen only)
	// rebuildEmu is the persistent tall emulator snapshots replay history
	// through (see rebuildView). Guarded by rebuildMu; lazily created.
	rebuildEmu      *vt.Emulator
	rebuildMu       sync.Mutex
	scrollbackBytes int // byte cap on a snapshot's history (0 = no cap)

	writeMu sync.Mutex // serializes WS writes; safe across sender/reader goroutines

	// kex throttle. Each kex_init we answer is exactly one online PIN guess
	// (the viewer-side blinding means a forged handshake can test only one
	// candidate — see crypto/kex.go), so a token bucket here bounds an active
	// relay's brute-force of the 6-digit PIN. This replaces the relay's old
	// 5-strike lockout, which we removed because the relay no longer sees the
	// PIN at all (it can't, without becoming able to MITM the EKE).
	kexMu     sync.Mutex
	kexTokens float64
	kexLast   time.Time

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

	// name + cwd are session identity. name comes from `reminal new --name` /
	// `reminal --name` (empty if unnamed). cwd starts as os.Getwd() at
	// construction but is refreshed from the shell's live working directory
	// (see refreshCwd) so `reminal list` follows the shell as it cd's.
	// Persisted into the active record; resolveActive also matches on them.
	name string
	cwd  string
	// metaMu guards title + lastActivity + cwd, which the pumpPTY and
	// meta-flush goroutines write while activeRecord reads them.
	metaMu       sync.Mutex
	title        string
	lastActivity time.Time
	// metaDirty is set by pumpPTY when title or lastActivity changed since
	// the last on-disk flush; the meta-flush loop clears it when it writes.
	// Keeps idle sessions from churning the active record.
	metaDirty atomic.Bool
	// curViewers caches the latest relay-reported viewer count so the
	// meta-flush loop can rewrite the record without losing it (the
	// viewer-count path passes the count explicitly; the meta path can't).
	curViewers atomic.Int32
	// OSC title-parser state, touched only by pumpPTY's goroutine. oscState
	// is the small state machine in feedTitle; oscBuf accumulates the body
	// of an in-progress OSC 0/2 sequence across PTY read boundaries.
	oscState int
	oscBuf   []byte
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

	// viewerSizeMu guards viewerCols / viewerRows / viewerCount —
	// the size-tracking state recordViewerSize maintains. viewerCols/Rows
	// hold the size the most-recently-active viewer reported (latest-resize-
	// wins); zero size = "no viewer size known", host wins.
	viewerSizeMu   sync.Mutex
	viewerCols     uint16
	viewerRows     uint16
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

	// Window mirroring: winBackend is the OS-specific window enumerate/
	// capture/input driver (built once via winOnce). winStreams, guarded by
	// winMu, maps a streaming window's id to its stop channel — several
	// windows can stream at once (the browser shows them as draggable panes),
	// so this is a set, not a single slot. winOps is a serialized work queue:
	// window enumeration and input injection shell out to osascript /
	// screencapture, which can take seconds, so they must NOT run on the
	// relay reader goroutine (that would freeze terminal I/O). A single
	// worker drains winOps in FIFO order, keeping keystrokes ordered while
	// never blocking the reader. See windows.go.
	winOnce    sync.Once
	winBackend windowBackend
	winMu      sync.Mutex
	winStreams map[string]chan struct{}
	// winAck maps a streaming window's id to a channel the viewer's frame acks
	// are delivered on, so streamWindow can pace to the viewer (see streamWindow).
	// Guarded by winMu.
	winAck map[string]chan uint64
	// winMenu marks a window whose right-click just opened a context menu. macOS
	// draws menus as SEPARATE windows, so a capture-by-window-id misses them; for
	// a short interval after a right-click we instead capture that window's screen
	// REGION (bounds snapshotted at the click), which composites the overlaid menu
	// into the frame. Cleared on the next click (menu dismissed / item chosen) or
	// on timeout. Guarded by winMu. See streamWindow / handleWindowInput.
	winMenu map[string]winMenuState
	// winAwake holds a display-sleep inhibitor (caffeinate -d) while ANY window
	// is being mirrored, so the host can't idle-lock — a locked Mac drops
	// synthetic input, making remote window control silently dead. Held from the
	// first stream, released when the last stops. Guarded by winMu.
	winAwake func()
	// stayAwake holds a display-sleep inhibitor for the WHOLE session when the
	// "always unlocked" setting is on, so the host can't idle-lock even before a
	// viewer connects. Toggled at startup (settings/env) and live via the control
	// socket. Guarded by stayMu.
	stayMu    sync.Mutex
	stayAwake func()
	winOps    chan func()
	// Click-counting state for native double/triple-click detection. Touched
	// only by the single winOps worker goroutine, so it needs no lock.
	winClickN      int
	winLastClickX  int
	winLastClickY  int
	winLastClickAt time.Time
	// Scroll-gesture tracking (winOps worker only): we raise the target window
	// once at the start of a gesture so scroll lands on it, then skip re-raising
	// during the gesture to keep it smooth.
	winScrollID string
	winScrollAt time.Time

	// WebRTC peer-to-peer frame transport. When a viewer can open a
	// DataChannel, window frames + acks flow directly to it instead of through
	// the (per-message-billed) relay. rtcPeers maps a viewer-chosen peer id to
	// its PeerConnection state; guarded by rtcMu. See webrtc.go.
	rtcMu    sync.Mutex
	rtcPeers map[string]*rtcPeer
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
	// Name is an optional human-friendly label for the session, surfaced by
	// `reminal list` and usable in place of the ID. Set from
	// `reminal new --name` / `reminal --name`. Empty leaves the session
	// unnamed. Ignored on Resume (the name is recovered from the existing
	// record so it survives `reminal restart`).
	Name string
}

// currentCwd returns the process working directory for the active record,
// or "" if it can't be determined. Best-effort — an empty cwd just means
// `reminal list` omits the directory column for this session.
func currentCwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return wd
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
		//
		// The session encryption key (sessionKey) is NOT carried
		// across the exec — the previous process's K is gone with it.
		// A fresh K is generated below; reconnecting viewers re-run
		// the EKE handshake on their next WS connection and pick up
		// the new K transparently.
		r := opts.Resume
		sessionKey, err := crypto.NewSessionKey()
		if err != nil {
			return nil, err
		}
		box, err := crypto.NewBox(sessionKey)
		if err != nil {
			return nil, err
		}
		// Reattach credential. If the previous image carried a token forward,
		// this session is already on the new (token) scheme — reuse it and never
		// send pin_hash. If it didn't (we were hot-restarted BY an older binary
		// that predates tokens), this is a legacy pin_hash session: mint a token
		// now and flag a one-time pin_hash send so the relay can migrate us.
		token := r.Token
		migrate := false
		if token == "" {
			token, err = session.NewToken()
			if err != nil {
				return nil, err
			}
			migrate = true
		}
		return &Agent{
			sessionID:      r.SessionID,
			pin:            r.PIN,
			pinHash:        r.PinHash,
			token:          token,
			sendPinHash:    migrate,
			webURL:         config.WebURL(),
			shell:          config.Shell(),
			version:        version,
			box:            box,
			sessionKey:     sessionKey,
			buf:            newScrollback(scrollbackBytes),
			hostEscape:     make(chan struct{}),
			pendingUploads: make(map[string]*pendingUpload),
			term:           r.PTY,
			startedAt:      r.StartedAt,
			resumed:        true,
			headless:       opts.Headless,
			handshakeFD:    opts.HandshakeFD,
			cwd:            currentCwd(),
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
	sessionKey, err := crypto.NewSessionKey()
	if err != nil {
		return nil, err
	}
	box, err := crypto.NewBox(sessionKey)
	if err != nil {
		return nil, err
	}
	pinHash, err := session.HashPIN(pin)
	if err != nil {
		return nil, err
	}
	// Brand-new session: token-native from the first connect, so no PIN-derived
	// value ever reaches the relay. pinHash is kept in memory only (for a
	// possible future migration / legacy-relay fallback) and never sent.
	token, err := session.NewToken()
	if err != nil {
		return nil, err
	}

	return &Agent{
		sessionID:      id,
		pin:            pin,
		pinHash:        pinHash,
		token:          token,
		webURL:         config.WebURL(),
		shell:          config.Shell(),
		version:        version,
		box:            box,
		sessionKey:     sessionKey,
		buf:            newScrollback(scrollbackBytes),
		hostEscape:     make(chan struct{}),
		pendingUploads: make(map[string]*pendingUpload),
		headless:       opts.Headless,
		handshakeFD:    opts.HandshakeFD,
		name:           strings.TrimSpace(opts.Name),
		cwd:            currentCwd(),
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
		// One-tap join link with the PIN in the #p= fragment (auto-filled by
		// the web client, never sent to the server) — tap it from a phone.
		fmt.Printf("  Join:     %s/?s=%s#p=%s\n", a.webURL, a.sessionID, a.pin)
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
	} else if a.name == "" {
		// Hot-restart preserves the PID, so the prior record is still on
		// disk. Recover the user-set name (ResumeState doesn't carry it) so
		// it survives `reminal restart`. cwd already survives via Getwd().
		if prev, err := session.ReadActiveByID(a.sessionID); err == nil {
			a.name = prev.Name
		}
	}
	_ = session.WriteActive(a.activeRecord(0))
	defer func() { _ = session.ClearActive(a.sessionID) }()

	// Start the per-agent control socket so `reminal send <file>` (and any
	// other future sibling commands) can talk to us locally without going
	// through the relay.
	stopControl := a.listenControl()
	a.stopControlFn = stopControl
	defer stopControl()

	// Apply the persisted "always unlocked" preference (or the env override) so
	// the host is already prevented from idle-locking before any viewer connects.
	if config.LoadSettings().StayUnlocked || os.Getenv("REMINAL_STAY_UNLOCKED") == "1" {
		a.setStayUnlocked(true)
	}
	defer a.setStayUnlocked(false)

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
	// Seed the record with the shell's actual working directory and persist
	// it now so `reminal list` is correct immediately instead of after the
	// first flush.
	a.refreshCwd()
	if !a.paused.Load() {
		_ = session.WriteActive(a.activeRecord(int(a.curViewers.Load())))
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
			// Registered after Restore so it runs before it (LIFO): disable any
			// terminal modes (e.g. color-scheme notifications) that inner
			// programs leaked onto the host terminal, so they don't spew stray
			// reports at the user's shell prompt after reminal exits.
			defer resetLeakedTermModes()
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

	// Build the emulator before the pump starts so it sees output from the
	// very first byte (snapshot-on-attach; REMINAL_SNAPSHOT=0 disables it).
	a.initScreen()

	shellExit := make(chan struct{})
	go func() {
		a.pumpPTY()
		close(shellExit)
	}()

	// Persist title / last-activity updates to the active record on a
	// throttle. Runs for both headless and foreground sessions so
	// `reminal list` ordering and `reminal prune` idle detection work
	// regardless of how the session was started. Stops when the shell exits.
	go a.metaFlushLoop(shellExit)

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

// recordViewerSize tracks the size the PTY should take: latest-resize-wins.
//
// The most-recently-active viewer drives the size. A single viewer (the
// common case — your phone) is mirrored verbatim, so the PTY grows back
// the moment the soft keyboard collapses or the device rotates.
//
// With 2+ viewers we'd ideally use the min of their CURRENT sizes, but the
// relay forwards viewer resizes without a per-viewer id, so the agent can't
// tell which viewer a size came from or recompute the min when one grows.
// The previous "monotonically shrink" workaround never recovered: a phone's
// keyboard-OPEN height is a transient shrink that became a permanent floor,
// pinning the phone to ~60% forever after the keyboard closed (and any time
// a smaller viewer had ever connected). So instead the latest resize wins —
// whichever screen you're actively using fills correctly, and it recovers
// instantly when that viewer grows. Other attached viewers render at that
// size (margins if they're larger, scroll if smaller), which is the right
// trade for "the screen I'm using is always right".
//
// resetViewerSize() clears state when the viewer count drops to 0.
func (a *Agent) recordViewerSize(cols, rows uint16) {
	if cols == 0 || rows == 0 {
		return
	}
	a.viewerSizeMu.Lock()
	a.viewerCols = cols
	a.viewerRows = rows
	a.viewerSizeMu.Unlock()
}

// resetViewerSize clears the viewer min — called when the last viewer
// leaves, so a fresh single viewer joining later can grow the PTY back
// up to its size instead of being pinned at whatever a long-gone phone
// once requested.
func (a *Agent) resetViewerSize() {
	a.viewerSizeMu.Lock()
	a.viewerCols = 0
	a.viewerRows = 0
	a.viewerCount = 0
	a.viewerSizeMu.Unlock()
}

// applyEffectiveSize resizes the PTY to the right dimensions for the
// currently attached viewer(s). The active viewer's reported size
// (recordViewerSize, latest-resize-wins) drives the PTY:
//
//   - ROWS: the active viewer's rows, NOT capped by the host terminal.
//     The host just scrolls past any extra rows. This is what lets a tall
//     phone use its full height even when the host window (or, in a
//     headless/dev run, the host PTY) is shorter — capping rows to the
//     host was what left a big empty band at the bottom of the phone,
//     both on keyboard-collapse and whenever a second viewer joined.
//
//   - COLUMNS: the active viewer's cols, additionally capped to the host
//     width. A viewer wider than the host terminal would make the shell
//     pad output past the host's right edge; that output is mirrored to
//     BOTH screens, so the narrower host wraps it — most visibly
//     stranding zsh's PROMPT_EOL_MARK (a ghost "%") on its own line.
//     Capping columns to the host keeps every screen wrap-clean.
//
// With no viewers attached, the PTY simply matches the host terminal.
//
// Safe to call from any path that thinks the size might have changed.
// Dedups against the last applied size so repeated calls are cheap.
func (a *Agent) applyEffectiveSize() {
	if a.term == nil {
		return
	}
	var hostCols, hostRows uint16
	if c, r, err := xterm.GetSize(int(os.Stdout.Fd())); err == nil {
		hostCols, hostRows = uint16(c), uint16(r)
	}
	a.viewerSizeMu.Lock()
	vc, vr := a.viewerCols, a.viewerRows
	a.viewerSizeMu.Unlock()

	var cols, rows uint16
	if vc > 0 && vr > 0 {
		// A viewer is attached — the active viewer's size drives the PTY.
		// ROWS are taken as-is; the host terminal scrolls vertically, so a
		// tall phone is never capped to a short host window (or, in a
		// headless/dev run, to a tiny host PTY) — that host-row cap was the
		// multi-viewer "gap" bug. COLS are capped to the host width so the
		// host terminal never has to wrap the shell's output and strand
		// zsh's PROMPT_EOL_MARK (the ghost "%").
		cols, rows = vc, vr
		if hostCols > 0 && hostCols < cols {
			cols = hostCols
		}
	} else {
		// No viewers attached — match the host terminal.
		cols, rows = hostCols, hostRows
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
	a.resizeScreen(cols, rows)
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
//
// Sentinel (0, 0) means "tell me your size" — sent when the lone
// remaining viewer needs to re-report its viewport so the agent can
// grow the PTY back after the smallest viewer left. Viewers handle
// it by refitting + sendResize-ing their own viewport dimensions.
func (a *Agent) broadcastSize(cols, rows uint16) {
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
	a.metaMu.Lock()
	title := a.title
	last := a.lastActivity
	name := a.name
	cwd := a.cwd
	a.metaMu.Unlock()
	if last.IsZero() {
		last = a.startedAt
	}
	return session.Active{
		ID:           a.sessionID,
		PIN:          a.pin,
		OpenURL:      fmt.Sprintf("%s/?s=%s", a.webURL, a.sessionID),
		PID:          os.Getpid(),
		StartedAt:    a.startedAt,
		Headless:     a.headless,
		Viewers:      viewers,
		Name:         name,
		Cwd:          cwd,
		Title:        title,
		LastActivity: last,
	}
}

// refreshCwd updates the recorded cwd from the shell's live working directory
// so `reminal list` follows cd's instead of showing the launch dir. The PID is
// read fresh each call (not cached) because after a hot-restart it comes from
// the terminal's foreground process group, which changes as programs come and
// go. Best-effort — if the lookup fails the previous value is kept.
func (a *Agent) refreshCwd() {
	if a.term == nil {
		return
	}
	if c := shellCwd(a.term.Pid()); c != "" {
		a.metaMu.Lock()
		a.cwd = c
		a.metaMu.Unlock()
	}
}

// setName changes the session's display name (from `reminal rename`, via the
// control socket) and persists the active record immediately so `reminal list`
// reflects it without waiting for the next throttled flush. Guarded by metaMu
// since activeRecord reads a.name from other goroutines.
func (a *Agent) setName(name string) {
	a.metaMu.Lock()
	a.name = strings.TrimSpace(name)
	a.metaMu.Unlock()
	if !a.paused.Load() {
		_ = session.WriteActive(a.activeRecord(int(a.curViewers.Load())))
	}
}

// updateActiveViewers refreshes the on-disk viewer count. No-op when paused
// (active.json is intentionally absent during pause).
func (a *Agent) updateActiveViewers(viewers int) {
	a.curViewers.Store(int32(viewers))
	if a.paused.Load() {
		return
	}
	_ = session.WriteActive(a.activeRecord(viewers))
}

// markActivity stamps the last-PTY-output time and flags the record dirty so
// the meta-flush loop rewrites it. Called from pumpPTY on every output chunk;
// the actual disk write is throttled by metaFlushLoop so a busy shell doesn't
// rewrite active-*.json on every read.
func (a *Agent) markActivity(now time.Time) {
	a.metaMu.Lock()
	a.lastActivity = now
	a.metaMu.Unlock()
	a.metaDirty.Store(true)
}

// feedTitle drives a tiny state machine over PTY output to capture the latest
// terminal title the shell sets via OSC 0 (icon+title) or OSC 2 (title) —
// "ESC ] (0|2) ; <text> (BEL | ESC \)". Most shells set this to the cwd or the
// running command, giving `reminal list` a recognisable "running: …" hint for
// free. Runs in pumpPTY's goroutine; oscState/oscBuf are owned by it.
func (a *Agent) feedTitle(p []byte) {
	const (
		oscIdle    = iota
		oscEsc     // saw ESC, expecting ']'
		oscBody    // inside OSC body, collecting until BEL or ST
		oscBodyEsc // inside OSC body, saw ESC, expecting '\' (ST)
	)
	for _, c := range p {
		switch a.oscState {
		case oscIdle:
			if c == 0x1b {
				a.oscState = oscEsc
			}
		case oscEsc:
			if c == ']' {
				a.oscState = oscBody
				a.oscBuf = a.oscBuf[:0]
			} else {
				a.oscState = oscIdle
			}
		case oscBody:
			switch c {
			case 0x07: // BEL terminator
				a.commitTitle()
				a.oscState = oscIdle
			case 0x1b:
				a.oscState = oscBodyEsc
			default:
				if len(a.oscBuf) < 512 { // cap: titles are short; ignore overruns
					a.oscBuf = append(a.oscBuf, c)
				}
			}
		case oscBodyEsc:
			if c == '\\' { // ST terminator (ESC \)
				a.commitTitle()
				a.oscState = oscIdle
			} else if c == 0x1b {
				a.oscState = oscEsc // a fresh escape; abandon this OSC
			} else {
				a.oscState = oscIdle
			}
		}
	}
}

// commitTitle parses an accumulated OSC body ("Ps;Pt") and, if it's a title
// set (Ps 0 or 2) that changed, stores the sanitized text and flags the
// record dirty for the next meta flush.
func (a *Agent) commitTitle() {
	s := string(a.oscBuf)
	i := strings.IndexByte(s, ';')
	if i < 0 {
		return
	}
	if ps := s[:i]; ps != "0" && ps != "2" {
		return // not a title-setting OSC (e.g. OSC 1 icon, OSC 12 cursor)
	}
	title := sanitizeTitle(s[i+1:])
	if title == "" {
		return
	}
	a.metaMu.Lock()
	changed := title != a.title
	a.title = title
	a.metaMu.Unlock()
	if changed {
		a.metaDirty.Store(true)
	}
}

// sanitizeTitle trims whitespace, strips control characters, and caps length
// so a hostile or noisy title can't break `reminal list`'s layout.
func sanitizeTitle(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for _, r := range s {
		if r >= 0x20 && r != 0x7f {
			b.WriteRune(r)
		}
	}
	out := b.String()
	const max = 120
	if len(out) > max {
		out = out[:max]
	}
	return strings.TrimSpace(out)
}

// metaFlushLoop periodically persists title/last-activity changes to the
// active record. Throttled (and dirty-gated) so an actively-used shell rewrites
// at most once per tick and an idle one not at all. Stops when stop is closed
// (Run's shellExit). No-op while paused — active-*.json is intentionally
// absent during `reminal stop`.
func (a *Agent) metaFlushLoop(stop <-chan struct{}) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			if a.paused.Load() {
				continue
			}
			if a.metaDirty.Swap(false) {
				// A cd shows up as PTY output (the new prompt), so a dirty
				// record is exactly when the cwd may have changed — refresh
				// it before persisting.
				a.refreshCwd()
				_ = session.WriteActive(a.activeRecord(int(a.curViewers.Load())))
			}
		}
	}
}

// syncViewerList reconciles the in-memory viewer connect-timestamp list to
// the new count broadcast by the relay. We can't perfectly identify which
// viewer left (the relay only sends a delta), so the approximation:
//   - on a connect event, append now() and truncate from the end if we
//     somehow over-counted
//   - on a disconnect event, drop newest entries until len matches count
//
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
	total      int            // total chunks expected
	chunks     map[int][]byte // index -> decoded bytes
	startedAt  time.Time
	staleTimer *time.Timer
	// progress notice is sent at the start; final notice on completion
}

// uploadStaleTimeout drops an upload that goes more than this long
// without receiving another chunk. Generous enough to survive slow
// mobile uploads but short enough that crashed viewers don't pin memory.
const uploadStaleTimeout = 60 * time.Second

// maxUploadTTLSeconds caps the viewer-supplied TTL on uploads. Past
// this a time.Duration cast overflows (int64 nanoseconds capped at
// ~292 years), and even short of overflow there's no meaningful
// distinction between "kept for a decade" and "kept forever". One
// year is the longest TTL that still feels like an auto-delete.
const maxUploadTTLSeconds = 365 * 24 * 60 * 60

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
	// Strip control characters before the name reaches broadcastNotice
	// (which renders to the host terminal AND to viewer terminals).
	// Without this an authenticated viewer could upload a file named
	// "\x1b[2J" or "\x07\x1b]0;hacked\x07" and disrupt the host's
	// display every time we log progress. filepath.Base does not
	// sanitize control bytes — they're legal in POSIX filenames.
	safe = stripControlChars(safe)
	if safe == "" {
		a.broadcastNotice("upload failed: invalid filename (control chars only)")
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
	// finalizeUpload writes the file to disk and broadcasts an ack —
	// for a 100 MB upload, the disk write alone can take hundreds of
	// ms on a slow disk. Run it in a goroutine so the WS reader loop
	// can keep processing the viewer's keystrokes, resizes, and
	// pings without a perceptible stall right after a big upload.
	id, name, ttl := payload.UploadID, up.name, up.ttlSeconds
	go a.finalizeUpload(id, name, assembled, ttl)
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
		// Cap at one year. A viewer-supplied ttl_seconds of, say,
		// 9999999999 would overflow time.Duration (int64 nanoseconds,
		// max ≈ 292 years) once multiplied by time.Second — time.AfterFunc
		// would then fire with an undefined duration, potentially
		// auto-deleting the file immediately. Anything past a year is
		// indistinguishable from "kept forever" anyway.
		if ttlSeconds > maxUploadTTLSeconds {
			ttlSeconds = maxUploadTTLSeconds
		}
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

// stripControlChars removes C0 / C1 / DEL bytes from s. Used to sanitize
// viewer-supplied strings (filenames, names) before they reach a path
// that renders to a terminal — otherwise a malicious viewer could
// embed escape sequences in their upload's filename and disrupt the
// host's display (clear screen, change title, ring bell on a loop) the
// next time we log a notice mentioning the name.
func stripControlChars(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		b := s[i]
		if b < 0x20 || b == 0x7f {
			continue
		}
		out = append(out, b)
	}
	return string(out)
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
	a.record([]byte(line))
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
// initScreen sets up the headless emulator used for snapshot-on-attach, unless
// REMINAL_SNAPSHOT=0 disables it (falling back to raw replay). Sized to the
// current PTY size; kept in sync by feedScreen / resizeScreen.
func (a *Agent) initScreen() {
	if os.Getenv("REMINAL_SNAPSHOT") == "0" {
		return
	}
	a.scrollbackLines = config.SnapshotScrollbackLines()
	a.scrollbackBytes = config.SnapshotScrollbackBytes()
	cols, rows := a.lastAppliedCol, a.lastAppliedRow
	if cols == 0 || rows == 0 {
		cols, rows = 80, 24
	}
	a.screenMu.Lock()
	a.screen = vt.NewEmulator(int(cols), int(rows))
	if a.scrollbackLines > 0 {
		a.screen.Scrollback().SetMaxLines(a.scrollbackLines)
	}
	scr := a.screen
	a.screenMu.Unlock()
	if a.buf != nil {
		// Seed the replay-geometry baseline for the history rebuild (see
		// scrollback.SetBase / rebuildView).
		a.buf.SetBase(int(cols), int(rows))
	}

	// CRITICAL: the emulator answers terminal queries (device attributes,
	// cursor-position / status reports, etc.) by writing the reply into an
	// internal pipe. If nothing reads that pipe, the *next* such query makes
	// Write block forever — which stalls record() and freezes the shell. Apps
	// like `claude` emit these queries on startup (plain `seq`/`less` don't,
	// which is why this slipped through). We don't want the emulator's replies
	// anyway — the real viewer/host terminal answers queries over the normal
	// stdin→PTY path — so drain and discard them. Runs until the emulator is
	// closed / the process exits.
	go func() { _, _ = io.Copy(io.Discard, scr) }()
}

// record is the single path that commits a chunk of plaintext output to both
// the emulator and the scrollback buffer, under screenMu. Doing both under one
// lock keeps the invariant the snapshot relies on: the emulator's state always
// corresponds exactly to the buffer through its latest seq, so a snapshot can be
// tagged with that seq without duplicating or dropping a chunk on the joiner.
func (a *Agent) record(p []byte) {
	a.screenMu.Lock()
	if a.screen != nil {
		_, _ = a.screen.Write(p)
	}
	if enc, err := a.box.Encrypt(p); err == nil {
		a.buf.Append(enc)
	}
	a.screenMu.Unlock()
}

// resizeScreen keeps the emulator's dimensions in step with the PTY so the
// snapshot matches the size viewers render at.
func (a *Agent) resizeScreen(cols, rows uint16) {
	if a.screen == nil || cols == 0 || rows == 0 {
		return
	}
	a.screenMu.Lock()
	a.screen.Resize(int(cols), int(rows))
	if a.buf != nil {
		// Marker for the history rebuild: bytes after this point were emitted
		// for the new geometry. Under screenMu so it orders correctly against
		// record()'s appends.
		a.buf.AppendResize(int(cols), int(rows))
	}
	a.screenMu.Unlock()
}

// rebuildView replays the raw (encrypted) output buffer through a tall replay
// emulator and returns the reconstructed HISTORY plus, when the session is on
// the main screen, the current SCREEN rows — carved from the same replay, so
// history and screen can never overlap or misalign (no seam heuristics).
// screen == nil means "use the live emulator's screen" (alt-screen sessions,
// where the replay only reconstructs the main-buffer content behind the app).
// ok=false means the rebuild can't run (no crypto box or buffer — bare test
// agents) and the caller should fall back to the live emulator's scrollback.
//
// The buffer is a bounded ring, so the replay may begin mid-stream — the
// parser resynchronises at the next escape sequence, and full-screen apps
// repaint, so at worst the oldest line or two of history render oddly. Output
// recorded between this snapshot of the buffer and the live-screen read that
// follows is missing from the replay only for milliseconds' worth of bytes.
func (a *Agent) rebuildView() (history, screen []string, ok bool) {
	if a.box == nil || a.buf == nil || a.scrollbackLines == 0 {
		return nil, nil, false
	}
	entries := a.buf.From(0)
	if len(entries) == 0 {
		return nil, nil, true // nothing recorded yet — empty history is correct
	}
	// Start the replay at the geometry in effect at the oldest retained entry
	// and follow the recorded resize markers from there — every segment
	// re-renders at the width it was emitted for, exactly like a live viewer
	// saw it (replaying 120-col output at 100 cols wraps every line).
	cols, rows := a.buf.Base()
	if cols <= 0 || rows <= 0 {
		a.screenMu.Lock()
		cols, rows = a.screen.Width(), a.screen.Height()
		a.screenMu.Unlock()
	}
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	// Replay on a TALL screen (current width). Inline TUIs repaint their whole
	// visible transcript with cursor-relative writes; on a screen-height
	// emulator each repaint lands after content has scrolled and a stale copy
	// gets pushed into scrollback — one duplicate transcript block per resize
	// or redraw. On a tall screen the repaints overwrite IN PLACE (the cursor
	// never leaves the visible area), so each line exists exactly once by
	// construction. Only genuinely long history scrolls off the tall screen.
	//
	// The emulator is PERSISTENT (created once, reset with RIS between
	// rebuilds) because vt's Close races its own query-drain goroutine, and
	// not closing would leak that goroutine per snapshot. Serialized by
	// rebuildMu — concurrent joins take turns.
	const rebuildRows = 400
	a.rebuildMu.Lock()
	defer a.rebuildMu.Unlock()
	if a.rebuildEmu == nil {
		a.rebuildEmu = vt.NewEmulator(cols, rebuildRows)
		re := a.rebuildEmu
		// Drain terminal-query replies or Write can block forever (see
		// initScreen). Lives as long as the agent, like the live emulator's.
		go func() { _, _ = io.Copy(io.Discard, re) }()
	}
	e := a.rebuildEmu
	_, _ = e.Write([]byte("\x1bc")) // RIS: fresh terminal state for this rebuild
	e.ClearScrollback()
	e.Resize(cols, rebuildRows)
	e.Scrollback().SetMaxLines(a.scrollbackLines)
	// The app addresses rows assuming the terminal is `rows` tall; on the tall
	// emulator those must be translated into the sliding virtual viewport or a
	// resize repaint homing to "row 1" would overwrite the oldest history
	// instead of its own previous render (see vviewWriter).
	w := &vviewWriter{e: e, rows: rows}
	for _, ent := range entries {
		if ent.Cols > 0 {
			// Resize marker: the following bytes were emitted for this geometry.
			e.Resize(ent.Cols, rebuildRows)
			w.setRows(ent.Rows)
			continue
		}
		if ent.Bar {
			continue // status-bar chrome: geometry-bound, meaningless in a replay
		}
		pt, err := a.box.Decrypt(ent.Data)
		if err != nil {
			return nil, nil, false
		}
		w.Write(pt)
	}
	wasAlt := e.IsAltScreen()
	if wasAlt {
		// Pop back to the main buffer so Render() shows the content BEHIND the
		// full-screen app — that content is the history. The live emulator
		// still owns the actual alt screen the viewer will get.
		_, _ = e.Write([]byte("\x1b[?1049l"))
	}
	// Rendered rows of the tall replay, trailing blanks trimmed.
	lines := strings.Split(e.Render(), "\n")
	end := len(lines)
	for end > 0 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	lines = lines[:end]
	for i, r := range lines {
		lines[i] = strings.TrimRight(r, " ")
	}
	// The virtual viewport [base, base+rows) is the current screen; everything
	// above it — plus whatever scrolled off the tall replay — is history. On
	// the alt screen the whole main buffer is history (the app owns the view).
	history = renderScrollback(e, a.scrollbackLines)
	base := w.Base()
	if wasAlt || base > len(lines) {
		base = len(lines)
	}
	history = append(history, lines[:base]...)
	if !wasAlt {
		screen = lines[base:]
	}
	if a.scrollbackLines > 0 && len(history) > a.scrollbackLines {
		history = history[len(history)-a.scrollbackLines:]
	}
	return history, screen, true
}

// snapshotFrame returns the encrypted snapshot of the current screen+scrollback
// and the seq it represents (everything through that seq). Built under screenMu
// with the latest seq read in the same critical section, so the frame and seq
// are consistent. Returns ("", 0) if snapshots are disabled or building fails.
func (a *Agent) snapshotFrame() (string, uint64) {
	if a.screen == nil {
		return "", 0
	}
	history, rebuiltScreen, ok := a.rebuildView()
	a.screenMu.Lock()
	if !ok {
		history = renderScrollback(a.screen, a.scrollbackLines)
		rebuiltScreen = nil
	}
	snap := buildSnapshot(a.screen, history, rebuiltScreen, a.scrollbackBytes, false)
	latest := a.buf.LatestSeq()
	a.screenMu.Unlock()
	if snap == "" {
		return "", 0
	}
	enc, err := a.box.Encrypt([]byte(snap))
	if err != nil {
		return "", 0
	}
	return enc, latest
}

// scrollback. The sender goroutine drains the scrollback over the websocket.
func (a *Agent) pumpPTY() {
	buf := make([]byte, 4096)
	for {
		n, err := a.term.Read(buf)
		if n > 0 {
			// Record liveness + sniff the shell's window title so
			// `reminal list` can order recent-first and show a "running: …"
			// hint. Both are cheap and update in-memory only; the on-disk
			// rewrite is throttled by metaFlushLoop.
			a.markActivity(time.Now())
			a.feedTitle(buf[:n])
			// Mirror to the host's stdout so the user typing at the agent
			// terminal sees the shell's output as if they had run it
			// directly. The same bytes go into scrollback for remote viewers.
			if a.localActive {
				_, _ = os.Stdout.Write(buf[:n])
			}
			a.record(buf[:n])
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
	// Always prove control with the high-entropy token. Only a legacy session
	// mid-migration also sends pin_hash (once) so the relay can match its old
	// credential before switching us to token-only.
	auth := protocol.Message{Type: protocol.TypeAuth, Token: a.token}
	if a.sendPinHash {
		auth.PinHash = a.pinHash
	}
	if err := a.writeMsg(conn, auth); err != nil {
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
	// Migration succeeded — the relay now holds our token. Stop sending pin_hash
	// so it stops crossing the wire on subsequent reconnects.
	a.sendPinHash = false
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
		case protocol.TypeKexInit:
			// A viewer is asking us to run the PIN-authenticated EKE.
			// handleKexInit broadcasts a TypeKexResp tagged with the
			// same ex_id; only the originating viewer matches it (and
			// only that viewer's ephemeral private key can unwrap the
			// session key inside).
			a.handleKexInit(conn, msg.ExID, msg.Data)
		case protocol.TypeUpload:
			a.handleUpload(msg.Data)
		case protocol.TypeWindowList:
			a.enqueueWinOp(func() { a.handleWindowList(conn) })
		case protocol.TypeWindowCtl:
			d := msg.Data
			a.enqueueWinOp(func() { a.handleWindowCtl(d) })
		case protocol.TypeWindowInput:
			d := msg.Data
			a.enqueueWinOp(func() { a.handleWindowInput(d) })
		case protocol.TypeWindowAck:
			// Acks pace the frame streams (see streamWindow). Handle them
			// directly rather than via the serialized winOps queue so a pending
			// capture/input op can't delay the ack that unblocks the next frame.
			a.handleWindowAck(msg.Data)
		case protocol.TypeAppList:
			a.enqueueWinOp(func() { a.handleAppList(conn) })
		case protocol.TypeAppOpen:
			d := msg.Data
			a.enqueueWinOp(func() { a.handleAppOpen(d) })
		case protocol.TypeWebRTCHello:
			// A viewer wants a peer-to-peer frame channel; reply with an offer.
			// Off the read loop: minting Cloudflare TURN creds is a network call
			// that must not stall terminal I/O.
			go a.handleWebRTCHello(conn, msg.Data)
		case protocol.TypeWebRTCAnswer:
			a.handleWebRTCAnswer(msg.Data)
		case protocol.TypeWebRTCICE:
			a.handleWebRTCICE(msg.Data)
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
			prevCount := a.viewerCount
			a.viewerCount = msg.Count
			pc, pr := a.lastAppliedCol, a.lastAppliedRow
			a.viewerSizeMu.Unlock()
			// Re-publish the PTY size so the freshly-joined viewer
			// can size its xterm.js to match. Existing viewers
			// no-op this since they're already at the right size.
			if pc > 0 && pr > 0 {
				a.broadcastSize(pc, pr)
				// Force the shell to repaint its prompt on the
				// 0→1+ transition only. That covers the true
				// reattach case (sole viewer dropped, came back)
				// where the prompt was drawn before the disconnect
				// and might have scrolled out of the resume window.
				// Gating on prevCount==0 avoids flickering TUI
				// apps (claude code, vim) for existing viewers when
				// a SECOND viewer joins — they don't need the
				// repaint, and React/Ink-based UIs re-render the
				// whole screen on every WINCH.
				if prevCount == 0 {
					_ = a.term.Resize(pc, pr+1)
					_ = a.term.Resize(pc, pr)
				}
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
				// No one left to receive frames — stop all window streams so
				// we're not capturing the screen into the void, and release any
				// held mouse button / modifier so leaving the page can never
				// strand the host's desktop in a grab.
				a.stopWindowStream("")
				a.closeAllRTCPeers()
				a.enqueueWinOp(func() { _ = a.windows().releaseInput() })
			}
			a.viewerSizeMu.Lock()
			a.viewerCount = msg.Count
			// Going from 2+ → 1: clear the stored size so the PTY won't
			// linger at whatever the departed viewer last set. Paired with
			// the broadcastSize(0,0) below, the lone remaining viewer
			// re-reports its size and drives the PTY afresh.
			rebroadcast := msg.Count == 1
			if rebroadcast {
				a.viewerCols = 0
				a.viewerRows = 0
			}
			a.viewerSizeMu.Unlock()
			a.updateActiveViewers(msg.Count)
			a.syncViewerList(msg.Count, false)
			// Ask the lone remaining viewer to re-publish its
			// viewport size — sentinel (cols=0, rows=0) means
			// "tell me your size". Without this prompt, the viewer
			// keeps its xterm at the size dictated by whichever
			// smaller viewer just left and the PTY can't grow back
			// (visualViewport events alone don't trigger a fresh
			// resize when nothing changed locally).
			if rebroadcast {
				a.broadcastSize(0, 0)
			}
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

		// Fresh attach (or a viewer that fell behind past what we still
		// buffer): instead of replaying the whole raw output history — which
		// the viewer's terminal re-executes mutation-by-mutation, the
		// "fast-forward replay" — send one snapshot that paints the current
		// screen + scrollback directly. Tagged with the latest seq so
		// up-to-date viewers drop it and only the new joiner applies it.
		if a.screen != nil && cursor <= a.buf.OldestSeq() {
			if frame, latest := a.snapshotFrame(); frame != "" {
				if err := a.writeMsg(conn, protocol.Message{
					Type: protocol.TypeData,
					Data: frame,
					Seq:  latest,
				}); err != nil {
					return err
				}
				cursor = latest + 1
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

// handleKexInit completes one EKE handshake on behalf of a viewer.
// Silent on every failure mode (malformed input, low-order point,
// bad encoding): a malicious or buggy peer can't probe us for
// distinguishable error replies, and a legitimate viewer that
// gets no kex_resp will time out and reconnect via the normal path.
// kexBurst is how many kex handshakes we answer back-to-back before the
// throttle bites — comfortably covers several viewers connecting at once plus
// a legit user fat-fingering the PIN a few times.
const kexBurst = 8

// kexRefill is how long it takes to earn back one kex token. At steady state
// an attacker gets ~6 guesses/min, so the 10^6 PIN space takes ~115 days of
// continuous, conspicuous handshake spam — while a real viewer reconnecting
// occasionally never notices.
const kexRefill = 10 * time.Second

// allowKex reports whether we should answer another kex_init right now,
// draining one token from the bucket if so. A refused attempt is dropped
// silently (the viewer just sees a handshake timeout and can retry later).
func (a *Agent) allowKex(now time.Time) bool {
	a.kexMu.Lock()
	defer a.kexMu.Unlock()
	if a.kexLast.IsZero() {
		a.kexTokens = kexBurst
	} else {
		a.kexTokens += now.Sub(a.kexLast).Seconds() / kexRefill.Seconds()
		if a.kexTokens > kexBurst {
			a.kexTokens = kexBurst
		}
	}
	a.kexLast = now
	if a.kexTokens < 1 {
		return false
	}
	a.kexTokens--
	return true
}

func (a *Agent) handleKexInit(conn *websocket.Conn, exIDHex, dataB64 string) {
	if !a.allowKex(time.Now()) {
		return
	}
	exID, err := crypto.ParseExID(exIDHex)
	if err != nil {
		return
	}
	blindedViewer, err := base64.StdEncoding.DecodeString(dataB64)
	if err != nil || len(blindedViewer) != crypto.PubKeyBytes {
		return
	}
	viewerPub, err := crypto.UnblindPub(blindedViewer, a.pin)
	if err != nil {
		return
	}
	peerKey, err := crypto.PeerPublicKey(viewerPub)
	if err != nil {
		// Low-order or otherwise invalid; could be a wrong PIN on the
		// peer's side (their mask landed on a bad point) or just a
		// junk message. Either way, refuse silently.
		return
	}
	eph, err := crypto.NewEphemeralKey()
	if err != nil {
		return
	}
	shared, err := eph.ECDH(peerKey)
	if err != nil {
		return
	}
	wrapped, err := crypto.WrapSessionKey(shared, exID, a.sessionKey)
	if err != nil {
		return
	}
	blindedAgent, err := crypto.BlindPub(eph.PublicKey().Bytes(), a.pin)
	if err != nil {
		return
	}
	_ = a.writeMsg(conn, protocol.Message{
		Type: protocol.TypeKexResp,
		ExID: exIDHex,
		Data: base64.StdEncoding.EncodeToString(blindedAgent),
		Wrap: base64.StdEncoding.EncodeToString(wrapped),
	})
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
