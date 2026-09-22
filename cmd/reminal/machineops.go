// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"fmt"
	"strings"
	"time"

	"reminal/internal/client"
	"reminal/internal/config"
)

// machineOpTimeout bounds a remote directory-channel op (dial + owner handshake
// + the host's spawn/kill). Spawn runs the same detached-session handshake the
// host uses locally, so it needs headroom beyond a plain query.
const machineOpTimeout = 25 * time.Second

// runNewOnMachine starts a fresh session on another machine you own — over its
// owner directory channel — and prints how to connect. `selector` is a machine
// name or a mach_ id, exactly as shown by `reminal machines`.
func runNewOnMachine(name, selector, cwd string) error {
	om, err := client.ResolveOwnedMachine(selector)
	if err != nil {
		return err
	}
	// Naming THIS machine just means a normal local `reminal new` — no round trip
	// (and the machine doesn't host a directory channel to itself for spawning).
	if local, _ := client.MachinePub(); local != nil && om.Key.Equal(local) {
		selfHealBundle()
		return runNew(name, cwd)
	}
	label := machineLabel(om)
	sp, err := client.SpawnOnMachine(om.Key, name, cwd, machineOpTimeout)
	if err != nil {
		return fmt.Errorf("start a session on %s: %w", label, err)
	}
	fmt.Println()
	fmt.Printf("  %s Started a session on %s\n", cGreen("✓"), cBold(label))
	fmt.Println()
	fmt.Printf("  Session:  %s\n", sp.ID)
	fmt.Printf("  PIN:      %s\n", sp.PIN)
	fmt.Printf("  Connect:  %s\n", cBold("reminal connect "+sp.ID+" "+sp.PIN))
	if web := strings.TrimRight(config.WebURL(), "/"); web != "" {
		fmt.Printf("  Open:     %s/?s=%s#p=%s\n", web, sp.ID, sp.PIN)
	}
	fmt.Println()
	return nil
}

// runKillOnMachine terminates a session on another machine you own — over its
// owner directory channel. `selector` is a machine name or mach_ id. Remote
// termination is by session id (the id shown in `reminal machines`); the host
// upper-cases it, so any case works.
//
// localFn handles the case where the selector names THIS machine: `kill
// --machine <self>` passes runKill, `stop --machine <self>` passes runStop, so
// the local path keeps each verb's own meaning (kill ends the shell; stop only
// stops broadcasting). Remotely there is no "stop broadcasting", so both verbs
// terminate the session.
func runKillOnMachine(sessionID, selector string, yes bool, localFn func(string, bool) error) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return fmt.Errorf("which session? usage: reminal kill <session> --machine <id|name>")
	}
	om, err := client.ResolveOwnedMachine(selector)
	if err != nil {
		return err
	}
	// This machine → the normal local verb, which accepts an id OR a name. (Don't
	// upper-case first: that would break name matching on the local path.)
	if local, _ := client.MachinePub(); local != nil && om.Key.Equal(local) {
		return localFn(sessionID, yes)
	}
	label := machineLabel(om)
	if err := client.KillOnMachine(om.Key, sessionID, machineOpTimeout); err != nil {
		return fmt.Errorf("end %s on %s: %w", sessionID, label, err)
	}
	fmt.Printf("  %s Ended %s on %s\n", cGreen("✓"), cBold(strings.ToUpper(sessionID)), cBold(label))
	return nil
}

// machineLabel is the friendly name for an owned machine — its user-set name, or
// its short mach_ id when it has none.
func machineLabel(om client.OwnedMachine) string {
	if strings.TrimSpace(om.Name) != "" {
		return om.Name
	}
	return client.ShortMachineID(om.Key)
}
