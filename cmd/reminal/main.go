package main

import (
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"runtime"
	"strings"

	"github.com/reminal/reminal/internal/client"
	"github.com/reminal/reminal/internal/keepawake"
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
		case "help", "-h", "--help":
			printHelp()
			return
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

	agent, err := client.NewAgent()
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
