// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mdp/qrterminal/v3"
)

// spawnHandshakeTimeout bounds how long `reminal new` will wait for the
// background child to write its credentials back. Connecting to the
// relay typically takes <1s on broadband; we give it generous headroom
// for slow / hostile networks but bail before the user wonders what's
// stuck.
const spawnHandshakeTimeout = 15 * time.Second

// SpawnedSession is the JSON the headless agent writes back to its
// parent over the inherited handshake pipe. Mirrors session.Active for
// the fields the caller actually needs.
type SpawnedSession struct {
	ID      string `json:"id"`
	PIN     string `json:"pin"`
	OpenURL string `json:"open_url"`
	PID     int    `json:"pid"`
}

// Spawn launches a detached headless reminal child via the running
// binary and blocks until the child writes its credentials back over
// the inherited fd 3, or until spawnHandshakeTimeout fires.
//
// cwd, when set, is the directory the new shell starts in (absolute,
// must exist). Empty keeps the previous default: inherit this process's
// directory, or the user's home when we ourselves are at "/" (the
// launchd/systemd host).
//
// The child is fully decoupled from this process — it gets its own
// session leader (Setsid) and stdin/stdout/stderr are wired to
// /dev/null, so the parent can exit immediately after printing the
// credentials and the agent keeps running in the background.
func Spawn(name, cwd string) (*SpawnedSession, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate self: %w", err)
	}

	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", os.DevNull, err)
	}
	defer devnull.Close()

	cmd := exec.Command(exe, "--headless")
	// Pass through enough env that the child finds the same relay /
	// shell / debug knobs the user expected. The full environment is
	// inherited by default in exec.Cmd; we only need to scrub
	// REMINAL_SESSION (which marks "you're INSIDE a shared shell" and
	// would trip the headless agent's self-attach checks) — but in
	// practice the child doesn't run any of those checks. Leave env
	// untouched for now.
	//
	// The user-chosen name rides along in REMINAL_NEW_NAME — the detached
	// child has no argv we control after exec, so env is the clean channel
	// for it. The headless agent reads it into AgentOptions.Name.
	if name = strings.TrimSpace(name); name != "" {
		cmd.Env = append(os.Environ(), "REMINAL_NEW_NAME="+name)
	}
	cmd.Stdin = devnull
	cmd.Stdout = devnull
	cmd.Stderr = devnull

	dir, err := resolveSpawnDir(cwd)
	if err != nil {
		return nil, err
	}
	if dir != "" {
		cmd.Dir = dir
	}

	// The handshake channel (fd-3 pipe on Unix, loopback socket on Windows) +
	// the platform's detach attributes. The child writes one JSON line once
	// startup is complete; we read it back here and surface it to the user.
	recv, afterStart, err := prepareHandshake(cmd)
	if err != nil {
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		afterStart()
		return nil, fmt.Errorf("start headless reminal: %w", err)
	}
	afterStart()

	// Release the child so we don't keep a zombie around if Spawn's
	// caller exits before the child does.
	_ = cmd.Process.Release()

	// Read the JSON handshake with a deadline. Reading until newline is
	// cheap and robust; a malformed payload (child wrote junk) shows up
	// as a parse error rather than a hang. On failure the child may
	// already be running fine with a broken handshake channel — don't
	// pretend we know its state; the user can `reminal list` / `kill`.
	line, err := recv(spawnHandshakeTimeout)
	if err != nil {
		return nil, err
	}
	var sp SpawnedSession
	if err := json.Unmarshal([]byte(line), &sp); err != nil {
		return nil, fmt.Errorf("parse handshake: %w", err)
	}
	return &sp, nil
}

// PrintSpawned formats the spawned session's credentials for the user's
// calling terminal — same shape as the foreground banner so the user
// recognises it instantly, plus a join-QR. The caller is expected to
// print this and then exit (the spawned agent keeps running detached).
func PrintSpawned(sp *SpawnedSession, name, version string) {
	fmt.Println()
	fmt.Printf("  reminal — new background session · v%s · %s\n", version, sp.ID)
	fmt.Println()
	if name = strings.TrimSpace(name); name != "" {
		fmt.Printf("  Name:     %s\n", name)
	}
	fmt.Printf("  Session:  %s\n", sp.ID)
	fmt.Printf("  PIN:      %s\n", sp.PIN)
	fmt.Printf("  Open:     %s\n", sp.OpenURL)
	// One-tap join link (PIN in the #p= fragment, auto-filled by the web
	// client and never sent to the server) — tap it from a phone.
	fmt.Printf("  Join:     %s#p=%s\n", sp.OpenURL, sp.PIN)
	fmt.Printf("  Connect:  reminal connect %s %s\n", sp.ID, sp.PIN)
	fmt.Printf("  PID:      %d  (detached — survives this terminal closing)\n", sp.PID)
	fmt.Println()
	// Reuse the same QR routine the foreground agent uses so phone
	// scans look identical. Builds the join URL with the PIN in the
	// fragment so the web client auto-fills.
	qrURL := sp.OpenURL + "#p=" + sp.PIN
	qrterminal.GenerateWithConfig(qrURL, qrterminal.Config{
		Level:     qrterminal.L,
		Writer:    os.Stdout,
		BlackChar: qrterminal.BLACK,
		WhiteChar: qrterminal.WHITE,
		QuietZone: 1,
	})
	fmt.Println()
	// Prefer the name in the hints when the user gave one — it's what
	// they'll remember, and resolveActive accepts it anywhere an ID works.
	ref := sp.ID
	if n := strings.TrimSpace(name); n != "" {
		ref = n
	}
	fmt.Println("  This session has no host terminal — to drive it from here, run:")
	fmt.Printf("    reminal attach %s\n", ref)
	fmt.Println("  To stop broadcasting:    reminal stop", ref)
	fmt.Println("  To terminate completely: reminal kill", ref)
	fmt.Println()
}

// ParseHandshakeFD returns the int value of --handshake-fd from os.Args
// if present, or 0 otherwise. Helper for cmd/reminal/main.go's flag
// plumbing — the agent's headless path reads it via AgentOptions.
func ParseHandshakeFD(args []string) int {
	for i := 0; i < len(args); i++ {
		if args[i] == "--handshake-fd" && i+1 < len(args) {
			n, err := strconv.Atoi(args[i+1])
			if err == nil {
				return n
			}
		}
	}
	return 0
}

// resolveSpawnDir picks cmd.Dir for a new headless session. An explicit
// absolute directory that exists wins; otherwise inherit (or home when the
// host itself is at "/").
func resolveSpawnDir(cwd string) (string, error) {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		// Start the session in the user's home when we have no meaningful working
		// directory of our own — i.e. when spawned by the background host, which runs
		// under launchd/systemd with cwd "/". A shell that opens at "/" isn't what a
		// real terminal gives you; "~" is. When `reminal new` is run from an actual
		// directory, that directory is inherited (empty return → cmd.Dir unset).
		if wd, err := os.Getwd(); err != nil || wd == "/" {
			if home, err := os.UserHomeDir(); err == nil {
				return home, nil
			}
		}
		return "", nil
	}
	if !filepath.IsAbs(cwd) {
		return "", fmt.Errorf("cwd must be an absolute path")
	}
	cwd = filepath.Clean(cwd)
	st, err := os.Stat(cwd)
	if err != nil {
		return "", fmt.Errorf("cwd: %w", err)
	}
	if !st.IsDir() {
		return "", fmt.Errorf("cwd is not a directory")
	}
	return cwd, nil
}
