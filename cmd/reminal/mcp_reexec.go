// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"bufio"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// reloadedEnv marks a process that re-exec'd itself onto a new binary, so the
// new image knows to tell the client its tool list has changed.
const reloadedEnv = "REMINAL_MCP_RELOADED"

// binaryWatchInterval polls our own binary for the atomic rename an upgrade
// performs. One stat, and an upgrade is rare — the goal is to be current within
// seconds, not milliseconds.
var binaryWatchInterval = 5 * time.Second

// mcpIdleWait is how long the request reader waits for a line before it
// counts the process idle (mcpReadLines). The same order as the binary
// watch: a swap can never be noticed sooner than that anyway.
var mcpIdleWait = binaryWatchInterval

var binarySwapped atomic.Bool

// mcpWatchBinary notices when our own on-disk binary is replaced — reminal
// upgrading itself underneath us — and raises a flag the main loop acts on
// between requests, never mid-call.
//
// The daemon solves this by exiting and letting its service manager restart it
// (watchBinaryAndExit). An MCP server has no service manager: it is a stdio
// child of the client, and if it exits the client may not bring it back, which
// is worse than serving a stale tool list. So we re-exec in place instead.
// mcpWatchBinary raises binarySwapped once the binary on disk is not the one
// this process started from, then returns; mcpIdle acts on it.
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
// Called only when nothing of the client's is in this process's hands: just
// after a request was answered (the client has not received the reply, so it
// has not sent the next one), or when the reader's wait lapsed with nothing
// read and nothing half read (mcpReadLines). Anything unread in a buffer
// would not survive exec; what is still in the kernel's pipe does.
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
	// The new image is told to announce the change; this one's environment
	// is left as it is, in case the exec fails and it carries on.
	execSelf(exe, os.Args, append(os.Environ(), reloadedEnv+"=1"))
}

// mcpReadLines hands each line of a client's requests to handle, and between
// lines — only then — lets the process swap onto a newly installed binary.
//
// Waiting on a line blocks, and a client that never calls a reminal tool
// never sends one: an agent started before reminal was upgraded kept the
// old build's tools for as long as it ran, whatever the new build added.
// So the wait has a deadline. When it lapses with nothing read and nothing
// buffered, the process is provably idle: anything the client has written
// since is still in the kernel's pipe, and survives exec for the new image
// to read. A partial line read before the deadline is kept and completed
// first; exec never happens while one is held.
func mcpReadLines(in *os.File, handle func(line string)) {
	f := mcpPollable(in)
	rd := bufio.NewReaderSize(f, 64<<10) // a line longer than this is gathered in partial
	var partial []byte
	for {
		// A fresh deadline for every wait, whether or not a line is half
		// read: a deadline is a bound on one wait, and one left in the past
		// makes every read after it fail at once — the server then never
		// reads again (a re-exec'd image inherits an already-pollable stdin
		// and did exactly that after its first request).
		_ = f.SetReadDeadline(time.Now().Add(mcpIdleWait))
		chunk, err := rd.ReadString('\n')
		partial = append(partial, chunk...)
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				// On a lapsed wait bufio has handed back all it held, so
				// partial alone says whether a request is half read.
				if len(partial) == 0 {
					mcpIdle() // nothing of the client's is in our hands
				}
				continue
			}
			// The client closed its end. A last line without its newline is
			// still a request.
			if line := strings.TrimSpace(string(partial)); line != "" {
				handle(line)
			}
			return
		}
		line := strings.TrimSpace(string(partial))
		partial = partial[:0]
		if line == "" {
			continue
		}
		handle(line)
		// Just answered: the client has not sent the next request yet, and
		// nothing is buffered to lose across exec.
		if rd.Buffered() == 0 {
			mcpIdle()
		}
	}
}

// mcpIdle runs when the server provably holds nothing of the client's. What it
// does is swap onto a newer binary if one was installed; a variable so the
// reader's timing can be tested without an exec.
var mcpIdle = maybeReexec
