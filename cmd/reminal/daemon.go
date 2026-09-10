// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"github.com/reminal/reminal/internal/client"
)

// runDaemon backs the hidden `reminal daemon` command. Bare, it runs the
// always-on directory host in the foreground (what the login service invokes);
// with --install/--uninstall it manages that login service. Not shown in help —
// enrolling/removing an owner wires the service up for you.
func runDaemon(args []string) error {
	for _, a := range args {
		switch a {
		case "--install":
			return client.InstallDaemonService()
		case "--uninstall":
			return client.UninstallDaemonService()
		}
	}
	return client.RunDaemon(version)
}
