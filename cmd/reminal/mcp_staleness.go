// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/reminal/reminal/internal/updater"
)

// The tool schema a client sees is fetched once — when it spawns `reminal mcp`
// and calls tools/list — then cached for that client session. reminal can update
// itself underneath that running child (the process re-execs onto a newer build,
// or the on-disk binary is replaced), and the client keeps showing the OLD list.
// An agent then reasons against a stale schema: a real case had an agent conclude
// read_transcript "has no pin parameter" and file a bug for a parameter that had
// already shipped, purely because its cached list predated it.
//
// These helpers detect that skew so every tool result can carry a one-line
// warning (as its own content block, never inlined into a result an agent may
// parse), letting the agent refresh instead of trusting a stale list.

// bootVersionEnv carries the version this process FIRST booted at across an
// in-place re-exec (self-update inherits the environment). A process that
// re-execs onto a newer binary then sees the ORIGINAL version here and can tell
// that the schema the client cached at first boot is now out of date.
const bootVersionEnv = "REMINAL_MCP_BOOT_VERSION"

// rememberBootVersion records the first-boot version in the environment if it
// isn't already set, so it survives a later re-exec. Called once at mcp startup.
func rememberBootVersion() {
	if strings.TrimSpace(os.Getenv(bootVersionEnv)) == "" {
		_ = os.Setenv(bootVersionEnv, version)
	}
}

var (
	toolsListServed  atomic.Bool
	cacheReuseWarned atomic.Bool
)

// noteToolsListServed records that this process answered tools/list, i.e. the
// client's list is one WE produced.
func noteToolsListServed() { toolsListServed.Store(true) }

// cacheReuseWarning fires when a tool is called by a client that never asked
// THIS process for its tool list. A conforming client calls tools/list after
// initialize on every server process it starts, so the combination means the
// client is working from a list some earlier process produced.
//
// That is the stale-schema case no version check can see: the running server is
// perfectly current, and the list the agent is reasoning about is not. It is
// what made a real report unfalsifiable from the inside — every test confirmed
// the wrong theory, because the agent could only ever call the tool the one way
// its cached list allowed.
//
// A heuristic rather than a proven mismatch, so it is said ONCE per process; the
// version-skew warnings repeat on every call because those are provable.
func cacheReuseWarning() string {
	if toolsListServed.Load() || !cacheReuseWarned.CompareAndSwap(false, true) {
		return ""
	}
	return "⚠ reminal: your client is using a tool list this server never sent it — that list came from an earlier reminal process, so it may predate an update. Re-request the tool list to be sure. If a parameter you need isn't listed, pass it anyway: this server accepts every parameter it supports, whether or not your cached list mentions it."
}

// versionIsReal is false for dev / unstamped builds, where version comparison is
// meaningless and any warning would be noise.
func versionIsReal(v string) bool {
	v = strings.TrimSpace(v)
	return v != "" && v != "dev" && v != "0.0.0"
}

const onDiskVersionTTL = 60 * time.Second

var (
	onDiskMu  sync.Mutex
	onDiskVal string
	onDiskAt  time.Time
)

// onDiskReminalVersion returns the version of the reminal binary currently at
// this process's path — which differs from our compiled-in version once reminal
// has replaced the file underneath us. Cached to at most one exec per TTL, so a
// tool call never pays for more than a stat-cheap lookup. Returns "" if it can't
// be determined. A var so tests can stub it.
var onDiskReminalVersion = func() string {
	onDiskMu.Lock()
	defer onDiskMu.Unlock()
	if !onDiskAt.IsZero() && time.Since(onDiskAt) < onDiskVersionTTL {
		return onDiskVal
	}
	onDiskAt = time.Now()
	onDiskVal = ""
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// `reminal version` prints just the version and exits — side-effect-free.
	out, err := exec.CommandContext(ctx, exe, "version").Output()
	if err != nil {
		return ""
	}
	onDiskVal = strings.TrimSpace(string(out))
	return onDiskVal
}

// schemaStaleWarning returns a one-line warning to attach to tool results when
// reminal has updated itself since the client cached the tool list, or "" when
// there's no detectable skew. It never blocks longer than the cached lookup.
func schemaStaleWarning() string {
	if !versionIsReal(version) {
		return ""
	}
	// Case 1 — re-exec: we booted at `boot`, then re-execed onto a newer build.
	// This process (version) is the NEW one and already honors the newer params;
	// only the client's cached list, from `boot`, is behind.
	if boot := strings.TrimSpace(os.Getenv(bootVersionEnv)); versionIsReal(boot) && boot != version && updater.Newer(boot, version) {
		return staleLine(version, boot, true)
	}
	// Case 2 — on-disk newer: the file was replaced but this process still runs
	// the older code, so both the client's list and this server are behind.
	if disk := onDiskReminalVersion(); versionIsReal(disk) && updater.Newer(version, disk) {
		return staleLine(version, disk, false)
	}
	return ""
}

// staleLine builds the warning. runningIsNewer distinguishes the two skews: when
// this server is the newer build (a re-exec), passing the newer params works now;
// when it's the older build (on-disk was replaced), the client must restart first.
func staleLine(runningVer, otherVer string, runningIsNewer bool) string {
	if runningIsNewer {
		return fmt.Sprintf("⚠ reminal version skew: this MCP server updated itself to v%s, but your client cached its tool list at v%s (before the update). The list you have may be missing newer parameters — this is exactly how read_transcript's `pin` went unnoticed. Ask your MCP client to reload / re-request tools (or restart it). The running server already accepts the newer parameters if you pass them.", runningVer, otherVer)
	}
	return fmt.Sprintf("⚠ reminal version skew: reminal on disk is v%s but this MCP server still runs v%s — it updated itself and this server is now behind. Restart your MCP client so it re-spawns the newer reminal and refreshes the tool list; newer parameters will not work until you do.", otherVer, runningVer)
}
