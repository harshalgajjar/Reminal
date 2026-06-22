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
//     AND ~/.reminal/active.json matches, we show that record — works
//     whether the user is at the agent's host terminal or attached over
//     `reminal connect` from the same machine.
//  2. If REMINAL_SESSION is set but doesn't match the local record (e.g.
//     the user is on a different machine, connected via the relay), we
//     show the session ID we know — the PIN/URL aren't ours to print.
//  3. Otherwise we fall back to the local active.json (the original
//     behaviour) so `reminal info` from a fresh terminal still works.
func ShowActiveInfo() error {
	envID := os.Getenv("REMINAL_SESSION")
	if envID != "" {
		a, err := session.ReadActive()
		if err == nil && a.ID == envID {
			printActiveBanner(a)
			return nil
		}
		// We're inside a session that isn't recorded on this machine.
		// The PIN lives on the host; surface what we can and tell the
		// user where to look for the rest.
		fmt.Println()
		fmt.Println("  reminal — remote terminal")
		fmt.Println()
		fmt.Printf("  Session:  %s\n", envID)
		fmt.Println("  (this shell is connected to a session whose host is on another machine —")
		fmt.Println("   run `reminal info` on the host to see the PIN and join URL)")
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
	fmt.Printf("  Started:  %s (PID %d)\n", a.StartedAt.Format(time.RFC3339), a.PID)
	if a.Viewers > 0 {
		fmt.Printf("  Viewers:  %d currently attached\n", a.Viewers)
	} else {
		fmt.Println("  Viewers:  none currently attached")
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
	a, err := loadActive()
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
	a, err := loadActive()
	if err != nil {
		return err
	}
	joinURL := fmt.Sprintf("%s#p=%s", a.OpenURL, a.PIN)
	qrterminal.GenerateHalfBlock(joinURL, qrterminal.L, os.Stdout)
	return nil
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
