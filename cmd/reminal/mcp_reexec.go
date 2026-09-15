// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"os"
	"sync/atomic"
	"time"
)

// reloadedEnv marks a process that re-exec'd itself onto a new binary, so the
// new image knows to tell the client its tool list has changed.
const reloadedEnv = "REMINAL_MCP_RELOADED"

// binaryWatchInterval polls our own binary for the atomic rename an upgrade
// performs. One stat, and an upgrade is rare — the goal is to be current within
// seconds, not milliseconds.
const binaryWatchInterval = 5 * time.Second

var binarySwapped atomic.Bool

// mcpWatchBinary notices when our own on-disk binary is replaced — reminal
// upgrading itself underneath us — and raises a flag the main loop acts on
// between requests, never mid-call.
//
// The daemon solves this by exiting and letting its service manager restart it
// (watchBinaryAndExit). An MCP server has no service manager: it is a stdio
// child of the client, and if it exits the client may not bring it back, which
// is worse than serving a stale tool list. So we re-exec in place instead.
func mcpWatchBinary(stop <-chan struct{}) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	base, err := os.Stat(exe)
	if err != nil {
		return
	}
	t := time.NewTicker(binaryWatchInterval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			fi, err := os.Stat(exe)
			if err != nil {
				continue // caught mid-rename; look again next tick
			}
			if !fi.ModTime().Equal(base.ModTime()) || fi.Size() != base.Size() {
				binarySwapped.Store(true)
				return
			}
		}
	}
}

// maybeReexec replaces this process with the newly installed binary, keeping
// stdin/stdout — the client's pipes — open across the swap, so the client never
// sees its server disconnect and never has to respawn anything.
//
// Called only just after answering a request. At that instant the client has not
// yet received our reply, so it has not sent the next one: nothing is in flight
// to lose. (Anything sitting unread in our buffer would not survive exec, which
// is why this is never called while a read is pending.)
//
// On success it does not return. On failure — or on Windows, which has no exec —
// we stay on the old image and fall back to the staleness warning, since a
// server that exits is one the client may never bring back.
func maybeReexec() {
	if !binarySwapped.CompareAndSwap(true, false) {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	// Survives exec, and tells the new image to announce the change.
	_ = os.Setenv(reloadedEnv, "1")
	execSelf(exe, os.Args, os.Environ())
}
