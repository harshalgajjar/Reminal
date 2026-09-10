// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build darwin

package client

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// bundlePath returns the path to the containing reminal.app if this binary lives
// inside one (…/reminal.app/Contents/MacOS/reminal), else "". Resolves symlinks
// first, since the installed CLI is a symlink into the bundle.
func bundlePath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}
	macos := filepath.Dir(exe)      // …/Contents/MacOS
	contents := filepath.Dir(macos) // …/Contents
	app := filepath.Dir(contents)   // …/reminal.app
	if filepath.Base(macos) == "MacOS" && filepath.Base(contents) == "Contents" &&
		strings.HasSuffix(app, ".app") {
		return app
	}
	return ""
}

// permStep is one guided permission: the token passed to the bundle to trigger
// just its prompt, the daemon socket command that reports its grant, and the
// human-facing copy.
type permStep struct {
	which string // bundle arg + picks the daemon check
	check string // daemon socket command (check/axcheck/autocheck)
	name  string
	why   string
	how   string // what the user does in the dialog
}

// permSteps is the fixed order reminal requests its three grants in.
var permSteps = []permStep{
	{which: "screen", check: "check", name: "Screen Recording",
		why: "mirror your windows and the desktop to viewers",
		how: "click Open System Settings, then switch reminal on"},
	{which: "accessibility", check: "axcheck", name: "Accessibility",
		why: "let viewers move the cursor, click, scroll, and drag",
		how: "click Open System Settings, then switch reminal on"},
	{which: "automation", check: "autocheck", name: "Automation",
		why: "type text and focus windows (it controls System Events)",
		how: "click OK"},
}

// EnsurePermissions implements `reminal permissions` as a GUIDED, one-at-a-time
// flow. When reminal is installed as reminal.app, a foreground `reminal` rides the
// TERMINAL's grants and the background daemon can't prompt at all — so viewing AND
// control silently fail for daemon-spawned ("+") sessions until reminal's OWN
// identity is granted. The subtlety this flow exists to fix: macOS shows only ONE
// TCC dialog at a time and DROPS any queued behind it, so firing all three at once
// (the old behavior) surfaced just one per run — users had to re-run three times.
// Here we explain each permission, request it alone, and poll the daemon (the only
// vantage point that reports reminal's real grant) until it lands before moving on.
func EnsurePermissions() error {
	app := bundlePath()
	if app == "" {
		fmt.Println("These permissions only apply to the packaged reminal.app.")
		fmt.Println("This build isn't a bundle, so it uses your terminal's grants.")
		return nil
	}
	// The flow polls the daemon (sh.reminal identity) for grant status, so it has to
	// be running. Idempotent — installs it if a prior upgrade/migration left it out.
	// version is unused on macOS: the bundle (checked just above) is the real-install
	// signal, so any value passes the darwin gate.
	EnsureDaemonInstalled("")
	// Give a freshly-(re)installed daemon a moment to bind its socket, so the
	// already-granted pre-checks below can see existing grants and skip them rather
	// than re-prompting. Reachable → returns "ok"/"no"; unreachable → "".
	for i := 0; i < 20 && mirrorGrantQuery("check") == ""; i++ {
		time.Sleep(150 * time.Millisecond)
	}

	fmt.Println()
	fmt.Println("reminal needs three macOS permissions. Granting each once lets viewing AND")
	fmt.Println("remote control work from every session — including background (\"+\") ones.")
	fmt.Println("I'll request them one at a time and wait for each; nothing is granted without")
	fmt.Println("your click.")

	newly := 0
	missing := 0
	for i := range permSteps {
		switch requestAndWait(app, permSteps[i], i+1, len(permSteps)) {
		case permNewlyGranted:
			newly++
		case permTimedOut, permError:
			missing++
		}
	}

	// A freshly-granted Screen Recording / Accessibility only reaches the RUNNING
	// daemon's capture/injection once it restarts under the new grant. Bounce it so
	// mirroring works immediately, no reboot.
	if newly > 0 {
		_ = RestartDaemonService()
	}

	fmt.Println()
	if missing == 0 {
		fmt.Println("\x1b[32m✓ All set\x1b[0m — screen recording, accessibility, and automation are granted.")
	} else {
		fmt.Printf("%d still pending. Re-run \x1b[1mreminal permissions\x1b[0m to finish "+
			"(already-granted steps are skipped instantly).\n", missing)
	}
	return nil
}

type permResult int

const (
	permAlready      permResult = iota // was already granted
	permNewlyGranted                   // granted during this run
	permTimedOut                       // user didn't grant within the window
	permError                          // couldn't launch the prompt
)

// requestAndWait runs one guided step: explain it, skip if already granted, else
// trigger JUST this prompt (attributed to the bundle) and poll the daemon until the
// grant lands or a timeout.
func requestAndWait(app string, s permStep, n, total int) permResult {
	fmt.Println()
	fmt.Printf("\x1b[1mStep %d/%d — %s\x1b[0m\n", n, total, s.name)
	fmt.Printf("  Why: reminal needs it to %s.\n", s.why)

	if mirrorGrantQuery(s.check) == "ok" {
		fmt.Printf("  \x1b[32m✓ already granted\x1b[0m\n")
		return permAlready
	}

	// Trigger this one prompt, attributed to sh.reminal, by launching a fresh bundle
	// instance (-n) with the per-permission flag so the args are actually delivered
	// even if a previous step's instance is still winding down. `open` returns
	// immediately; the launched app keeps its dialog alive until the user answers.
	if err := exec.Command("/usr/bin/open", "-n", "-a", app,
		"--args", "__request-permissions", s.which).Run(); err != nil {
		fmt.Printf("  \x1b[33m! couldn't open reminal.app: %v\x1b[0m\n", err)
		return permError
	}
	fmt.Printf("  A dialog will appear — %s.\n", s.how)

	// Poll the daemon until granted (~2 min). It runs the preflight as its own child,
	// so it reports reminal's real grant the moment the user flips the switch.
	deadline := time.Now().Add(120 * time.Second)
	const spin = `-\|/`
	i := 0
	for time.Now().Before(deadline) {
		if mirrorGrantQuery(s.check) == "ok" {
			fmt.Printf("\r  \x1b[32m✓ granted\x1b[0m                                   \n")
			return permNewlyGranted
		}
		fmt.Printf("\r  %c waiting for you to grant it…", spin[i%len(spin)])
		i++
		time.Sleep(400 * time.Millisecond)
	}
	fmt.Printf("\r  \x1b[33m(not granted yet — you can finish it later)\x1b[0m      \n")
	return permTimedOut
}

// RequestPermission runs INSIDE the LaunchServices-launched reminal.app (via the
// hidden `__request-permissions [which]`), so each prompt is attributed to the
// bundle identity (sh.reminal) — the one grant that also covers the background
// daemon's ("+") sessions. `which` selects a single prompt for the guided flow;
// an empty which triggers all three (legacy / non-interactive fallback).
func RequestPermission(which string) error {
	switch which {
	case "screen":
		requestScreenRecording()
	case "accessibility":
		requestAccessibilityGrant()
	case "automation":
		requestAutomationGrant()
	default:
		requestScreenRecording()
		time.Sleep(500 * time.Millisecond)
		requestAccessibilityGrant()
		requestAutomationGrant()
	}
	return nil
}

// requestScreenRecording asks ScreenCaptureKit for shareable content purely to
// surface the Screen Recording (TCC) dialog; the helper blocks until answered.
func requestScreenRecording() {
	if helper, err := captureHelperPath(); err == nil {
		_ = exec.Command(helper, "request").Run()
	}
}

// requestAccessibilityGrant surfaces the Accessibility dialog. The helper stays
// alive polling (~30s) so the dialog isn't dropped if it's queued behind another.
func requestAccessibilityGrant() {
	if helper, err := captureHelperPath(); err == nil {
		_ = exec.Command(helper, "accessibility").Run()
	}
}

// requestAutomationGrant surfaces the "…wants to control System Events" dialog via
// a benign query. Blocks until the user answers.
func requestAutomationGrant() {
	_ = exec.Command("/usr/bin/osascript", "-e",
		`tell application "System Events" to get name of first process`).Run()
}
