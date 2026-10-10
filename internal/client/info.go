// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"reminal/internal/session"
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
		out := bannerOut()
		fmt.Fprintln(out)
		fmt.Fprintln(out, "  reminal — remote terminal")
		fmt.Fprintln(out)
		fmt.Fprintf(out, "  Session:  %s\n", envID)
		fmt.Fprintln(out, "  (this session's host is on another machine — run `reminal info` there")
		fmt.Fprintln(out, "   for its PIN)")
		fmt.Fprintln(out)
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
	out := bannerOut()
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  reminal — remote terminal")
	printActiveDetails(a)
	fmt.Fprintln(out, "  Scan to join from your phone:")
	fmt.Fprintln(out)
	joinURL := fmt.Sprintf("%s#p=%s", a.OpenURL, a.PIN)
	printJoinQR(out, joinURL)
	fmt.Fprintln(out)
}

// printActiveDetails prints every textual field for a session — name, creds,
// links, where/what's running, age, idle, viewers — but NOT the QR. It's the
// shared body of the single-session banner and `reminal info --all`, so both
// surface the same information per session.
func printActiveDetails(a *session.Active) {
	out := bannerOut()
	fmt.Fprintln(out)
	if a.Name != "" {
		fmt.Fprintf(out, "  Name:     %s\n", a.Name)
	}
	fmt.Fprintf(out, "  Session:  %s\n", a.ID)
	fmt.Fprintf(out, "  PIN:      %s\n", a.PIN)
	fmt.Fprintf(out, "  Open:     %s\n", a.OpenURL)
	// One-tap join link with the PIN in the URL fragment (#p=…). The fragment
	// never leaves the device (browsers don't send it to the server); the web
	// client reads it to auto-fill the PIN. Ideal for tapping on a phone.
	fmt.Fprintf(out, "  Join:     %s#p=%s\n", a.OpenURL, a.PIN)
	fmt.Fprintf(out, "  Connect:  reminal connect %s %s\n", a.ID, a.PIN)
	// Where it's running / what's running — so iterating many sessions tells
	// you which is which without attaching to each.
	if a.Cwd != "" {
		fmt.Fprintf(out, "  Dir:      %s\n", a.Cwd)
	}
	if a.Title != "" {
		fmt.Fprintf(out, "  Running:  %s\n", a.Title)
	}
	// PID + StartedAt are only known on the host machine. Skip them
	// gracefully when we're reconstructing the banner from env vars
	// on a remote (no local active record).
	if a.PID > 0 && !a.StartedAt.IsZero() {
		fmt.Fprintf(out, "  Started:  %s (PID %d)\n", a.StartedAt.Format(time.RFC3339), a.PID)
		if !a.LastActivity.IsZero() {
			fmt.Fprintf(out, "  Idle:     %s\n", time.Since(a.LastActivity).Round(time.Second))
		}
		if a.Viewers > 0 {
			fmt.Fprintf(out, "  Viewers:  %d currently attached\n", a.Viewers)
		} else {
			fmt.Fprintln(out, "  Viewers:  none currently attached")
		}
	}
	fmt.Fprintln(out)
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
	return json.NewEncoder(os.Stdout).Encode(InfoJSON(a))
}

// ShowActiveQR prints just the join-URL QR code for the running agent, no
// banner. Handy for showing on a second monitor or in a video call without
// the rest of the session details cluttering the frame.
func ShowActiveQR() error {
	out := bannerOut()
	a, err := resolveActiveForInfo()
	if err != nil {
		return err
	}
	joinURL := fmt.Sprintf("%s#p=%s", a.OpenURL, a.PIN)
	printJoinQR(out, joinURL)
	return nil
}

// ShowInfoFor prints the full join banner (name, session, PIN, URL, QR, plus
// the working dir and what's running) for an already-resolved session. Used by
// `reminal info <id|name>` and `reminal info --all --qr`.
func ShowInfoFor(a *session.Active) { printActiveBanner(a) }

// ShowQRFor prints just the join QR for an already-resolved session. Used by
// `reminal qr <id|name>`.
func ShowQRFor(a *session.Active) {
	out := bannerOut()
	joinURL := fmt.Sprintf("%s#p=%s", a.OpenURL, a.PIN)
	printJoinQR(out, joinURL)
}

// ShowInfoDetails prints the full per-session detail block (everything the
// single-session banner shows except the QR) for an already-resolved session.
// Used by `reminal info --all` so each session shows the same information as
// `reminal info <id>` would. Pair with ShowInfoFor when a QR is wanted too.
func ShowInfoDetails(a *session.Active) { printActiveDetails(a) }

// InfoJSON returns the JSON-serialisable view of a session's connect details.
// Shared by the single-session and --all JSON paths so the shape stays
// identical. Exported so cmd/reminal can marshal one or an array of them.
func InfoJSON(a *session.Active) any {
	return struct {
		ID        string    `json:"id"`
		Name      string    `json:"name,omitempty"`
		PIN       string    `json:"pin"`
		OpenURL   string    `json:"open_url"`
		JoinURL   string    `json:"join_url"`
		Cwd       string    `json:"cwd,omitempty"`
		Title     string    `json:"title,omitempty"`
		PID       int       `json:"pid"`
		StartedAt time.Time `json:"started_at"`
	}{
		ID:        a.ID,
		Name:      a.Name,
		PIN:       a.PIN,
		OpenURL:   a.OpenURL,
		JoinURL:   fmt.Sprintf("%s#p=%s", a.OpenURL, a.PIN),
		Cwd:       a.Cwd,
		Title:     a.Title,
		PID:       a.PID,
		StartedAt: a.StartedAt,
	}
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
