package client

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
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
// The child is fully decoupled from this process — it gets its own
// session leader (Setsid) and stdin/stdout/stderr are wired to
// /dev/null, so the parent can exit immediately after printing the
// credentials and the agent keeps running in the background.
func Spawn(name string) (*SpawnedSession, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate self: %w", err)
	}

	// Pipe used as fd 3 in the child. The child writes one JSON line
	// once startup is complete; we read it here and surface to the user.
	r, w, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("create handshake pipe: %w", err)
	}
	defer r.Close()

	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("open /dev/null: %w", err)
	}
	defer devnull.Close()

	cmd := exec.Command(exe, "--headless", "--handshake-fd", "3")
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
	cmd.ExtraFiles = []*os.File{w}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("start headless reminal: %w", err)
	}
	// Close our copy of the write end so the read EOFs once the child
	// closes its copy (e.g., child crashed before writing). Without this
	// the parent would block until the child fully exits.
	_ = w.Close()

	// Release the child so we don't keep a zombie around if Spawn's
	// caller exits before the child does.
	_ = cmd.Process.Release()

	// Read the JSON handshake with a deadline. Reading until newline is
	// cheap and robust; a malformed payload (child wrote junk) shows up
	// as a parse error rather than a hang.
	type result struct {
		s   *SpawnedSession
		err error
	}
	done := make(chan result, 1)
	go func() {
		br := bufio.NewReader(r)
		line, err := br.ReadString('\n')
		if err != nil {
			done <- result{nil, fmt.Errorf("read handshake: %w", err)}
			return
		}
		var sp SpawnedSession
		if err := json.Unmarshal([]byte(line), &sp); err != nil {
			done <- result{nil, fmt.Errorf("parse handshake: %w", err)}
			return
		}
		done <- result{&sp, nil}
	}()

	select {
	case res := <-done:
		if res.err != nil {
			// The child may already be running fine but with a broken
			// handshake pipe. Don't pretend we know its state — just
			// surface the error so the user can `reminal list` to find
			// out and `reminal kill` if needed.
			return nil, res.err
		}
		return res.s, nil
	case <-time.After(spawnHandshakeTimeout):
		return nil, errors.New("headless reminal didn't report ready within " + spawnHandshakeTimeout.String())
	}
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
