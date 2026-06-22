package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/reminal/reminal/internal/client"
	"github.com/reminal/reminal/internal/keepawake"
	"github.com/reminal/reminal/internal/session"
	"github.com/reminal/reminal/internal/updater"
	"golang.org/x/term"
)

// version, buildDate, and commit are stamped at build time via
//   -ldflags "-X main.version=… -X main.buildDate=… -X main.commit=…"
// in scripts/build.sh and the release workflow. Dev builds keep their
// placeholder values so the updater skips the upgrade prompt and version
// --verbose still shows something readable.
var (
	version   = "dev"
	buildDate = "unknown"
	commit    = "unknown"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
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
			if err := updater.Upgrade(version); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
			return
		case "info":
			jsonOut := false
			for _, a := range os.Args[2:] {
				if a == "--json" || a == "-j" {
					jsonOut = true
				}
			}
			var err error
			if jsonOut {
				err = client.ShowActiveInfoJSON()
			} else {
				err = client.ShowActiveInfo()
			}
			if err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}
			return
		case "doctor":
			if err := client.Doctor(version); err != nil {
				os.Exit(1)
			}
			return
		case "qr":
			if err := client.ShowActiveQR(); err != nil {
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
				fmt.Fprintln(os.Stderr, "usage: reminal completion <bash|zsh|fish>")
				os.Exit(1)
			}
			if err := client.Completion(shell); err != nil {
				os.Exit(1)
			}
			return
		case "connect":
			if len(os.Args) < 3 {
				fmt.Fprintln(os.Stderr, "usage: reminal connect <session-id-or-url> [pin]")
				os.Exit(1)
			}
			target := os.Args[2]
			pinArg := ""
			if len(os.Args) > 3 {
				pinArg = os.Args[3]
			}
			if err := runConnect(target, pinArg); err != nil {
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
			if err := runNew(); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
			return
		case "list", "ls":
			if err := runList(); err != nil {
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
	verbose := flag.Bool("v", false, "verbose mode — append raw error detail to status lines (same as REMINAL_DEBUG=1)")
	verboseLong := flag.Bool("verbose", false, "alias for -v")
	headless := flag.Bool("headless", false, "run without owning the host terminal — for spawned background sessions; users normally invoke this via `reminal new`")
	handshakeFD := flag.Int("handshake-fd", 0, "fd inherited from `reminal new` for the credentials handshake (internal)")
	flag.Parse()

	if *verbose || *verboseLong {
		os.Setenv("REMINAL_DEBUG", "1")
	}

	// Headless agent path: skip the upgrade prompt + the
	// "already-running, attach?" prompt — neither makes sense for a
	// detached background child. Run the agent directly.
	if *headless {
		opts := client.AgentOptions{Headless: true, HandshakeFD: *handshakeFD}
		agent, err := client.NewAgentWith(version, opts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
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
		if err := runConnect(*connect, *pin); err != nil {
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

	agent, err := client.NewAgent(version)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	stopKeepAwake := keepawake.Start()
	defer stopKeepAwake()
	if err := agent.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func printHelp() {
	fmt.Print(`reminal — remote terminal access from any browser or terminal

Usage:
  reminal                                  Share this terminal (works out of the box)
  reminal new                              Spawn a fresh background session (detached — survives this terminal closing)
  reminal list                             List every reminal session running on this machine
  reminal connect <session-or-url> [pin]   Connect to a remote session (PIN prompted if omitted)
  reminal attach [id]                      Re-connect to a local agent as a viewer (no copy-paste). id required if multiple
  reminal stop [id] [-y]                   Stop broadcasting (kicks viewers, keeps shell alive). Explains consequences first
  reminal kill [id] [-y]                   Fully terminate a session (irreversible — kills shell + disconnects viewers)
  reminal send <file>                      Push a file to every connected viewer (web client auto-downloads)
  reminal notify <message>                 Push a notification to viewers (browser notification on web)
  reminal connections                      List currently attached viewers with connect time
  reminal info [--json]                    Reprint session ID / PIN / URL / QR for the running agent (or JSON)
  reminal qr                               Print just the join QR for the running agent (for a second screen)
  reminal doctor                           Self-diagnostic: version, relay reachability, terminal, shell
  reminal completion <bash|zsh|fish>       Print shell completion script (source it in your shell rc)
  reminal upgrade                          Upgrade to the latest release
  reminal relay [port]                     Start local relay server (dev only)
  reminal version [--verbose]              Print version (--verbose adds build date / commit / go version)
  reminal help                             Show this help

  reminal --connect <session-or-url>       Long-form alias of "reminal connect ..."

Security:
  Each session requires a random 8-char ID + 6-digit PIN.
  Terminal traffic is end-to-end encrypted — the relay cannot read it.

Inside reminal --connect:
  Local Ctrl+C goes to the remote shell. To disconnect the viewer cleanly,
  press Ctrl-] (the agent on the host keeps running for new viewers).

Environment:
  REMINAL_RELAY          Override relay URL (default: hosted Cloudflare relay)
  REMINAL_WEB            Override web UI URL
  REMINAL_LOCAL          Set to 1 to use localhost relay (with reminal relay)
  REMINAL_NO_KEEP_AWAKE  Set to 1 to let the host sleep while reminal runs
  REMINAL_DEBUG          Set to 1 to append raw error detail to status lines
  SHELL                  Shell to run (default: $SHELL, falls back to zsh / bash / sh)

Examples:
  reminal
  reminal connect ABC12345 482916
  reminal connect ABC12345                                          # PIN prompted
  reminal connect "https://reminal-relay.reminal.workers.dev/?s=ABC12345#p=482916"
  reminal doctor                                                    # confirm relay reachability etc.

Bug reports + feature requests: https://github.com/harshalgajjar/Reminal/issues
`)
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
	a, err := session.ReadActive()
	if err != nil {
		if os.IsNotExist(err) {
			return errors.New("no active reminal session on this machine")
		}
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
//   $ make build && reminal notify "build done"
func runNotify(message string) error {
	if strings.TrimSpace(message) == "" {
		return errors.New("message required")
	}
	a, err := session.ReadActive()
	if err != nil {
		if os.IsNotExist(err) {
			return errors.New("no active reminal session on this machine")
		}
		return err
	}
	_, err = sendControl(a.PID, "notify "+message)
	return err
}

// sendControl is the shared dial-and-send helper for the agent's Unix
// control socket. Used by every `reminal <verb>` that needs the agent to
// take action. Returns the payload after "ok " on success, "" if the
// reply is just "ok\n", or an error if the reply starts with "error:".
func sendControl(pid int, cmd string) (string, error) {
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

// runSend connects to the local agent's control socket and asks it to
// broadcast the given file to every connected viewer as a TypeDownload
// message. The file is read by the AGENT (not this process), so the path
// must be valid from the agent's working directory perspective — which is
// guaranteed when invoked from inside the shared shell.
func runSend(path string) error {
	a, err := session.ReadActive()
	if err != nil {
		if os.IsNotExist(err) {
			return errors.New("no active reminal session on this machine")
		}
		return err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if _, err := os.Stat(abs); err != nil {
		return err
	}
	if _, err := sendControl(a.PID, "send "+abs); err != nil {
		return err
	}
	fmt.Printf("Sent %s to viewers.\n", filepath.Base(abs))
	return nil
}

// resolveActive picks a target session from the supplied id arg. When
// no arg is given the resolution order is:
//  1. REMINAL_SESSION env var (set by the agent inside the shared shell
//     — "this terminal" is the most natural default for any command
//     typed at the host's own prompt).
//  2. The single running session if there's exactly one.
//  3. Otherwise: print the list and require the caller to disambiguate
//     so `reminal stop`/`kill`/`attach` never silently target the
//     wrong agent.
func resolveActive(idArg string) (*session.Active, error) {
	idArg = strings.ToUpper(strings.TrimSpace(idArg))
	all, err := session.ReadAllActive()
	if err != nil {
		return nil, err
	}
	if len(all) == 0 {
		return nil, errors.New("no active reminal session on this machine — start one with `reminal` or `reminal new`")
	}
	if idArg != "" {
		for i := range all {
			if all[i].ID == idArg {
				return &all[i], nil
			}
		}
		return nil, fmt.Errorf("no active session with id %q (running: %s)", idArg, joinIDs(all))
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

func joinIDs(all []session.Active) string {
	ids := make([]string, len(all))
	for i, a := range all {
		ids[i] = a.ID
	}
	return strings.Join(ids, ", ")
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
	if err := syscall.Kill(a.PID, syscall.SIGUSR1); err != nil {
		return fmt.Errorf("signal PID %d: %w", a.PID, err)
	}
	fmt.Printf("  Stopped session %s (PID %d).\n", a.ID, a.PID)
	return nil
}

// runAttach finds an agent running on this machine and connects to it as
// a viewer using its recorded session ID and PIN. With multiple sessions
// running, an explicit id is required.
func runAttach(idArg string) error {
	a, err := resolveActive(idArg)
	if err != nil {
		return err
	}
	if os.Getenv("REMINAL_SESSION") == a.ID {
		return fmt.Errorf("already inside session %s — attaching would loop viewer output back through the PTY; open a different terminal first", a.ID)
	}
	return client.Connect(a.ID, a.PIN)
}

// runNew spawns a fresh headless reminal in the background and prints
// its credentials in the calling terminal. Behaves exactly like opening
// a new terminal and typing `reminal` — except the shell runs detached,
// so killing this terminal doesn't kill the session.
func runNew() error {
	if os.Getenv("REMINAL_NEW_NESTED") == "1" {
		return errors.New("refusing to spawn from inside another reminal new — protection against runaway recursion")
	}
	sp, err := client.Spawn()
	if err != nil {
		return err
	}
	client.PrintSpawned(sp, version)
	return nil
}

// runList prints one line per running reminal agent on this host.
func runList() error {
	all, err := session.ReadAllActive()
	if err != nil {
		return err
	}
	if len(all) == 0 {
		fmt.Println("No reminal sessions running. Start one with `reminal` or `reminal new`.")
		return nil
	}
	fmt.Printf("%d session(s) running:\n\n", len(all))
	now := time.Now()
	for _, a := range all {
		mode := "foreground"
		if a.Headless {
			mode = "headless"
		}
		age := now.Sub(a.StartedAt).Round(time.Second)
		viewers := ""
		if a.Viewers > 0 {
			noun := "viewer"
			if a.Viewers != 1 {
				noun = "viewers"
			}
			viewers = fmt.Sprintf(", %d %s", a.Viewers, noun)
		}
		fmt.Printf("  %s  · %-10s · PID %-6d · up %v%s\n", a.ID, mode, a.PID, age, viewers)
		fmt.Printf("           %s\n", a.OpenURL)
	}
	fmt.Println()
	fmt.Println("  reminal attach <id>    drive a session from this terminal")
	fmt.Println("  reminal stop <id>      pause broadcasting (keeps shell alive)")
	fmt.Println("  reminal kill <id>      fully terminate the session")
	return nil
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
	if err := syscall.Kill(a.PID, syscall.SIGTERM); err != nil {
		return fmt.Errorf("SIGTERM PID %d: %w", a.PID, err)
	}
	// Give the agent a brief window to run its defers (clear active
	// record, close WS gracefully, restore host terminal if foreground).
	// Then escalate to SIGKILL if it's still alive — a hung agent
	// shouldn't be able to refuse a kill.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(a.PID, 0) != nil {
			break // process gone
		}
		time.Sleep(100 * time.Millisecond)
	}
	if syscall.Kill(a.PID, 0) == nil {
		_ = syscall.Kill(a.PID, syscall.SIGKILL)
	}
	fmt.Printf("  Killed session %s (PID %d).\n", a.ID, a.PID)
	return nil
}

// runConnect is the shared body of both `reminal connect <target> [pin]`
// and `reminal --connect <target> --pin <pin>`. pinArg may be empty, in which
// case we fall back to a PIN embedded in the target URL, and finally to an
// interactive prompt. On wrong-PIN errors we re-prompt up to 3 times,
// matching ssh's password-retry convention; the relay locks out after 5
// total wrong attempts anyway, so the user can't burn through their budget.
func runConnect(target, pinArg string) error {
	sessionID, urlPin := parseConnectTarget(target)
	if sessionID == "" {
		return errors.New("needs a session ID or a relay URL containing ?s=<ID>")
	}
	if os.Getenv("REMINAL_SESSION") == sessionID {
		return fmt.Errorf("already inside session %s — connecting from this shell would loop viewer output back through the PTY; open a different terminal first", sessionID)
	}
	// Precedence: explicit pin arg > PIN extracted from URL > interactive prompt.
	resolvedPin := pinArg
	if resolvedPin == "" {
		resolvedPin = urlPin
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
