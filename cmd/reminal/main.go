package main

import (
	"bufio"
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
			if err := runAttach(); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
			return
		case "stop":
			if err := runStop(); err != nil {
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
	flag.Parse()

	if *verbose || *verboseLong {
		os.Setenv("REMINAL_DEBUG", "1")
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

	// If an agent is already running on this machine, prefer attaching to
	// it over silently spawning a second one (which would orphan the first
	// — different ID/PIN, scrollback split, viewer confusion). Skipped if
	// REMINAL_NEW=1 is set or if stdin isn't a TTY (no one to prompt).
	if existing, err := session.ReadActive(); err == nil && os.Getenv("REMINAL_NEW") != "1" {
		// If we're already INSIDE this session's shared shell (REMINAL_SESSION
		// is the env var the agent injects when it spawns its PTY), attaching
		// would create an output feedback loop: viewer stdout goes back into
		// the PTY, pumpPTY broadcasts it, viewer renders it, broadcast again,
		// ad infinitum. Refuse + point at the obvious fix.
		if os.Getenv("REMINAL_SESSION") == existing.ID {
			fmt.Fprintf(os.Stderr,
				"You're already inside reminal session %s (this shell IS the shared shell).\n",
				existing.ID)
			fmt.Fprintln(os.Stderr, "  To stop sharing:        reminal stop")
			fmt.Fprintln(os.Stderr, "  To see join info / QR:  reminal info")
			fmt.Fprintln(os.Stderr, "  To attach from another shell, open a new terminal first.")
			os.Exit(2)
		}
		if term.IsTerminal(int(os.Stdin.Fd())) {
			age := time.Since(existing.StartedAt).Round(time.Second)
			viewers := ""
			if existing.Viewers > 0 {
				viewers = fmt.Sprintf(", %d viewer(s) attached", existing.Viewers)
			}
			fmt.Fprintf(os.Stderr,
				"A reminal session is already running here: %s (started %v ago, PID %d%s)\n",
				existing.ID, age, existing.PID, viewers)
			fmt.Fprint(os.Stderr, "Attach to it instead of starting a new session? (Y/n) ")
			reader := bufio.NewReader(os.Stdin)
			line, _ := reader.ReadString('\n')
			resp := strings.ToLower(strings.TrimSpace(line))
			if resp == "" || resp == "y" || resp == "yes" {
				if err := runAttach(); err != nil {
					fmt.Fprintf(os.Stderr, "error: %v\n", err)
					os.Exit(1)
				}
				return
			}
			fmt.Fprintln(os.Stderr, "Starting a new session — the previous one (above) is now orphaned.")
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
  reminal connect <session-or-url> [pin]   Connect to a remote session (PIN prompted if omitted)
  reminal attach                           Re-connect to the agent running on this machine (no copy-paste)
  reminal stop                             Stop broadcasting (kicks viewers, keeps your local shell running)
  reminal send <file>                      Push a file to every connected viewer (web client auto-downloads)
  reminal notify <message>                 Push a notification to viewers (browser notification on web)
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
  REMINAL_NEW            Set to 1 to skip the "attach to existing?" prompt and always start a new session
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
	return sendControl(a.PID, "notify "+message)
}

// sendControl is shared dial-and-send helper for the agent's Unix control
// socket. Used by `reminal send` and `reminal notify`.
func sendControl(pid int, cmd string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	sock := filepath.Join(home, ".reminal", fmt.Sprintf("agent-%d.sock", pid))
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return fmt.Errorf("connect to agent: %w", err)
	}
	defer conn.Close()
	if _, err := fmt.Fprintln(conn, cmd); err != nil {
		return err
	}
	reply, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return err
	}
	reply = strings.TrimSpace(reply)
	if reply != "ok" {
		return fmt.Errorf("agent: %s", reply)
	}
	return nil
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
	if err := sendControl(a.PID, "send "+abs); err != nil {
		return err
	}
	fmt.Printf("Sent %s to viewers.\n", filepath.Base(abs))
	return nil
}

// runStop tells the agent running on this machine to stop broadcasting to
// the relay (closes the WS, clears active.json, kicks any viewers) while
// keeping its PTY pumps alive — the host's terminal continues working as a
// plain local shell. Useful when you've returned to your laptop and don't
// need the remote-share open anymore but want to keep the shell session.
func runStop() error {
	a, err := session.ReadActive()
	if err != nil {
		if os.IsNotExist(err) {
			return errors.New("no active reminal session to stop")
		}
		return err
	}
	if err := syscall.Kill(a.PID, syscall.SIGUSR1); err != nil {
		return fmt.Errorf("signal PID %d: %w", a.PID, err)
	}
	fmt.Printf("Sent stop to reminal (PID %d) — session %s. The local shell continues.\n",
		a.PID, a.ID)
	return nil
}

// runAttach finds the agent running on this machine (via ~/.reminal/
// active.json) and connects to it as a viewer using its recorded session ID
// and PIN. The user pays zero copy-paste cost to drive the existing PTY from
// a different shell — useful when reminal is sitting in a tmux pane / nohup
// / a window the user can't get back to.
func runAttach() error {
	a, err := session.ReadActive()
	if err != nil {
		if os.IsNotExist(err) {
			return errors.New("no active reminal session on this machine — start one with `reminal`")
		}
		return err
	}
	if os.Getenv("REMINAL_SESSION") == a.ID {
		return fmt.Errorf("already inside session %s — attaching would loop viewer output back through the PTY; open a different terminal first", a.ID)
	}
	return client.Connect(a.ID, a.PIN)
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
