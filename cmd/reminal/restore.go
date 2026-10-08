// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"reminal/internal/atrest"
	"reminal/internal/client"
	"reminal/internal/session"
)

// runRestore lists the sessions a restart ended, or brings them back (see
// client/restore.go): `reminal restore`, `reminal restore <id|name>`,
// `reminal restore --all`.
func runRestore(args []string) error {
	if len(args) > 0 && args[0] == "--records" {
		return printRestoreRecords()
	}
	gone, err := client.Restorable()
	if errors.Is(err, atrest.ErrCurrentKeyMissing) {
		fmt.Println("The key your saved sessions are protected with is missing from disk; they are kept as they are. Run `reminal doctor --repair-key` (running sessions still hold the key), then run this again.")
	} else if errors.Is(err, atrest.ErrLocked) {
		fmt.Println("Some saved sessions are locked in your keychain right now; unlock it (log in to the desktop) and run this again to see them.")
	} else if err != nil {
		return err
	}
	if len(args) == 0 {
		if len(gone) == 0 {
			fmt.Println("Nothing to restore — every session that was running still is.")
			return nil
		}
		fmt.Printf("%d session(s) ended by a restart:\n\n", len(gone))
		for _, r := range gone {
			what := "a shell"
			if r.Fg != "" {
				what = r.Fg
			}
			fmt.Printf("  %-20s %s  %s in %s  %s\n", r.Name, cBold(r.ID), what, r.Cwd, cDim("last seen "+restoreAgo(time.Since(r.SavedAt))+" ago"))
		}
		fmt.Println("\n  reminal restore <id|name>   bring one back · reminal restore --all")
		return nil
	}
	var pick []session.Restore
	if args[0] == "--all" || args[0] == "-a" {
		pick = gone
	} else {
		q := strings.ToLower(strings.TrimSpace(args[0]))
		for _, r := range gone {
			if strings.ToLower(r.ID) == q || strings.ToLower(r.Name) == q || strings.HasPrefix(strings.ToLower(r.ID), q) {
				pick = append(pick, r)
			}
		}
		if len(pick) != 1 {
			return fmt.Errorf("%d sessions to restore match %q — see reminal restore", len(pick), args[0])
		}
	}
	if len(pick) == 0 {
		fmt.Println("Nothing to restore.")
		return nil
	}
	failed := 0
	for _, r := range pick {
		sp, err := client.RestoreSession(r)
		if err != nil && client.SessionRunning(r.ID) {
			// It did not report back in time, but it is running.
			fmt.Printf("  %s %s %s back — it was slow to say so; see reminal list\n", cGreen("✓"), cBold(r.ID), cDim(r.Name))
			continue
		}
		if err != nil {
			fmt.Printf("  %s %s: %v\n", cRed("✗"), r.ID, err)
			failed++
			continue
		}
		note := "a new shell in " + r.Cwd
		if r.Fg != "" {
			note += ", " + r.Fg + " resumed"
		}
		fmt.Printf("  %s %s %s back — %s. PIN %s\n", cGreen("✓"), cBold(sp.ID), cDim(r.Name), note, sp.PIN)
	}
	if failed > 0 {
		return errors.New("some sessions could not be restored")
	}
	return nil
}

func restoreAgo(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// printRestoreRecords prints every saved session record as JSON — running
// sessions included, PIN and relay token left out. The records are sealed on
// disk; this is how a script (the restore test rigs) reads what they say.
func printRestoreRecords() error {
	all, err := session.ReadRestores()
	if err != nil && !errors.Is(err, atrest.ErrLocked) {
		return err
	}
	type rec struct {
		ID       string    `json:"id"`
		Name     string    `json:"name,omitempty"`
		Cwd      string    `json:"cwd,omitempty"`
		Headless bool      `json:"headless,omitempty"`
		Fg       string    `json:"fg,omitempty"`
		FgArgs   []string  `json:"fg_args,omitempty"`
		Conv     string    `json:"conv,omitempty"`
		SavedAt  time.Time `json:"saved_at"`
	}
	out := make([]rec, 0, len(all))
	for _, r := range all {
		out = append(out, rec{r.ID, r.Name, r.Cwd, r.Headless, r.Fg, r.FgArgs, r.Conv, r.SavedAt})
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(b))
	return err
}
