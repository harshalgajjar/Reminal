package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/reminal/reminal/internal/config"
	"github.com/reminal/reminal/internal/protocol"
	"github.com/reminal/reminal/internal/session"
	"golang.org/x/term"
)

// Doctor runs a series of environment checks and prints a color-coded report.
// It's meant for users who want to confirm reminal is set up correctly or
// debug why something isn't working — equivalent in spirit to `brew doctor`
// or `docker info`.
func Doctor(currentVersion string) error {
	fmt.Println()
	fmt.Println("  reminal doctor")
	fmt.Println()

	worst := levelOK
	for _, c := range allChecks(currentVersion) {
		lvl, summary := c.run()
		fmt.Printf("  %s  %-14s %s\n", badge(lvl), c.name, summary)
		if lvl > worst {
			worst = lvl
		}
	}

	fmt.Println()
	switch worst {
	case levelOK:
		fmt.Println("  All good. Run `reminal` to start sharing.")
	case levelWarn:
		fmt.Println("  Mostly good — warnings above are non-blocking but worth a look.")
	case levelFail:
		fmt.Println("  Something needs fixing. Address the FAIL lines above.")
		fmt.Println("  Stuck? Open an issue with this output:")
		fmt.Println("    https://github.com/harshalgajjar/Reminal/issues")
	}
	fmt.Println()
	if worst == levelFail {
		return errors.New("doctor: one or more checks failed")
	}
	return nil
}

type level int

const (
	levelOK level = iota
	levelWarn
	levelFail
)

type check struct {
	name string
	run  func() (level, string)
}

func allChecks(currentVersion string) []check {
	return []check{
		{"Version", func() (level, string) { return checkVersion(currentVersion) }},
		{"Relay", checkRelay},
		{"WebSocket", checkRelayWS},
		{"Terminal", checkTerminal},
		{"Shell", checkShell},
		{"Active session", checkActiveSession},
		{"Config dir", checkConfigDir},
	}
}

func badge(l level) string {
	switch l {
	case levelOK:
		return "\x1b[32m[ OK ]\x1b[0m"
	case levelWarn:
		return "\x1b[33m[WARN]\x1b[0m"
	case levelFail:
		return "\x1b[31m[FAIL]\x1b[0m"
	}
	return "[????]"
}

func checkVersion(current string) (level, string) {
	if current == "" || current == "dev" {
		return levelWarn, "dev build — version check skipped"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET",
		"https://api.github.com/repos/harshalgajjar/Reminal/releases/latest", nil)
	if err != nil {
		return levelWarn, fmt.Sprintf("v%s (couldn't check GitHub: %v)", current, err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return levelWarn, fmt.Sprintf("v%s (couldn't reach GitHub: %v)", current, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return levelWarn, fmt.Sprintf("v%s (GitHub returned %s)", current, resp.Status)
	}
	var rel struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return levelWarn, fmt.Sprintf("v%s (couldn't parse GitHub response)", current)
	}
	latest := strings.TrimPrefix(rel.TagName, "v")
	if latest == current {
		return levelOK, fmt.Sprintf("v%s (latest)", current)
	}
	return levelWarn, fmt.Sprintf("v%s — newer available: %s (run `reminal upgrade`)", current, rel.TagName)
}

func checkRelay() (level, string) {
	// Probe the web URL (https) since /ws is a WebSocket upgrade endpoint and
	// can't be reached with a plain GET; both share the same Cloudflare host.
	url := config.WebURL()
	if url == "" {
		return levelFail, "no relay configured (REMINAL_RELAY/REMINAL_WEB unset)"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return levelFail, fmt.Sprintf("%s — bad URL: %v", url, err)
	}
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return levelFail, fmt.Sprintf("%s — unreachable: %v", url, err)
	}
	resp.Body.Close()
	elapsed := time.Since(start).Round(time.Millisecond)
	if resp.StatusCode >= 500 {
		return levelFail, fmt.Sprintf("%s — relay returned %s (%v)", url, resp.Status, elapsed)
	}
	return levelOK, fmt.Sprintf("%s — reachable, %v", url, elapsed)
}

// checkRelayWS confirms the relay accepts a WebSocket upgrade. Many corporate
// proxies pass plain HTTPS but strip the Upgrade header (or sit behind a
// load balancer that does), which would make checkRelay green but reminal
// hang at "Connecting…" forever. We dial a dummy session ID and accept any
// outcome other than a transport-layer failure as proof that WS works —
// the relay is expected to immediately close with "session not found" since
// AAAAAAAA isn't a real session.
func checkRelayWS() (level, string) {
	url := config.SessionWS("AAAAAAAA", string(protocol.RoleViewer))
	dialer := *websocket.DefaultDialer
	dialer.HandshakeTimeout = 3 * time.Second
	start := time.Now()
	conn, _, err := dialer.Dial(url, nil)
	if err != nil {
		return levelFail, fmt.Sprintf("%s — upgrade failed: %v", url, err)
	}
	elapsed := time.Since(start).Round(time.Millisecond)
	_ = conn.Close()
	return levelOK, fmt.Sprintf("upgrade OK (%v) — relay accepts WS connections", elapsed)
}

func checkTerminal() (level, string) {
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		return levelWarn, "stdout is not a TTY (running in a pipe?)"
	}
	cols, rows, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		return levelWarn, fmt.Sprintf("can't read terminal size: %v", err)
	}
	t := os.Getenv("TERM")
	if t == "" {
		t = "(TERM unset)"
	}
	return levelOK, fmt.Sprintf("%s, %dx%d", t, cols, rows)
}

func checkShell() (level, string) {
	sh := config.Shell()
	if _, err := os.Stat(sh); err != nil {
		return levelFail, fmt.Sprintf("%s not found or unreadable", sh)
	}
	return levelOK, sh
}

func checkActiveSession() (level, string) {
	a, err := session.ReadActive()
	if errors.Is(err, os.ErrNotExist) {
		return levelOK, "none (run `reminal` to start one)"
	}
	if err != nil {
		return levelWarn, fmt.Sprintf("couldn't read active record: %v", err)
	}
	return levelOK, fmt.Sprintf("%s (PID %d, started %s)", a.ID, a.PID, a.StartedAt.Format(time.RFC3339))
}

func checkConfigDir() (level, string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return levelFail, fmt.Sprintf("can't find home dir: %v", err)
	}
	dir := filepath.Join(home, ".reminal")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return levelFail, fmt.Sprintf("%s not writable: %v", dir, err)
	}
	// Round-trip a sentinel file to confirm we can actually write.
	tmp, err := os.CreateTemp(dir, ".doctor-*")
	if err != nil {
		return levelFail, fmt.Sprintf("%s not writable: %v", dir, err)
	}
	name := tmp.Name()
	_ = tmp.Close()
	_ = os.Remove(name)
	return levelOK, fmt.Sprintf("%s writable", dir)
}
