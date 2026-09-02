// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/reminal/reminal/internal/client"
	"github.com/reminal/reminal/internal/config"
	"github.com/reminal/reminal/internal/keepawake"
	"github.com/reminal/reminal/internal/proc"
	"github.com/reminal/reminal/internal/pty"
	"github.com/reminal/reminal/internal/session"
	"github.com/reminal/reminal/internal/updater"
	"golang.org/x/term"
)

// version, buildDate, and commit are stamped at build time via
//
//	-ldflags "-X main.version=… -X main.buildDate=… -X main.commit=…"
//
// in scripts/build.sh and the release workflow. Dev builds keep their
// placeholder values so the updater skips the upgrade prompt and version
// --verbose still shows something readable.
var (
	version   = "dev"
	buildDate = "unknown"
	commit    = "unknown"
)

func main() {
	// Make the console render ANSI escapes (no-op outside Windows/conhost).
	enableVTConsole()
	if err := config.ValidateRelayURLs(); err != nil {
		fmt.Fprintf(os.Stderr, "configuration error: %v\n", err)
		os.Exit(2)
	}

	// Hot-restart resume path. The previous binary image hit
	// syscall.Exec on us with REMINAL_RESUME=1 + session credentials in
	// env + the PTY master inherited as fd 3. Take over without
	// spawning a new shell or generating a new session ID.
	if resume, err := client.LoadResumeState(); err != nil {
		fmt.Fprintf(os.Stderr, "resume error: %v\n", err)
		os.Exit(1)
	} else if resume != nil {
		agent, err := client.NewAgentWith(version, client.AgentOptions{Resume: resume, HandshakeAddr: resume.HandshakeAddr})
		if err != nil {
			fmt.Fprintf(os.Stderr, "resume error: %v\n", err)
			os.Exit(1)
		}
		if err := agent.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "__ptyhold":
			// Hidden: the per-session ConPTY holder that owns the shell so hot
			// restart can swap the agent underneath it (Windows-only; see
			// internal/pty/holder_windows.go).
			if len(os.Args) < 4 {
				os.Exit(2)
			}
			// Optional trailing "<cols> <rows> <cursorRow> <cursorCol>": what
			// the agent measured about its console before spawning us, so the
			// pseudo console is built at the right size and starts below what
			// is already on screen instead of clearing it. Anything missing or
			// unparsable degrades to the 80x24 default with no cursor
			// inheritance — the pre-3.0.13 behaviour.
			var opts pty.HolderOpts
			dim := func(i int) uint16 {
				if i >= len(os.Args) {
					return 0
				}
				v, err := strconv.Atoi(os.Args[i])
				if err != nil || v <= 0 || v > math.MaxUint16 {
					return 0
				}
				return uint16(v)
			}
			opts.Cols, opts.Rows = dim(4), dim(5)
			opts.CursorRow, opts.CursorCol = dim(6), dim(7)
			if err := pty.RunHolder(os.Args[2], os.Args[3], opts); err != nil {
				fmt.Fprintf(os.Stderr, "ptyhold: %v\n", err)
				os.Exit(1)
			}
			return
		case "relay":
			port := ""
			if len(os.Args) > 2 {
				port = os.Args[2]
			}
			if err := client.RunRelay(port); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
			return
		case "version", "-v", "--version":
			if len(os.Args) > 2 && (os.Args[2] == "--verbose" || os.Args[2] == "-v") {
				printVersionInfo()
			} else {
				fmt.Println(version)
			}
			return
		case "upgrade":
			updated, err := updater.Upgrade(version)
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
			if updated {
				// Re-INSTALL (not just restart) so the service DEFINITION is refreshed —
				// this is how a plist fix (e.g. dropping ProcessType=Background, which
				// was throttling spawned sessions) reaches machines set up by an older
				// version. It also moves the daemon onto the new binary. On darwin ALWAYS
				// do it: a bare→bundle migration just installed the app with no daemon
				// yet, and correctness (not version) is what we key on. Linux keeps the
				// refresh-if-present behavior (its daemon is ownership-driven).
				// Best-effort.
				if runtime.GOOS == "darwin" || client.DaemonServiceInstalled() {
					_ = client.InstallDaemonService()
				}
			}
			return
		case "info":
			jsonOut, all, withQR := false, false, false
			arg := ""
			for _, a := range os.Args[2:] {
				switch {
				case a == "--json" || a == "-j":
					jsonOut = true
				case a == "--all" || a == "-a":
					all = true
				case a == "--qr":
					withQR = true
				case !strings.HasPrefix(a, "-") && arg == "":
					arg = a
				}
			}
			if err := runInfo(arg, jsonOut, all, withQR); err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}
			return
		case "doctor":
			if err := client.Doctor(version); err != nil {
				os.Exit(1)
			}
			return
		case "permissions", "permission":
			// If an older `reminal upgrade` left us a loose binary, heal into the
			// bundle first (and re-exec) — otherwise EnsurePermissions can only report
			// "this build isn't a bundle". This is the command users run right after
			// upgrading, so it must complete the upgrade, not dead-end.
			selfHealBundle()
			// macOS: surface all three TCC prompts (Screen Recording, Accessibility,
			// Automation) for the reminal.app identity so one grant of each covers
			// background ("+") sessions too.
			if err := client.EnsurePermissions(); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
			return
		case "__request-permissions":
			// Hidden: runs INSIDE the LaunchServices-launched reminal.app (see
			// `reminal permissions`) to trigger the TCC prompt(s) as sh.reminal. An
			// optional arg (screen|accessibility|automation) triggers just that one so
			// the guided flow can request them one at a time; no arg does all three.
			which := ""
			if len(os.Args) > 2 {
				which = os.Args[2]
			}
			_ = client.RequestPermission(which)
			return
		case "mcp":
			// Stdio MCP server. Speaks JSON-RPC on stdout, so nothing else may
			// print there — errors go to stderr and a non-zero exit.
			if err := runMCP(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
			return
		case "integrate":
			if err := runIntegrate(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
			return
		case "settings":
			if err := runSettings(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
			return
		case "qr":
			arg := ""
			for _, a := range os.Args[2:] {
				if !strings.HasPrefix(a, "-") && arg == "" {
					arg = a
				}
			}
			if err := runQR(arg); err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}
			return
		case "completion":
			shell := ""
			if len(os.Args) > 2 {
				shell = os.Args[2]
			}
			if shell == "" {
				fmt.Fprintln(os.Stderr, "usage: reminal completion <bash|zsh|fish|powershell>")
				os.Exit(1)
			}
			if err := client.Completion(shell); err != nil {
				os.Exit(1)
			}
			return
		case "__complete":
			// Hidden helper the generated shell-completion scripts call to
			// list session id/name/port candidates for `attach`/`kill`/etc.
			// Best-effort and silent — completion must never print errors.
			client.CompleteSessions()
			return
		case "daemon":
			// Hidden. The always-on directory host: keeps an owned machine
			// reachable (listable + "+"-spawnable) even with no live session.
			// Installed as a login service when an owner is enrolled (see
			// runAddOwner); not a user-facing command. `--install`/`--uninstall`
			// manage that service; bare `daemon` runs the host in the foreground.
			if err := runDaemon(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
			return
		case "connect":
			if len(os.Args) < 3 {
				fmt.Fprintln(os.Stderr, "usage: reminal connect <session-id-or-url> [pin] [--owner]")
				os.Exit(1)
			}
			target := ""
			pinArg := ""
			owner := false
			for _, a := range os.Args[2:] {
				switch {
				case a == "--owner":
					owner = true
				case target == "":
					target = a
				case pinArg == "":
					pinArg = a
				}
			}
			if err := runConnect(target, pinArg, owner); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
			return
		case "attach":
			idArg := ""
			if len(os.Args) > 2 && !strings.HasPrefix(os.Args[2], "-") {
				idArg = os.Args[2]
			}
			if err := runAttach(idArg); err != nil {
				if errors.Is(err, errPickCancelled) {
					return // user backed out of the picker — not an error
				}
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
			return
		case "stop":
			idArg := ""
			yes := false
			for _, a := range os.Args[2:] {
				switch a {
				case "-y", "--yes":
					yes = true
				default:
					if !strings.HasPrefix(a, "-") && idArg == "" {
						idArg = a
					}
				}
			}
			if err := runStop(idArg, yes); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
			return
		case "send":
			if len(os.Args) < 3 {
				fmt.Fprintln(os.Stderr, "usage: reminal send <file>")
				os.Exit(1)
			}
			if err := runSend(os.Args[2]); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
			return
		case "copy":
			if err := runCopyCmd(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
			return
		case "paste":
			if err := runPasteCmd(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
			return
		case "notify":
			if len(os.Args) < 3 {
				fmt.Fprintln(os.Stderr, "usage: reminal notify <message>")
				os.Exit(1)
			}
			msg := strings.Join(os.Args[2:], " ")
			if err := runNotify(msg); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
			return
		case "connections":
			if err := runConnections(); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
			return
		case "new":
			// Name may be given as `reminal new <name>` (positional) or
			// `reminal new --name <name>` / `--name=<name>`. First non-flag
			// arg wins for the positional form.
			name := ""
			for i := 2; i < len(os.Args); i++ {
				a := os.Args[i]
				switch {
				case a == "--name" || a == "-name":
					if i+1 < len(os.Args) {
						name = os.Args[i+1]
						i++
					}
				case strings.HasPrefix(a, "--name="):
					name = strings.TrimPrefix(a, "--name=")
				case strings.HasPrefix(a, "-name="):
					name = strings.TrimPrefix(a, "-name=")
				case !strings.HasPrefix(a, "-") && name == "":
					name = a
				}
			}
			// Heal a loose post-upgrade install into the bundle first — a "+"
			// background session needs the daemon the bundle carries.
			selfHealBundle()
			if err := runNew(name); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
			return
		case "list", "ls":
			if err := runList(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
			return
		case "prune":
			idle := 30 * time.Minute
			yes := false
			parseIdle := func(s string) time.Duration {
				d, derr := parseDuration(s)
				if derr != nil {
					fmt.Fprintf(os.Stderr, "reminal prune: %v\n", derr)
					os.Exit(2)
				}
				return d
			}
			for i := 2; i < len(os.Args); i++ {
				a := os.Args[i]
				switch {
				case a == "-y" || a == "--yes":
					yes = true
				case a == "--idle":
					if i+1 < len(os.Args) {
						idle = parseIdle(os.Args[i+1])
						i++
					}
				case strings.HasPrefix(a, "--idle="):
					idle = parseIdle(strings.TrimPrefix(a, "--idle="))
				case !strings.HasPrefix(a, "-"):
					// Positional shorthand: `reminal prune 12h`, `reminal prune 1d`.
					idle = parseIdle(a)
				}
			}
			if err := runPrune(idle, yes); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
			return
		case "rename":
			// One arg renames the CURRENT session (the one whose shell you're
			// in — no id needed, which is the only sane option on mobile):
			//   reminal rename <new-name>
			// Two+ args target another session explicitly, remaining args
			// joined so multi-word names work without quoting:
			//   reminal rename <id|name> <new-name…>
			rest := os.Args[2:]
			var target, newName string
			switch {
			case len(rest) == 0:
				fmt.Fprintln(os.Stderr, "usage: reminal rename [id|name] <new-name>")
				fmt.Fprintln(os.Stderr, "  inside a session, just: reminal rename <new-name>")
				os.Exit(2)
			case len(rest) == 1:
				target, newName = "", rest[0] // current session
			default:
				target, newName = rest[0], strings.Join(rest[1:], " ")
			}
			if err := runRename(target, newName); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
			return
		case "restart":
			// No arg → the current/active session. An id|name → that session.
			// --all → every shell session (handy after `reminal upgrade` to
			// roll the whole machine onto the new binary at once).
			target := ""
			all := false
			for _, a := range os.Args[2:] {
				switch a {
				case "-a", "--all":
					all = true
				default:
					if !strings.HasPrefix(a, "-") && target == "" {
						target = a
					}
				}
			}
			var rerr error
			if all {
				rerr = runRestartAll()
			} else {
				rerr = runRestart(target)
			}
			if rerr != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", rerr)
				os.Exit(1)
			}
			return
		case "expose":
			port := 0
			public := false
			for _, a := range os.Args[2:] {
				switch a {
				case "--public":
					public = true
				default:
					if !strings.HasPrefix(a, "-") && port == 0 {
						if _, err := fmt.Sscanf(a, "%d", &port); err != nil || port <= 0 {
							fmt.Fprintf(os.Stderr, "reminal expose: %q is not a valid port number\n", a)
							os.Exit(2)
						}
					}
				}
			}
			if port == 0 {
				fmt.Fprintln(os.Stderr, "usage: reminal expose <port> [--public]")
				os.Exit(2)
			}
			if err := runExpose(port, public); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
			return
		case "kill":
			idArg := ""
			yes := false
			for _, a := range os.Args[2:] {
				switch a {
				case "-y", "--yes":
					yes = true
				default:
					if !strings.HasPrefix(a, "-") && idArg == "" {
						idArg = a
					}
				}
			}
			if err := runKill(idArg, yes); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
			return
		case "own":
			if err := runOwn(); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
			return
		case "add":
			if len(os.Args) >= 3 && os.Args[2] == "owner" {
				if err := runAddOwner(os.Args[3:]); err != nil {
					fmt.Fprintf(os.Stderr, "error: %v\n", err)
					os.Exit(1)
				}
				return
			}
			fmt.Fprintln(os.Stderr, "usage: reminal add owner <id> [--label <name>] [-y]")
			os.Exit(1)
			return
		case "owners":
			if err := runOwners(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
			return
		case "machines":
			if err := runMachines(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
			return
		case "help", "-h", "--help":
			printHelp()
			return
		}
		// Anything that looks like a subcommand attempt (doesn't start
		// with "-") but didn't match any case is a typo. Bailing here
		// prevents the silent fall-through to agent mode that would
		// spawn a second agent on top of an existing one.
		if !strings.HasPrefix(os.Args[1], "-") {
			fmt.Fprintf(os.Stderr, "reminal: unknown command %q\nRun `reminal help` for available commands.\n", os.Args[1])
			os.Exit(2)
		}
	}

	connect := flag.String("connect", "", "session ID or full relay URL to connect to (URL may include #p=PIN)")
	pin := flag.String("pin", "", "PIN for the remote session (prompted if omitted)")
	ownerConnect := flag.Bool("owner", false, "connect PIN-free as an enrolled owner device")
	verbose := flag.Bool("v", false, "verbose mode — append raw error detail to status lines (same as REMINAL_DEBUG=1)")
	verboseLong := flag.Bool("verbose", false, "alias for -v")
	name := flag.String("name", "", "human-friendly label for this session, shown in `reminal list` and usable in place of the ID")
	headless := flag.Bool("headless", false, "run without owning the host terminal — for spawned background sessions; users normally invoke this via `reminal new`")
	handshakeFD := flag.Int("handshake-fd", 0, "fd inherited from `reminal new` for the credentials handshake (internal, Unix)")
	handshakeAddr := flag.String("handshake-addr", "", "loopback address to report the credentials handshake to (internal, Windows)")
	exposeHeadless := flag.Bool("expose-headless", false, "run as a headless port-forwarder; users normally invoke this via `reminal expose <port>`")
	exposePort := flag.Int("expose-port", 0, "local TCP port to forward (used with --expose-headless)")
	exposePublic := flag.Bool("expose-public", false, "skip PIN gate on the port forward (used with --expose-headless)")
	flag.Parse()

	if *verbose || *verboseLong {
		os.Setenv("REMINAL_DEBUG", "1")
	}

	// Headless port-forwarder path. Like the shell --headless path
	// below, but spins up a Tunnel instead of an Agent. Internal flag —
	// users invoke this indirectly via `reminal expose <port>`.
	if *exposeHeadless {
		tun, err := client.NewTunnel(client.TunnelOptions{
			Port:          *exposePort,
			Public:        *exposePublic,
			HandshakeFD:   *handshakeFD,
			HandshakeAddr: *handshakeAddr,
			Version:       version,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if err := tun.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Headless agent path: skip the upgrade prompt + the
	// "already-running, attach?" prompt — neither makes sense for a
	// detached background child. Run the agent directly.
	if *headless {
		// The detached child has no argv we control, so `reminal new` passes
		// the name via REMINAL_NEW_NAME (see client.Spawn). Fall back to the
		// --name flag for a directly-invoked `reminal --headless --name`.
		hlName := *name
		if hlName == "" {
			hlName = os.Getenv("REMINAL_NEW_NAME")
		}
		opts := client.AgentOptions{Headless: true, HandshakeFD: *handshakeFD, HandshakeAddr: *handshakeAddr, Name: hlName}
		agent, err := client.NewAgentWith(version, opts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		keepawake.ReapOrphans() // clean up any inhibitors a previous (hot-restarted) incarnation leaked
		stopKeepAwake := keepawake.Start()
		defer stopKeepAwake()
		if err := agent.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Offer to upgrade if a newer release is available. Runs before we hand
	// stdin off to the PTY (agent) or raw mode (viewer); silently no-ops on
	// dev builds, brew-managed installs, network failures, or cache hits.
	updater.CheckAndPromptOnStart(version)

	if *connect != "" {
		if err := runConnect(*connect, *pin, *ownerConnect); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Running `reminal` from inside a reminal-broadcast shell would cause
	// an output feedback loop — the PTY echoes the agent's banner, which
	// pumpPTY broadcasts, which the viewer renders, which… etc.
	// Refuse + point at the obvious fix. Other "already running" cases
	// are allowed: `reminal` always starts a fresh foreground session,
	// and `reminal new` is for explicit background spawns.
	if inside := os.Getenv("REMINAL_SESSION"); inside != "" {
		if _, err := session.ReadActiveByID(inside); err == nil {
			fmt.Fprintf(os.Stderr,
				"You're already inside reminal session %s (this shell IS the shared shell).\n", inside)
			fmt.Fprintln(os.Stderr, "  To stop this session:    reminal stop")
			fmt.Fprintln(os.Stderr, "  To see join info / QR:   reminal info")
			fmt.Fprintln(os.Stderr, "  To create another one:   reminal new")
			os.Exit(2)
		}
	}

	// darwin self-heal: if an older `reminal upgrade` left us a loose binary,
	// re-materialize the app bundle + daemon and re-exec from it. One-shot.
	selfHealBundle()

	agent, err := client.NewAgentWith(version, client.AgentOptions{Name: *name})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	keepawake.ReapOrphans() // clean up any inhibitors a previous (hot-restarted) incarnation leaked
	stopKeepAwake := keepawake.Start()
	defer stopKeepAwake()
	if err := agent.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

type helpRow struct{ cmd, desc string }

// printHelp renders the CLI help styled and reflowed to the terminal width: a
// two-column command/description table when there's room, or the description
// stacked under each command on narrow terminals — so it never wraps into an
// unreadable jumble in an 80- or 40-column window. Piped (non-TTY) output is
// plain text at an 80-column assumption.
func printHelp() {
	w := helpWidth()
	tag := wrapText("reminal — remote terminal access from any browser or terminal", w)
	fmt.Println(cBold("reminal") + cDim(strings.TrimPrefix(tag[0], "reminal")))
	for _, ln := range tag[1:] {
		fmt.Println(cDim(ln))
	}
	fmt.Println()

	helpTable(w, "Commands", []helpRow{
		{"reminal [--name <name>]", "Share this terminal (works out of the box)"},
		{"reminal new [name]", "Spawn a detached background session (survives this terminal closing)"},
		{"reminal expose <port> [--public]", "Forward a local HTTP port to a public URL (PIN-protected by default)"},
		{"reminal list [filter] [-v]", "List sessions, recent-first; filter by id/name/cwd/title (--idle/--viewers/--headless)"},
		{"reminal prune [dur] [-y]", "Kill idle, unwatched shell sessions (default idle 30m+; e.g. 12h, 1d, 2w)"},
		{"reminal connect <session|url> [pin]", "Connect to a remote session (PIN prompted if omitted)"},
		{"reminal attach [id|name]", "Re-connect to a local session as a viewer (no arg -> interactive picker)"},
		{"reminal rename [id|name] <name>", "Rename a running session (inside one, just: reminal rename <name>)"},
		{"reminal own", "Print this device's owner id to paste into 'add owner' on a machine"},
		{"reminal add owner <id> [--label <n>]", "Enroll a device as an owner of this machine (needs sudo; asks to confirm, -y skips)"},
		{"reminal owners [rename|revoke|restore]", "List and manage this machine's owner devices"},
		{"reminal machines [rename <id> <name>]", "List every machine you own and its live sessions"},
		{"reminal stop [id|name|port] [-y]", "Stop the reminal layer — your shell/server keeps running"},
		{"reminal kill [id|name] [-y]", "Fully terminate a shell session (irreversible)"},
		{"reminal send <file>", "Push a file to every connected viewer (web auto-downloads)"},
		{"reminal copy [--ttl <dur>] [-f] <file>", "Offer a file for pickup elsewhere; prints a one-time code"},
		{"reminal paste <code> [dest]", "Fetch a file offered by 'reminal copy' (default dest: .)"},
		{"reminal notify <message>", "Push a notification to viewers (browser notification on web)"},
		{"reminal connections", "List currently attached viewers with connect time"},
		{"reminal info [id|name] [--qr|--json]", "Show connect details (ID/PIN/URL/QR)"},
		{"reminal qr [id|name]", "Print just the join QR"},
		{"reminal doctor", "Self-diagnostic: version, relay reachability, terminal, shell"},
		{"reminal settings", "Open the settings page (e.g. keep this Mac unlocked)"},
		{"reminal completion <bash|zsh|fish|powershell>", "Print a shell completion script"},
		{"reminal upgrade", "Upgrade to the latest release"},
		{"reminal restart [--all]", "Hot-swap the running agent(s) onto the latest binary"},
		{"reminal version [--verbose]", "Print version (--verbose adds build date / commit)"},
		{"reminal help", "Show this help"},
	})

	helpTable(w, "Environment", []helpRow{
		{"REMINAL_RELAY", "Relay WebSocket URL (overrides any build default)"},
		{"REMINAL_WEB", "Web UI URL (overrides any build default)"},
		{"REMINAL_LOCAL", "Set to 1 to use a localhost relay (with reminal relay)"},
		{"REMINAL_NO_KEEP_AWAKE", "Set to 1 to let the host sleep while reminal runs"},
		{"REMINAL_DEBUG", "Set to 1 to append raw error detail to status lines"},
		{"SHELL", "Shell to run (default: $SHELL, then zsh / bash / sh)"},
	})

	helpProse(w, "Security", "Each session needs a random 8-char id + 6-digit PIN, and terminal traffic is end-to-end encrypted — the relay can't read it. Inside a connect, local Ctrl+C goes to the remote shell; press Ctrl-] to disconnect the viewer cleanly (the host agent keeps running).")
	helpProse(w, "Naming", "Name a session (reminal new deploy) and use the name anywhere an id works — attach / kill / stop accept an exact id, an exact name, a unique id prefix, or a unique substring of the name / cwd / title.")

	helpTable(w, "Examples", []helpRow{
		{"reminal new deploy", "named background session"},
		{"reminal attach deploy", "attach to it by name"},
		{"reminal list ~/project", "filter by working directory"},
		{"reminal connect ABC12345 482916", "connect with id + pin"},
		{"reminal machines", "every machine you own, at a glance"},
	})

	fmt.Println(cDim("Bug reports + feature requests: https://github.com/harshalgajjar/Reminal/issues"))
}

// helpWidth is the terminal width to reflow help to, or 80 when output isn't a
// terminal (piped) or the size is unknown.
func helpWidth() int {
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 20 {
		return w
	}
	return 80
}

// wrapText word-wraps s to at most w columns without splitting words. Always
// returns at least one line.
func wrapText(s string, w int) []string {
	if w < 8 {
		w = 8
	}
	words := strings.Fields(s)
	if len(words) == 0 {
		return []string{""}
	}
	lines := []string{}
	cur := words[0]
	for _, word := range words[1:] {
		if len([]rune(cur))+1+len([]rune(word)) <= w {
			cur += " " + word
		} else {
			lines = append(lines, cur)
			cur = word
		}
	}
	return append(lines, cur)
}

// helpTable prints a titled command/description table. Two columns when the
// description column can start with room to spare; otherwise the description is
// stacked (wrapped) under each bold command.
func helpTable(width int, title string, rows []helpRow) {
	fmt.Println(cBold(title))
	maxCmd := 0
	for _, r := range rows {
		if l := len([]rune(r.cmd)); l > maxCmd {
			maxCmd = l
		}
	}
	const indent, gap = 2, 2
	descCol := indent + maxCmd + gap
	twoCol := descCol+28 <= width
	for _, r := range rows {
		if twoCol {
			pad := strings.Repeat(" ", maxCmd-len([]rune(r.cmd)))
			lines := wrapText(r.desc, width-descCol)
			fmt.Println(strings.Repeat(" ", indent) + cBold(r.cmd) + pad + strings.Repeat(" ", gap) + cDim(lines[0]))
			for _, ln := range lines[1:] {
				fmt.Println(strings.Repeat(" ", descCol) + cDim(ln))
			}
		} else {
			fmt.Println(strings.Repeat(" ", indent) + cBold(r.cmd))
			for _, ln := range wrapText(r.desc, width-indent-2) {
				fmt.Println(strings.Repeat(" ", indent+2) + cDim(ln))
			}
		}
	}
	fmt.Println()
}

// helpProse prints a titled, word-wrapped paragraph.
func helpProse(width int, title, body string) {
	fmt.Println(cBold(title))
	for _, ln := range wrapText(body, width-2) {
		fmt.Println("  " + cDim(ln))
	}
	fmt.Println()
}

// selfHealBundle re-materializes the reminal.app bundle when we're running as a
// loose darwin release binary — the state an OLDER version's `reminal upgrade`
// leaves behind (it extracts the inner binary loose, stripping the sh.reminal
// identity + always-on daemon). It installs THIS exact version's bundle, ensures
// the daemon, and re-execs from the bundle so the command the user actually ran
// (`reminal`, `reminal permissions`, …) continues under the correct identity.
// No-op once bundled, on non-darwin, and for dev builds. Called from the entry
// points a user hits right after upgrading, so the upgrade completes instead of
// dead-ending on "this build isn't a bundle".
func selfHealBundle() {
	newBin, healed := updater.EnsureBundleInstalled(version)
	if !healed {
		return
	}
	client.EnsureDaemonInstalled()
	if err := execReplace(newBin); err != nil {
		fmt.Fprintln(os.Stderr, "reminal: installed the app bundle — please re-run your command.")
		os.Exit(0)
	}
}

// printVersionInfo prints a multi-line build-detail block — version,
// build date, commit, Go version, OS/arch — for bug reports and CI logs.
// Triggered by `reminal version --verbose`.
func printVersionInfo() {
	fmt.Printf("reminal %s\n", version)
	fmt.Printf("  built:   %s\n", buildDate)
	fmt.Printf("  commit:  %s\n", commit)
	fmt.Printf("  go:      %s\n", runtime.Version())
	fmt.Printf("  os/arch: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Println("  bugs:    https://github.com/harshalgajjar/Reminal/issues")
}

// runConnections asks the local agent for its live viewer list and prints
// a short human-readable table: total count + each viewer's connect age.
// The list is best-effort (the relay only sends count deltas, so the
// agent can't perfectly identify which viewer left when one disconnects).
func runConnections() error {
	a, err := resolveActive("")
	if err != nil {
		return err
	}
	payload, err := sendControl(a.PID, "connections")
	if err != nil {
		return err
	}
	var stamps []time.Time
	if err := json.Unmarshal([]byte(payload), &stamps); err != nil {
		return fmt.Errorf("parse agent reply: %w", err)
	}
	if len(stamps) == 0 {
		fmt.Printf("Session %s has no viewers attached.\n", a.ID)
		return nil
	}
	noun := "viewer"
	if len(stamps) != 1 {
		noun = "viewers"
	}
	fmt.Printf("Session %s · %d %s attached:\n", a.ID, len(stamps), noun)
	now := time.Now()
	for _, t := range stamps {
		age := now.Sub(t).Round(time.Second)
		fmt.Printf("  · joined %v ago (at %s)\n", age, t.Format("15:04:05"))
	}
	return nil
}

// runNotify fires a one-shot notification to every connected viewer. Useful
// at the tail of a long pipeline so a phone-toting user gets pinged:
//
//	$ make build && reminal notify "build done"
func runNotify(message string) error {
	if strings.TrimSpace(message) == "" {
		return errors.New("message required")
	}
	a, err := resolveActive("")
	if err != nil {
		return err
	}
	// Control socket protocol is line-delimited: the agent reads up to
	// the first '\n'. Any embedded newline would silently truncate the
	// message ("build failed\nsee log" → just "build failed"). Collapse
	// CR/LF runs into a single " · " separator so multi-line messages
	// stay legible without breaking the wire format.
	flat := strings.Join(strings.FieldsFunc(message, func(r rune) bool {
		return r == '\n' || r == '\r'
	}), " · ")
	_, err = sendControl(a.PID, "notify "+flat)
	return err
}

// sendControl is the shared dial-and-send helper for the agent's Unix
// control socket. Used by every `reminal <verb>` that needs the agent to
// take action. Returns the payload after "ok " on success, "" if the
// reply is just "ok\n", or an error if the reply starts with "error:".
//
// An optional timeout bounds the exchange. Most verbs want no deadline — a
// `reminal send` of a large file legitimately takes a while — but anything on
// the path to killing a session needs one: a hung agent still has a listening
// socket, so the dial succeeds and the read would otherwise block forever,
// letting an unresponsive agent refuse its own termination.
func sendControl(pid int, cmd string, timeout ...time.Duration) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	sock := filepath.Join(home, ".reminal", fmt.Sprintf("agent-%d.sock", pid))
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return "", fmt.Errorf("connect to agent: %w", err)
	}
	defer conn.Close()
	if len(timeout) > 0 && timeout[0] > 0 {
		_ = conn.SetDeadline(time.Now().Add(timeout[0]))
	}
	if _, err := fmt.Fprintln(conn, cmd); err != nil {
		return "", err
	}
	reply, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return "", err
	}
	reply = strings.TrimRight(reply, "\r\n")
	switch {
	case reply == "ok":
		return "", nil
	case strings.HasPrefix(reply, "ok "):
		return strings.TrimPrefix(reply, "ok "), nil
	default:
		return "", fmt.Errorf("agent: %s", strings.TrimSpace(strings.TrimPrefix(reply, "error:")))
	}
}

// runCopyCmd implements `reminal copy [--ttl <dur>] [--foreground] <file>`.
// By default it forks a detached holder so the shell isn't blocked;
// --foreground (-f) keeps the offer in this terminal. --__hold/--handshake-fd
// are internal flags the detached holder is re-invoked with.
func runCopyCmd(args []string) error {
	ttl := client.DefaultCopyTTL
	var path string
	foreground := false
	hold := false
	handshakeFD := 0
	handshakeAddr := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--ttl" && i+1 < len(args):
			d, err := time.ParseDuration(args[i+1])
			if err != nil {
				return fmt.Errorf("bad --ttl %q: %w", args[i+1], err)
			}
			ttl = d
			i++
		case strings.HasPrefix(a, "--ttl="):
			d, err := time.ParseDuration(strings.TrimPrefix(a, "--ttl="))
			if err != nil {
				return fmt.Errorf("bad --ttl: %w", err)
			}
			ttl = d
		case a == "--foreground" || a == "-f":
			foreground = true
		case a == "--__hold":
			hold = true
		case a == "--handshake-fd" && i+1 < len(args):
			handshakeFD = client.ParseHandshakeFD(args[i : i+2])
			i++
		case a == "--handshake-addr" && i+1 < len(args):
			handshakeAddr = args[i+1]
			i++
		case !strings.HasPrefix(a, "-") && path == "":
			path = a
		}
	}
	if path == "" {
		return errors.New("usage: reminal copy [--ttl <dur>] [--foreground] <file>")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	switch {
	case hold:
		return client.RunCopyHold(abs, ttl, handshakeFD, handshakeAddr)
	case foreground:
		return client.RunCopy(abs, ttl)
	default:
		return client.RunCopyBackground(abs, ttl)
	}
}

// runPasteCmd implements `reminal paste <code> [destination]`.
func runPasteCmd(args []string) error {
	var code, dest string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		if code == "" {
			code = a
		} else if dest == "" {
			dest = a
		}
	}
	if code == "" {
		return errors.New("usage: reminal paste <code> [destination]")
	}
	return client.RunPaste(code, dest)
}

// runSend connects to the local agent's control socket and asks it to
// broadcast the given file to every connected viewer as a TypeDownload
// message. The file is read by the AGENT (not this process), so the path
// must be valid from the agent's working directory perspective — which is
// guaranteed when invoked from inside the shared shell.
func runSend(path string) error {
	a, err := resolveActive("")
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if _, err := os.Stat(abs); err != nil {
		return err
	}
	// Control socket protocol is line-delimited; an embedded newline
	// in the path would be silently truncated by the agent, then
	// fail with a confusing "no such file" referring to the
	// truncated prefix. Reject upfront with a clear error.
	if strings.ContainsAny(abs, "\n\r") {
		return fmt.Errorf("path contains control characters (rename the file or use a symlink): %q", abs)
	}
	if _, err := sendControl(a.PID, "send "+abs); err != nil {
		return err
	}
	fmt.Printf("Sent %s to viewers.\n", filepath.Base(abs))
	return nil
}

// resolveActive picks a target session from the supplied arg, which may
// be either a session ID, a port number (for port-forwards), or empty.
// Resolution order when no arg is given:
//  1. REMINAL_SESSION env var (set by the agent inside the shared shell
//     — "this terminal" is the most natural default for any command
//     typed at the host's own prompt).
//  2. The single running session if there's exactly one.
//  3. Otherwise: print the list and require the caller to disambiguate
//     so `reminal stop`/`kill`/`attach` never silently target the
//     wrong agent.
func resolveActive(arg string) (*session.Active, error) {
	arg = strings.TrimSpace(arg)
	all, err := session.ReadAllActive()
	if err != nil {
		return nil, err
	}
	if len(all) == 0 {
		return nil, errors.New("no active reminal session on this machine — start one with `reminal`, `reminal new`, or `reminal expose <port>`")
	}
	if arg != "" {
		// All-digit arg → look up by port (for port-forwards).
		if isAllDigits(arg) {
			var port int
			if _, err := fmt.Sscanf(arg, "%d", &port); err == nil {
				for i := range all {
					if all[i].IsPort() && all[i].Port == port {
						return &all[i], nil
					}
				}
				return nil, fmt.Errorf("no port forward on port %d (running: %s)", port, joinIDs(all))
			}
		}
		// Resolution precedence, most-specific first. Exact ID stays at the
		// top so every existing `reminal attach/kill/stop <ID>` keeps working
		// unchanged; name / prefix / substring are pure fallbacks layered
		// underneath. Each fallback tier must match exactly one session — an
		// ambiguous match errors with the candidates rather than guessing.
		upper := strings.ToUpper(arg)
		for i := range all {
			if all[i].ID == upper {
				return &all[i], nil // 1. exact ID (case-insensitive)
			}
		}
		if m := matchSessions(all, func(a session.Active) bool {
			return a.Name != "" && strings.EqualFold(a.Name, arg)
		}); len(m) == 1 {
			return m[0], nil // 2. exact name
		} else if len(m) > 1 {
			return nil, fmt.Errorf("%q matches multiple sessions by name: %s", arg, describeMatches(m))
		}
		if m := matchSessions(all, func(a session.Active) bool {
			return strings.HasPrefix(a.ID, upper)
		}); len(m) == 1 {
			return m[0], nil // 3. unique ID prefix
		} else if len(m) > 1 {
			return nil, fmt.Errorf("id prefix %q is ambiguous: %s", arg, describeMatches(m))
		}
		lower := strings.ToLower(arg)
		if m := matchSessions(all, func(a session.Active) bool {
			return strings.Contains(strings.ToLower(a.Name), lower) ||
				strings.Contains(strings.ToLower(a.Cwd), lower) ||
				strings.Contains(strings.ToLower(a.Title), lower)
		}); len(m) == 1 {
			return m[0], nil // 4. unique substring of name / cwd / title
		} else if len(m) > 1 {
			return nil, fmt.Errorf("%q matches multiple sessions: %s", arg, describeMatches(m))
		}
		return nil, fmt.Errorf("no active session matching %q (running: %s)", arg, joinIDs(all))
	}
	if inside := strings.ToUpper(strings.TrimSpace(os.Getenv("REMINAL_SESSION"))); inside != "" {
		for i := range all {
			if all[i].ID == inside {
				return &all[i], nil
			}
		}
	}
	if len(all) == 1 {
		return &all[0], nil
	}
	return nil, fmt.Errorf("multiple sessions running — pick one (%s)", joinIDs(all))
}

// matchSessions returns pointers to the elements of all satisfying pred,
// preserving order. Pointers index into the caller's slice, so the result
// stays valid as long as all does.
func matchSessions(all []session.Active, pred func(session.Active) bool) []*session.Active {
	var out []*session.Active
	for i := range all {
		if pred(all[i]) {
			out = append(out, &all[i])
		}
	}
	return out
}

// describeMatches formats ambiguous-match candidates as "ID (name)" so the
// error tells the user exactly what to disambiguate between.
func describeMatches(m []*session.Active) string {
	parts := make([]string, len(m))
	for i, a := range m {
		if a.Name != "" {
			parts[i] = fmt.Sprintf("%s (%s)", a.ID, a.Name)
		} else {
			parts[i] = a.ID
		}
	}
	return strings.Join(parts, ", ")
}

func joinIDs(all []session.Active) string {
	ids := make([]string, len(all))
	for i, a := range all {
		if a.IsPort() {
			ids[i] = fmt.Sprintf("%s (port %d)", a.ID, a.Port)
		} else {
			ids[i] = a.ID
		}
	}
	return strings.Join(ids, ", ")
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// runStop tells the agent running on this machine to stop broadcasting to
// the relay (closes the WS, clears active record, kicks any viewers) while
// keeping its PTY pumps alive — the host's terminal continues working as a
// plain local shell. Useful when you've returned to your laptop and don't
// need the remote-share open anymore but want to keep the shell session.
//
// Always prints the consequences before acting so the user doesn't trigger
// a viewer disconnect by mistake. `-y` skips the "press Enter to continue"
// gate for scripting.
func runStop(idArg string, yes bool) error {
	a, err := resolveActive(idArg)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("no active reminal session to stop")
		}
		return err
	}

	// Port forwards: tearing down the relay layer IS the whole job —
	// there's nothing for the user's underlying server to "keep running"
	// from our perspective. SIGTERM the tunnel process; it'll clean up
	// its own active record.
	if a.IsPort() {
		fmt.Printf("\n  This will stop the public proxy to localhost:%d.\n", a.Port)
		fmt.Printf("    · The URL %s stops working immediately.\n", a.OpenURL)
		fmt.Printf("    · Your localhost:%d server keeps running — nothing is killed.\n", a.Port)
		fmt.Printf("    · You can re-expose anytime with: reminal expose %d\n", a.Port)
		fmt.Println()
		if !yes && term.IsTerminal(int(os.Stdin.Fd())) {
			fmt.Print("  Press Enter to continue, Ctrl-C to cancel: ")
			_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		}
		if err := proc.Terminate(a.PID); err != nil && !errors.Is(err, proc.ErrGone) {
			return fmt.Errorf("signal PID %d: %w", a.PID, err)
		}
		// The tunnel's own SIGTERM handler clears the record on Unix; Windows
		// termination is abrupt (no handler runs), so clear it here too —
		// idempotent either way.
		_ = session.ClearActive(a.ID)
		fmt.Printf("  Stopped port-forward %s (port %d, PID %d).\n", a.ID, a.Port, a.PID)
		return nil
	}

	// Shell sessions: SIGUSR1 = pause broadcasting, keep the shell alive.
	fmt.Printf("\n  This will pause session %s.\n", a.ID)
	if a.Viewers > 0 {
		fmt.Printf("    · %d connected viewer(s) will be disconnected.\n", a.Viewers)
	} else {
		fmt.Println("    · No viewers are currently attached.")
	}
	if a.Headless {
		fmt.Println("    · The shell keeps running in the background — nothing is killed, no work is lost.")
		fmt.Printf("    · Resume with: `reminal kill %s` to fully end it, or leave it paused.\n", a.ID)
	} else {
		fmt.Println("    · The local shell keeps running — nothing is killed, no work is lost.")
		fmt.Println("    · The session ID and PIN stop accepting new viewers until you start a fresh `reminal`.")
	}
	fmt.Println()
	if !yes && term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Print("  Press Enter to continue, Ctrl-C to cancel: ")
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	}
	if err := pauseAgent(a.PID); err != nil {
		return fmt.Errorf("pause PID %d: %w", a.PID, err)
	}
	fmt.Printf("  Stopped session %s (PID %d).\n", a.ID, a.PID)
	return nil
}

// runAttach finds an agent running on this machine and connects to it as
// a viewer using its recorded session ID and PIN. With multiple sessions
// running, an explicit id is required.
func runAttach(idArg string) error {
	if idArg == "" {
		// No target: resolve unambiguously if we can (single session, or
		// we're inside one). Otherwise, with several shell sessions and a
		// TTY, offer the interactive picker instead of erroring.
		if a, err := resolveActive(""); err == nil {
			return attachTo(a)
		}
		all, lerr := session.ReadAllActive()
		if lerr != nil {
			return lerr
		}
		shells := matchSessions(all, func(a session.Active) bool { return !a.IsPort() })
		if len(shells) > 1 && term.IsTerminal(int(os.Stdin.Fd())) {
			picked, perr := pickSession(shells)
			if perr != nil {
				return perr
			}
			return attachTo(picked)
		}
		// Fall through to the original (helpful) error from resolveActive.
		_, err := resolveActive("")
		return err
	}
	a, err := resolveActive(idArg)
	if err != nil {
		return err
	}
	return attachTo(a)
}

// attachTo connects to a resolved session as a viewer, after guarding the two
// cases that can't work: attaching to a port-forward (no shell to drive) and
// attaching to the session whose shell we're currently sitting in (would loop
// the viewer's output back through the PTY).
func attachTo(a *session.Active) error {
	if a.IsPort() {
		return fmt.Errorf("session %s is a port forward (port %d), not a shell — nothing to attach to", a.ID, a.Port)
	}
	if os.Getenv("REMINAL_SESSION") == a.ID {
		return fmt.Errorf("already inside session %s — attaching would loop viewer output back through the PTY; open a different terminal first", a.ID)
	}
	return client.Connect(a.ID, a.PIN)
}

// runRename changes a running session's display name in place, via the agent's
// control socket. We can't just edit the active record from here — the agent
// owns it and rewrites it on viewer/activity changes, which would clobber a
// direct file edit. The agent persists the new name itself once told.
//
// idArg "" targets the current session (the one whose shell we're in, or the
// only one running) — the no-id form used from inside a session, especially on
// mobile. A non-empty idArg uses the usual id/name/prefix/substring resolution.
func runRename(idArg, newName string) error {
	newName = strings.TrimSpace(newName)
	// The control protocol is newline-delimited; keep the name on one line.
	newName = strings.NewReplacer("\n", " ", "\r", " ").Replace(newName)
	if newName == "" {
		return errors.New("new name cannot be empty")
	}
	a, err := resolveActive(idArg)
	if err != nil {
		if idArg == "" {
			// No id given and we couldn't infer one (run from outside any
			// session with several running). Point at the explicit form.
			return fmt.Errorf("%w — run from inside the session, or name it explicitly: reminal rename <id|name> %q", err, newName)
		}
		return err
	}
	if a.IsPort() {
		return fmt.Errorf("session %s is a port forward — rename is for shell sessions only", a.ID)
	}
	if _, err := sendControl(a.PID, "rename "+newName); err != nil {
		return fmt.Errorf("ask agent to rename: %w", err)
	}
	fmt.Printf("Renamed %s → %q\n", a.ID, newName)
	return nil
}

// runRestart asks one agent to hot-swap into the binary that's currently on
// disk, preserving its PTY (and thus the shell + running processes) plus
// session ID/PIN. Used after `reminal upgrade` so the upgrade actually takes
// effect on the running agent without needing physical access to kill +
// relaunch it. arg selects the session (empty → the current/active one).
func runRestart(arg string) error {
	a, err := resolveActive(arg)
	if err != nil {
		return err
	}
	if a.IsPort() {
		return fmt.Errorf("session %s is a port forward — restart is for shell agents only", a.ID)
	}
	if _, err := sendControl(a.PID, "restart"); err != nil {
		return fmt.Errorf("ask agent to restart: %w", err)
	}
	fmt.Printf("Asked reminal (PID %d, session %s) to hot-restart. Viewers will briefly disconnect.\n", a.PID, a.ID)
	return nil
}

// runRestartAll hot-restarts every shell session on the machine, so a single
// command rolls the whole box onto the freshly-upgraded binary. Port forwards
// are skipped (they have no PTY to preserve). Best-effort: a failure on one
// session is reported but doesn't stop the rest.
func runRestartAll() error {
	all, err := session.ReadAllActive()
	if err != nil {
		return err
	}
	daemonInstalled := client.DaemonServiceInstalled()
	if len(all) == 0 && !daemonInstalled {
		return errors.New("no active reminal sessions on this machine")
	}
	// Refresh the background host too — it runs independently of any terminal (so
	// it can't cut this command off) and, being a hidden service, would otherwise
	// be the one thing left on the old binary after an upgrade. No-op if absent.
	if daemonInstalled {
		if err := client.RestartDaemonService(); err != nil {
			fmt.Fprintf(os.Stderr, "  background host  failed: %v\n", err)
		} else {
			fmt.Println("  restarted background host")
		}
	}
	// The session we're being run from must be restarted LAST: hot-restarting it
	// re-execs the agent our own terminal is attached to, which cuts this command
	// off mid-loop — so any session after it in the list would silently miss its
	// restart. Defer it to the end (after the summary prints) so every other
	// session is done first.
	current := strings.ToUpper(strings.TrimSpace(os.Getenv("REMINAL_SESSION")))
	var self *session.Active
	var ok, skipped, failed int
	restart := func(a *session.Active) {
		label := a.ID
		if a.Name != "" {
			label = fmt.Sprintf("%s (%s)", a.Name, a.ID)
		}
		if _, err := sendControl(a.PID, "restart"); err != nil {
			fmt.Fprintf(os.Stderr, "  %-24s failed: %v\n", label, err)
			failed++
			return
		}
		fmt.Printf("  restarted %s\n", label)
		ok++
	}
	for i := range all {
		a := &all[i]
		if a.IsPort() {
			skipped++
			continue
		}
		if current != "" && a.ID == current {
			self = a
			continue
		}
		restart(a)
	}
	if self != nil {
		ok++ // count it now; the restart below may cut us off before we can print
		fmt.Printf("  restarted %s (this session, last)\n", self.ID)
	}
	// When only the background host was refreshed (no shell sessions), the line
	// above already said so — don't tack on a confusing "0 session(s)" summary.
	if ok > 0 || skipped > 0 {
		msg := fmt.Sprintf("Hot-restarted %d session(s)", ok)
		if skipped > 0 {
			msg += fmt.Sprintf(" (skipped %d port forward(s))", skipped)
		}
		fmt.Println(msg + ". Viewers briefly disconnect.")
	}
	if self != nil {
		_, _ = sendControl(self.PID, "restart")
	}
	if failed > 0 {
		return fmt.Errorf("%d session(s) could not be restarted", failed)
	}
	return nil
}

// runExpose spawns a detached port-forwarder for the given local port
// and prints the public URL + PIN + QR in the calling terminal. Refuses
// to double-spawn for a port that's already exposed.
func runExpose(port int, public bool) error {
	if existing, err := session.ReadActiveByPort(port); err == nil {
		fmt.Printf("Port %d is already exposed: %s\n", port, existing.OpenURL)
		fmt.Printf("To replace it: reminal stop %d  (then re-run reminal expose %d)\n", port, port)
		return nil
	}
	sp, err := client.SpawnTunnel(port, public)
	if err != nil {
		return err
	}
	client.PrintSpawnedTunnel(sp, port, public, version)
	return nil
}

// runNew spawns a fresh headless reminal in the background and prints
// its credentials in the calling terminal. Behaves exactly like opening
// a new terminal and typing `reminal` — except the shell runs detached,
// so killing this terminal doesn't kill the session.
func runNew(name string) error {
	if os.Getenv("REMINAL_NEW_NESTED") == "1" {
		return errors.New("refusing to spawn from inside another reminal new — protection against runaway recursion")
	}
	sp, err := client.Spawn(name, "")
	if err != nil {
		return err
	}
	client.PrintSpawned(sp, name, version)
	return nil
}

// runList prints the running reminal agents on this host, recent-first, one
// line each (name · id · mode · idle · viewers · cwd/title). An optional
// substring argument filters by id/name/cwd/title; --idle/--viewers/--headless
// narrow by state; --verbose adds the join URL + PIN under each row.
func runList(args []string) error {
	verbose := false
	onlyIdle, onlyViewers, onlyHeadless := false, false, false
	filter := ""
	for _, a := range args {
		switch {
		case a == "-v" || a == "--verbose":
			verbose = true
		case a == "--idle":
			onlyIdle = true
		case a == "--viewers":
			onlyViewers = true
		case a == "--headless":
			onlyHeadless = true
		case !strings.HasPrefix(a, "-") && filter == "":
			filter = a
		}
	}

	all, err := session.ReadAllActive()
	if err != nil {
		return err
	}
	now := time.Now()
	currentID := strings.ToUpper(strings.TrimSpace(os.Getenv("REMINAL_SESSION")))

	// Filter, then sort recent-first. We sort a local copy rather than
	// changing session.ReadAllActive's stable oldest-first order, which other
	// callers (ReadActive, port lookup) rely on.
	lower := strings.ToLower(filter)
	rows := make([]session.Active, 0, len(all))
	for _, a := range all {
		if onlyHeadless && !a.Headless {
			continue
		}
		if onlyViewers && a.Viewers == 0 {
			continue
		}
		if onlyIdle && (a.IsPort() || a.Viewers > 0) {
			continue
		}
		if lower != "" && !strings.Contains(strings.ToLower(a.ID), lower) &&
			!strings.Contains(strings.ToLower(a.Name), lower) &&
			!strings.Contains(strings.ToLower(a.Cwd), lower) &&
			!strings.Contains(strings.ToLower(a.Title), lower) {
			continue
		}
		rows = append(rows, a)
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].LastActive().After(rows[j].LastActive())
	})

	if len(rows) == 0 {
		if len(all) == 0 {
			fmt.Println("No reminal sessions running. Start one with:")
			fmt.Println("  reminal                # share this terminal")
			fmt.Println("  reminal new [name]     # share a background terminal")
			fmt.Println("  reminal expose <port>  # share a local HTTP port")
		} else {
			fmt.Printf("No sessions match (%d running — drop the filter or run `reminal list`).\n", len(all))
		}
		return nil
	}

	// Terminal width drives responsive truncation so rows never wrap. Fall back
	// to 80 when stdout isn't a terminal (piped) — there, nothing wraps anyway.
	width := 80
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 20 {
		width = w
	}

	// Pre-compute the name column width so rows line up. Unnamed sessions show a
	// dim "—" so the column never collapses. Cap it tighter on narrow terminals
	// so the id/state columns still fit.
	nameCap := 24
	if width < 90 {
		nameCap = 16
	}
	if width < 70 {
		nameCap = 12
	}
	nameW := 4
	for _, a := range rows {
		if n := len([]rune(a.Name)); n > nameW {
			nameW = n
		}
	}
	if nameW > nameCap {
		nameW = nameCap
	}

	fmt.Printf("%d session(s):\n\n", len(rows))
	for _, a := range rows {
		var mode string
		switch {
		case a.IsPort():
			mode = fmt.Sprintf("port:%d", a.Port)
		case a.Headless:
			mode = "headless"
		default:
			mode = "foreground"
		}

		var name string
		if a.Name == "" {
			name = cDim("—") + strings.Repeat(" ", max(0, nameW-1))
		} else {
			name = a.Name
			if len([]rune(name)) > nameW {
				name = string([]rune(name)[:nameW-1]) + "…"
			}
			name += strings.Repeat(" ", max(0, nameW-len([]rune(a.Name))))
		}

		// State column: who's watching / how idle. Ports have no viewer or
		// activity concept, so they just show their mode. Green highlights a
		// session that's live (watched or the one you're in).
		var state string
		stateColor := cDim
		switch {
		case a.IsPort():
			state = "up " + humanShort(now.Sub(a.StartedAt))
		case a.Viewers > 0:
			noun := "viewer"
			if a.Viewers != 1 {
				noun = "viewers"
			}
			state = fmt.Sprintf("%d %s", a.Viewers, noun)
			stateColor = cGreen
		case a.ID == currentID:
			// The shell we're typing in can't meaningfully be "idle" — its
			// last PTY output is just whenever we last hit Enter. Show it as
			// active so the [current] row never looks prunable.
			state = "active"
			stateColor = cGreen
		default:
			state = "idle " + humanShort(a.IdleFor(now))
		}

		// Identity tail: cwd (home-abbreviated) + live title hint, TRUNCATED to
		// whatever width is left so a long path never wraps onto the next line.
		tailPlain := abbrevHome(a.Cwd)
		if a.Title != "" {
			// Title is sniffed from the PTY (a program could set escapes in it).
			title := cleanTerm(a.Title)
			if tailPlain != "" {
				tailPlain += "  · " + title
			} else {
				tailPlain = "· " + title
			}
		}
		currentTag := ""
		if a.ID == currentID {
			currentTag = "  " + sgr("1;32", "[current]")
		}
		// Fixed prefix width up through the state column: 2 + name + 2 + id(8) +
		// 2 + mode(10) + 1 + state(12). Reserve the current tag and a margin.
		reserved := 2 + nameW + 2 + 8 + 2 + 10 + 1 + 12 + 2 + 1
		if a.ID == currentID {
			reserved += len("  [current]")
		}
		tail := ""
		if budget := width - reserved; budget >= 6 && tailPlain != "" {
			tail = "  " + cDim(truncate(tailPlain, budget))
		}

		fmt.Printf("  %s  %s  %s %s%s%s\n",
			name, cBold(a.ID), padCol(mode, 10, cDim), padCol(state, 12, stateColor), tail, currentTag)
		if verbose {
			fmt.Printf("  %s  %s\n",
				strings.Repeat(" ", nameW), cDim(a.OpenURL+"  ·  PIN "+a.PIN))
		}
	}
	fmt.Println()
	fmt.Println("  \x1b[2mAccepts id, name, unique prefix, or substring:\x1b[0m")
	fmt.Println("  reminal attach [id|name]       drive a session (no arg → interactive picker)")
	fmt.Println("  reminal kill   <id|name>       fully terminate a session (destroys the shell)")
	fmt.Println("  reminal prune                  kill idle, unwatched sessions in one go")
	return nil
}

// parseDuration is time.ParseDuration plus day ("d") and week ("w") units,
// which the stdlib doesn't support. Accepts a whole number with a trailing
// d/w (e.g. "1d", "2w") or anything time.ParseDuration handles ("12h", "90m",
// "1h30m"). Used by `reminal prune` so "reminal prune 1d" works.
func parseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty duration")
	}
	switch s[len(s)-1] {
	case 'd', 'w':
		n, err := strconv.Atoi(s[:len(s)-1])
		if err != nil || n < 0 {
			return 0, fmt.Errorf("invalid duration %q (try 30m, 12h, 1d, 2w)", s)
		}
		unit := 24 * time.Hour
		if s[len(s)-1] == 'w' {
			unit = 7 * 24 * time.Hour
		}
		return time.Duration(n) * unit, nil
	default:
		d, err := time.ParseDuration(s)
		if err != nil || d < 0 {
			return 0, fmt.Errorf("invalid duration %q (try 30m, 12h, 1d, 2w)", s)
		}
		return d, nil
	}
}

// runInfo handles `reminal info`. With no target it keeps the original
// env-aware behavior (the session you're in / the lone one). With a target it
// resolves by id/name/prefix/substring and prints that session's full banner.
// With --all it prints connect details for every shell session at once — the
// "iterate all my sessions" path — compact by default, full banner + QR per
// session under --qr. --json emits machine-readable output for any of these.
func runInfo(arg string, jsonOut, all, withQR bool) error {
	if all {
		allSessions, err := session.ReadAllActive()
		if err != nil {
			return err
		}
		shells := matchSessions(allSessions, func(a session.Active) bool { return !a.IsPort() })
		// recent-first, same ordering as `reminal list`
		sort.Slice(shells, func(i, j int) bool { return shells[i].LastActive().After(shells[j].LastActive()) })
		if len(shells) == 0 {
			fmt.Println("No reminal shell sessions running.")
			return nil
		}
		if jsonOut {
			records := make([]any, len(shells))
			for i, a := range shells {
				records[i] = client.InfoJSON(a)
			}
			return json.NewEncoder(os.Stdout).Encode(records)
		}
		fmt.Printf("%d session(s):\n", len(shells))
		for _, a := range shells {
			if withQR {
				client.ShowInfoFor(a) // full details + QR
			} else {
				client.ShowInfoDetails(a) // full details, no QR
			}
		}
		if !withQR {
			fmt.Println("  Add --qr to include a scannable code under each, or `reminal qr <id|name>` for one.")
		}
		return nil
	}

	// Single session. No arg → original env-aware path (works on the host and
	// over `reminal connect` from the same machine).
	if arg == "" {
		if jsonOut {
			return client.ShowActiveInfoJSON()
		}
		return client.ShowActiveInfo()
	}
	a, err := resolveActive(arg)
	if err != nil {
		return err
	}
	if jsonOut {
		return json.NewEncoder(os.Stdout).Encode(client.InfoJSON(a))
	}
	client.ShowInfoFor(a)
	return nil
}

// runQR prints just the join QR. No target → the current/lone session (original
// behavior); a target resolves by id/name/prefix/substring.
func runQR(arg string) error {
	if arg == "" {
		return client.ShowActiveQR()
	}
	a, err := resolveActive(arg)
	if err != nil {
		return err
	}
	client.ShowQRFor(a)
	return nil
}

// humanShort renders a duration as a compact 1-unit string (45s, 3m, 2h, 5d).
func humanShort(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// abbrevHome collapses the user's home directory to ~ for compact display.
func abbrevHome(p string) string {
	if p == "" {
		return ""
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if p == home {
			return "~"
		}
		if strings.HasPrefix(p, home+string(os.PathSeparator)) {
			return "~" + p[len(home):]
		}
	}
	return p
}

// runKill terminates the named agent. Destructive — asks for explicit
// y/N confirmation unless `-y` was passed.
func runKill(idArg string, yes bool) error {
	a, err := resolveActive(idArg)
	if err != nil {
		return err
	}
	fmt.Printf("\n  This will fully terminate session %s on this machine.\n", a.ID)
	fmt.Println("    · The shell process inside will be killed — anything running in")
	fmt.Println("      that shell stops immediately (unsaved work is lost).")
	if a.Viewers > 0 {
		noun := "viewer"
		if a.Viewers != 1 {
			noun = "viewers"
		}
		fmt.Printf("    · %d connected %s will be disconnected.\n", a.Viewers, noun)
	} else {
		fmt.Println("    · No viewers are currently attached.")
	}
	fmt.Println("    · The session ID and PIN stop being valid right away.")
	fmt.Println("    · This is irreversible.")
	fmt.Println()
	if !yes {
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return errors.New("refusing to kill without confirmation — re-run with -y to skip the prompt")
		}
		fmt.Print("  Proceed? [y/N]: ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		resp := strings.ToLower(strings.TrimSpace(line))
		if resp != "y" && resp != "yes" {
			fmt.Println("  Aborted.")
			return nil
		}
	}
	if err := terminateAgent(*a); err != nil {
		return err
	}
	fmt.Printf("  Killed session %s (PID %d).\n", a.ID, a.PID)
	return nil
}

// terminateAgent SIGTERMs the agent, gives it a brief window to run its defers
// (close WS gracefully, restore the host terminal), then escalates to SIGKILL
// if it's still alive — a hung agent shouldn't be able to refuse a kill.
//
// It then removes the session's active record itself rather than trusting the
// agent's own SIGTERM handler to do it. A dead, zombie, or unresponsive agent
// never runs that handler, which is how stale entries used to survive `reminal
// kill` / `reminal prune` and linger in `reminal list` forever. The record
// removal is idempotent and runs even when the process was already gone (ESRCH,
// which we treat as success). Shared by `reminal kill` and `reminal prune`.
func terminateAgent(a session.Active) error {
	// Always drop the record, however termination goes — the agent may already
	// be dead and unable to clean up after itself.
	defer func() { _ = session.ClearActive(a.ID) }()

	pid := a.PID
	// Ask first. A foreground agent owns the terminal it was started in — raw
	// mode, and whatever modes the session turned on — and only the agent
	// itself can hand that back. On Unix SIGTERM gives it the chance; on
	// Windows termination is abrupt (no signal reaches a Go process, no defer
	// runs), so a killed session used to leave that terminal mangling every
	// keystroke. The control socket is the graceful route on both.
	if _, err := sendControl(pid, "quit", 2*time.Second); err == nil {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if !proc.Alive(pid) {
				return nil // exited on its own terms, terminal restored
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	if err := proc.Terminate(pid); err != nil {
		if errors.Is(err, proc.ErrGone) {
			return nil // process already gone; record cleared by the defer
		}
		return fmt.Errorf("terminate PID %d: %w", pid, err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !proc.Alive(pid) {
			break // process gone
		}
		time.Sleep(100 * time.Millisecond)
	}
	if proc.Alive(pid) {
		_ = proc.Kill(pid)
	}
	return nil
}

// runPrune terminates idle, unwatched shell sessions in one go — the cleanup
// for a pile of abandoned `reminal new` instances. A session is a prune
// candidate when it's a shell (not a port forward), has zero attached viewers,
// isn't the shell we're currently sitting in, and has had no PTY activity for
// at least idle. Destructive — lists candidates and asks for confirmation
// unless -y was passed.
func runPrune(idle time.Duration, yes bool) error {
	all, err := session.ReadAllActive()
	if err != nil {
		return err
	}
	now := time.Now()
	currentID := strings.ToUpper(strings.TrimSpace(os.Getenv("REMINAL_SESSION")))
	var victims []session.Active
	for _, a := range all {
		if a.IsPort() || a.Viewers > 0 || a.ID == currentID {
			continue
		}
		if a.IdleFor(now) < idle {
			continue
		}
		victims = append(victims, a)
	}
	if len(victims) == 0 {
		fmt.Printf("Nothing to prune — no shell sessions idle ≥ %s with zero viewers.\n", humanShort(idle))
		return nil
	}
	fmt.Printf("\n  Will terminate %d idle session(s) (idle ≥ %s, no viewers):\n\n", len(victims), humanShort(idle))
	for _, a := range victims {
		label := a.ID
		if a.Name != "" {
			label = fmt.Sprintf("%s (%s)", a.ID, a.Name)
		}
		fmt.Printf("    · %-22s idle %-5s %s\n", label, humanShort(a.IdleFor(now)), abbrevHome(a.Cwd))
	}
	fmt.Println("\n    The shell inside each is killed — unsaved work is lost. Irreversible.")
	fmt.Println()
	if !yes {
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return errors.New("refusing to prune without confirmation — re-run with -y to skip the prompt")
		}
		fmt.Print("  Proceed? [y/N]: ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		resp := strings.ToLower(strings.TrimSpace(line))
		if resp != "y" && resp != "yes" {
			fmt.Println("  Aborted.")
			return nil
		}
	}
	killed := 0
	for _, a := range victims {
		if err := terminateAgent(a); err != nil {
			fmt.Printf("  ! %s: %v\n", a.ID, err)
			continue
		}
		fmt.Printf("  Killed %s (PID %d).\n", a.ID, a.PID)
		killed++
	}
	fmt.Printf("\n  Pruned %d of %d.\n", killed, len(victims))
	return nil
}

// runConnect is the shared body of both `reminal connect <target> [pin]`
// and `reminal --connect <target> --pin <pin>`. pinArg may be empty, in which
// case we fall back to a PIN embedded in the target URL, and finally to an
// interactive prompt. On wrong-PIN errors we re-prompt up to 3 times,
// matching ssh's password-retry convention; the relay locks out after 5
// total wrong attempts anyway, so the user can't burn through their budget.
func runConnect(target, pinArg string, owner bool) error {
	sessionID, urlPin := parseConnectTarget(target)
	if sessionID == "" {
		return errors.New("needs a session ID or a relay URL containing ?s=<ID>")
	}
	if os.Getenv("REMINAL_SESSION") == sessionID {
		return fmt.Errorf("already inside session %s — connecting from this shell would loop viewer output back through the PTY; open a different terminal first", sessionID)
	}
	// Refuse to connect to ANY remote reminal from inside a shell that
	// is itself being broadcast by a local reminal agent. Otherwise the
	// remote session's content streams through the local PTY and gets
	// re-broadcast to every viewer of the local session — both a
	// privacy leak and visually chaotic. The user needs to either stop
	// the local broadcast first or use a different terminal that
	// isn't behind reminal.
	if inside := os.Getenv("REMINAL_SESSION"); inside != "" && inside != sessionID {
		if active, err := session.ReadActiveByID(inside); err == nil && active != nil {
			return fmt.Errorf(
				"this shell is being broadcast by your local reminal (session %s) — connecting to %s would mirror it to your viewers.\n  Run `reminal stop` first to stop broadcasting, or open a separate terminal (not behind reminal) for `reminal connect`",
				inside, sessionID,
			)
		}
	}
	// Precedence: explicit pin arg > PIN extracted from URL > interactive prompt.
	resolvedPin := pinArg
	if resolvedPin == "" {
		resolvedPin = urlPin
	}
	// Try a PIN-free (owner) connect when EITHER --owner is forced, OR no PIN was
	// supplied and this device has an owner identity. On success we're in with no
	// PIN; if the machine doesn't recognise us (connected=false) and it wasn't
	// forced, fall through to the PIN prompt. Auto-detect stays gated on "no PIN"
	// so supplying a PIN skips the owner probe entirely — but an explicit --owner
	// forces the owner path even alongside a stray PIN.
	if owner || (resolvedPin == "" && client.HasDeviceKey()) {
		connected, err := client.ConnectOwner(sessionID)
		if err == nil {
			return nil
		}
		if connected || owner {
			return err // it did connect (then ended), or --owner was forced
		}
		// Only a "device not recognised" failure quietly falls back to a PIN. A
		// hard refusal (bad machine signature / changed machine identity) is a
		// security event — surface it instead of silently asking for a PIN.
		if !errors.Is(err, client.ErrNotOwner) {
			return err
		}
	}
	const maxAttempts = 3
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if resolvedPin == "" {
			p, err := readPIN(sessionID)
			if err != nil {
				return err
			}
			resolvedPin = p
		}
		err := client.Connect(sessionID, resolvedPin)
		if err == nil {
			return nil
		}
		// Only "incorrect PIN" is recoverable in-process; everything else
		// (locked out, session gone, network) propagates immediately.
		if attempt < maxAttempts && strings.Contains(err.Error(), "incorrect PIN") {
			fmt.Fprintf(os.Stderr, "%v — try again (%d/%d).\n", err, attempt, maxAttempts)
			resolvedPin = "" // force re-prompt on next iteration
			continue
		}
		return err
	}
	return errors.New("too many failed PIN attempts")
}

// parseConnectTarget accepts a bare session ID, a relay URL like
// https://relay/?s=ABC12345, or a relay URL with the PIN in the fragment
// (#p=NNNNNN) or query (?p=NNNNNN). Returns the session ID uppercased and the
// PIN if found.
func parseConnectTarget(target string) (sessionID, pin string) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", ""
	}
	// A bare session ID has no scheme and no URL punctuation.
	if !strings.Contains(target, "://") && !strings.ContainsAny(target, "/?#") {
		return strings.ToUpper(target), ""
	}
	u, err := url.Parse(target)
	if err != nil {
		return "", ""
	}
	sessionID = strings.ToUpper(u.Query().Get("s"))
	if u.Fragment != "" {
		if frag, err := url.ParseQuery(u.Fragment); err == nil {
			pin = frag.Get("p")
		}
	}
	if pin == "" {
		pin = u.Query().Get("p")
	}
	return sessionID, pin
}

// readPIN prompts on stderr and reads the PIN from stdin with echo disabled.
// Errors if stdin isn't a TTY since there's no one to prompt. sessionID is
// echoed in the prompt so the user can confirm they're authenticating to the
// session they meant — a typo'd session ID with the right PIN burns lockout
// budget against the wrong relay room.
func readPIN(sessionID string) (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", errors.New("PIN required — pass --pin or run interactively")
	}
	if sessionID != "" {
		fmt.Fprintf(os.Stderr, "PIN for %s: ", sessionID)
	} else {
		fmt.Fprint(os.Stderr, "PIN: ")
	}
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read PIN: %w", err)
	}
	pin := strings.TrimSpace(string(b))
	if pin == "" {
		return "", errors.New("PIN required")
	}
	return pin, nil
}
