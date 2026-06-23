package client

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/mdp/qrterminal/v3"
	"github.com/reminal/reminal/internal/session"
)

// ShowActiveInfo reprints the join details for the session the user is
// "in" right now. Resolution order:
//
//  1. If REMINAL_SESSION is set (the agent injects it into the spawned shell)
//     AND a matching local active record exists, we show that — works at
//     the agent's host terminal or attached over `reminal connect` from
//     the same machine. Includes started-at / PID / live viewer count.
//  2. If REMINAL_SESSION is set but no matching local record, we fall
//     back to the PIN + URL the agent injected into the shell env
//     (REMINAL_SESSION_PIN + REMINAL_SESSION_URL). Same banner shape,
//     just no host-only fields (PID / started-at / viewer count).
//  3. Otherwise we fall back to the local active.json so `reminal info`
//     from a fresh terminal still works.
func ShowActiveInfo() error {
	envID := os.Getenv("REMINAL_SESSION")
	if envID != "" {
		a, err := session.ReadActiveByID(envID)
		if err == nil && a != nil {
			printActiveBanner(a)
			return nil
		}
		// Not in our local active records — likely we're on a
		// different machine than the agent (e.g. SSH'd into the host,
		// or a nested viewer setup). The agent injected the PIN and
		// URL into env, so we can still rebuild the join banner.
		pin := os.Getenv("REMINAL_SESSION_PIN")
		openURL := os.Getenv("REMINAL_SESSION_URL")
		if pin != "" && openURL != "" {
			printActiveBanner(&session.Active{
				ID:      envID,
				PIN:     pin,
				OpenURL: openURL,
			})
			return nil
		}
		// Old agent that didn't inject PIN/URL into env. Fall back to
		// the previous stub display so the user at least sees the
		// session id.
		fmt.Println()
		fmt.Println("  reminal — remote terminal")
		fmt.Println()
		fmt.Printf("  Session:  %s\n", envID)
		fmt.Println("  (this shell is connected to a session whose host is on another machine,")
		fmt.Println("   and the host agent is too old to share its PIN/URL — upgrade the host")
		fmt.Println("   to ≥ v0.7.11 or run `reminal info` on the host directly)")
		fmt.Println()
		return nil
	}

	a, err := loadActive()
	if err != nil {
		return err
	}
	printActiveBanner(a)
	return nil
}

func printActiveBanner(a *session.Active) {
	fmt.Println()
	fmt.Println("  reminal — remote terminal")
	fmt.Println()
	fmt.Printf("  Session:  %s\n", a.ID)
	fmt.Printf("  PIN:      %s\n", a.PIN)
	fmt.Printf("  Open:     %s\n", a.OpenURL)
	fmt.Printf("  Connect:  reminal connect %s %s\n", a.ID, a.PIN)
	// PID + StartedAt are only known on the host machine. Skip them
	// gracefully when we're reconstructing the banner from env vars
	// on a remote (no local active record).
	if a.PID > 0 && !a.StartedAt.IsZero() {
		fmt.Printf("  Started:  %s (PID %d)\n", a.StartedAt.Format(time.RFC3339), a.PID)
		if a.Viewers > 0 {
			fmt.Printf("  Viewers:  %d currently attached\n", a.Viewers)
		} else {
			fmt.Println("  Viewers:  none currently attached")
		}
	}
	fmt.Println()
	fmt.Println("  Scan to join from your phone:")
	fmt.Println()
	joinURL := fmt.Sprintf("%s#p=%s", a.OpenURL, a.PIN)
	qrterminal.GenerateHalfBlock(joinURL, qrterminal.L, os.Stdout)
	fmt.Println()
}

// ShowActiveInfoJSON prints the active session as a one-line JSON object on
// stdout. Composable with shell scripts: `reminal info --json | jq .id`.
// The connect-URL form (`open_url` plus PIN fragment) is included so external
// tools don't have to reassemble it.
func ShowActiveInfoJSON() error {
	a, err := resolveActiveForInfo()
	if err != nil {
		return err
	}
	out := struct {
		ID        string    `json:"id"`
		PIN       string    `json:"pin"`
		OpenURL   string    `json:"open_url"`
		JoinURL   string    `json:"join_url"`
		PID       int       `json:"pid"`
		StartedAt time.Time `json:"started_at"`
	}{
		ID:        a.ID,
		PIN:       a.PIN,
		OpenURL:   a.OpenURL,
		JoinURL:   fmt.Sprintf("%s#p=%s", a.OpenURL, a.PIN),
		PID:       a.PID,
		StartedAt: a.StartedAt,
	}
	enc := json.NewEncoder(os.Stdout)
	return enc.Encode(out)
}

// ShowActiveQR prints just the join-URL QR code for the running agent, no
// banner. Handy for showing on a second monitor or in a video call without
// the rest of the session details cluttering the frame.
func ShowActiveQR() error {
	a, err := resolveActiveForInfo()
	if err != nil {
		return err
	}
	joinURL := fmt.Sprintf("%s#p=%s", a.OpenURL, a.PIN)
	qrterminal.GenerateHalfBlock(joinURL, qrterminal.L, os.Stdout)
	return nil
}

// resolveActiveForInfo finds the session to describe from any of the
// places the info / qr / --json commands can recover it: local active
// record matching REMINAL_SESSION, then the env-injected PIN/URL
// fallback, then the lone local active record. Keeps info / qr /
// --json behaving the same way as the human-banner path above.
func resolveActiveForInfo() (*session.Active, error) {
	if envID := os.Getenv("REMINAL_SESSION"); envID != "" {
		if a, err := session.ReadActiveByID(envID); err == nil && a != nil {
			return a, nil
		}
		pin := os.Getenv("REMINAL_SESSION_PIN")
		openURL := os.Getenv("REMINAL_SESSION_URL")
		if pin != "" && openURL != "" {
			return &session.Active{ID: envID, PIN: pin, OpenURL: openURL}, nil
		}
	}
	return loadActive()
}

func loadActive() (*session.Active, error) {
	a, err := session.ReadActive()
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no active reminal session — start one with `reminal`")
		}
		return nil, err
	}
	return a, nil
}
