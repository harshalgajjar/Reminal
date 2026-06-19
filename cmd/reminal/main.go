package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/reminal/reminal/internal/client"
)

var version = "0.1.3"

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
			fmt.Println(version)
			return
		case "help", "-h", "--help":
			printHelp()
			return
		}
	}

	connect := flag.String("connect", "", "connect to a remote session by ID")
	pin := flag.String("pin", "", "PIN for the remote session (required with --connect)")
	flag.Parse()

	if *connect != "" {
		if *pin == "" {
			fmt.Fprintln(os.Stderr, "error: --pin is required when using --connect")
			os.Exit(1)
		}
		if err := client.Connect(*connect, *pin); err != nil {
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
	if err := agent.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func printHelp() {
	fmt.Print(`reminal — remote terminal access from any browser or terminal

Usage:
  reminal                                  Share this terminal (works out of the box)
  reminal --connect <session> --pin <pin>  Connect to a remote session
  reminal relay [port]                     Start local relay server (dev only)
  reminal version                          Print version
  reminal help                             Show this help

Security:
  Each session requires a random 8-char ID + 6-digit PIN.
  Terminal traffic is end-to-end encrypted — the relay cannot read it.

Environment:
  REMINAL_RELAY   Override relay URL (default: hosted Cloudflare relay)
  REMINAL_WEB     Override web UI URL
  REMINAL_LOCAL   Set to 1 to use localhost relay (with reminal relay)
  SHELL           Shell to run (default: /bin/zsh or $SHELL)

Examples:
  reminal
  reminal --connect ABC12345 --pin 482916
`)
}
