// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"bufio"
	"fmt"
	"os"
	"os/user"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/reminal/reminal/internal/client"
)

// runOwn prints this device's public owner id and the exact command to paste on
// the machine you want to own. The device keypair is minted on first run; the
// private key never leaves this machine.
func runOwn() error {
	id, err := client.MyOwnerID()
	if err != nil {
		return err
	}
	fmt.Println()
	fmt.Println("  " + cBold("This device's owner id") + cDim("  (safe to share — it's a public key)"))
	fmt.Println()
	fmt.Println("    " + cBold(id))
	if fp, err := client.MyDeviceFingerprint(); err == nil {
		fmt.Println()
		fmt.Println("  Shown as " + cGreen(fp) + " in " + cBold("reminal owners") + ".")
	}
	fmt.Println()
	fmt.Println("  " + cDim("On the machine you want to own, paste:"))
	fmt.Println()
	fmt.Println("    " + cBold("sudo reminal add owner "+id))
	fmt.Println()
	return nil
}

// runAddOwner records a pasted owner id in this machine's owners.json.
// Usage: reminal add owner <id> [--label <name>] [-y]
// It's forgiving about paste slips: if you paste the whole suggested line, the
// rmnl_… token is still picked out, and a bare label after the id is captured.
func runAddOwner(args []string) error {
	id, label, yes := parseAddOwnerArgs(args)
	if id == "" {
		return fmt.Errorf("usage: reminal add owner <id> [--label <name>] [-y]")
	}

	// The disclaimer, before anything is written. Skipped with -y, when stdin
	// isn't a terminal (scripts), and when the device is already enrolled —
	// re-adding grants nothing the disclaimer warns about. The label is
	// deliberately NEVER shown here: it's attacker-typed text, and a
	// reassuring name on a security prompt lends it exactly the trust it
	// hasn't earned. The id is the only identity on this screen.
	if !yes && term.IsTerminal(int(os.Stdin.Fd())) && !client.IsEnrolledOwner(id) {
		if !confirmAddOwner(id) {
			fmt.Println("  " + cDim("Cancelled — nothing was changed."))
			return nil
		}
	}

	o, res, err := client.AddOwner(id, label)
	if needsSudoRetry(err) {
		// Writing /etc/reminal needs root — re-run under sudo. The human
		// already answered (or legitimately skipped) the disclaimer in this
		// process, so the child carries -y and never asks twice.
		return sudoReexec("-y")
	}
	if err != nil {
		return err
	}
	name := o.Label
	if name == "" {
		name = o.ID
	}
	switch res {
	case client.OwnerAdded:
		fmt.Printf("  %s Added owner %s  %s\n", cGreen("✓"), cBold(name), cDim("("+o.ID+")"))
	case client.OwnerRelabeled:
		fmt.Printf("  %s Already an owner — relabeled to %s  %s\n", cGreen("✓"), cBold(o.Label), cDim("("+o.ID+")"))
	case client.OwnerUnchanged:
		fmt.Printf("  %s is already an owner  %s\n", cBold(name), cDim("("+o.ID+")"))
		if o.Label != "" {
			fmt.Println("  " + cDim("To relabel it:  reminal owners rename "+o.ID+" <new-name>"))
		}
	}
	// Now that this machine has an owner, make sure it keeps a presence even with
	// no live session — so it stays listable and "+"-spawnable from the owner's
	// phone. Runs here (the context that actually wrote the store, under sudo), so
	// it installs exactly once; the auto-escalating parent doesn't re-run it.
	enableBackgroundHost()
	return nil
}

// confirmAddOwner shows what enrollment actually grants and requires a typed
// "yes". The CLI's usual confirm is Enter-to-continue, but this one hands the
// whole machine over — the default must be cancel, and the answer deliberate.
// The closing lines are the real security work: the plausible attack on this
// command is social engineering ("run this one line"), so the prompt turns
// into a verification step — the enrolling device is showing this same id on
// its own screen (reminal own, or the browser's enroll box).
func confirmAddOwner(id string) bool {
	fmt.Println()
	fmt.Println("  " + cRed("⚠  You are about to hand full control of this computer to that device."))
	fmt.Println()
	fmt.Println("     Once enrolled, the device with id")
	fmt.Println()
	fmt.Println("         " + cBold(id))
	fmt.Println()
	fmt.Println("     can, at any time, without a PIN and without anyone at this")
	fmt.Println("     keyboard approving it:")
	fmt.Println()
	fmt.Println("       •  open new terminal sessions and run any command as " + enrolledAccountName())
	fmt.Println("       •  attach to every existing reminal session on this machine")
	fmt.Println("       •  view the screen and control any window")
	fmt.Println("       •  reach this machine even when idle — enrolling installs a")
	fmt.Println("          background host that keeps it connected")
	fmt.Println()
	fmt.Println("     " + cDim("Check that this id matches the one shown on YOUR device's screen."))
	fmt.Println("     " + cDim("Only enroll a device you hold. Never paste an id someone unknown"))
	fmt.Println("     " + cDim("sent you."))
	fmt.Println()
	fmt.Print("  Type yes to enroll, anything else to cancel: ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.EqualFold(strings.TrimSpace(line), "yes")
}

// enrolledAccountName names the account an owner's sessions would run as —
// the login user, not root: under sudo that's SUDO_USER, never "root" (the
// command normally runs elevated, but sessions don't).
func enrolledAccountName() string {
	if u := os.Getenv("SUDO_USER"); u != "" {
		return u
	}
	if u, err := user.Current(); err == nil && u.Username != "" && u.Username != "root" {
		return u.Username
	}
	return "your user account"
}

// enableBackgroundHost installs (idempotently) the login service that keeps this
// machine's directory host running when idle. Best-effort: enrollment already
// succeeded, so a service hiccup must not fail the command — we just tell the
// user the machine will then only be reachable while a session is up.
func enableBackgroundHost() {
	if err := client.InstallDaemonService(); err != nil {
		fmt.Println("  " + cDim("(background host not enabled: "+err.Error()+")"))
		fmt.Println("  " + cDim("This machine stays reachable to you only while a session is running."))
		return
	}
	fmt.Println("  " + cDim("Background host enabled — this machine stays reachable to you even when idle."))
}

// The background daemon is a PERMANENT local service on every platform now: it is
// the machine's presence + stats layer (it answers `reminal machines` and samples
// the vitals the cards read), and on macOS it also performs the one-grant screen
// capture + input injection. Removing the last owner no longer tears it down —
// the machine should keep reporting its usage even with no owners — so there is
// nothing to do when the last owner is revoked.

// parseAddOwnerArgs pulls the owner id, label, and -y out of `add owner`
// arguments. It picks the rmnl_ token as the id even if extra words came along
// (a stray paste), but only treats trailing words as a bare label when the id
// was typed FIRST — so it never scavenges a label out of a longer pasted line.
// An explicit --label always wins. -y/--yes skips the enrollment disclaimer
// (scripts, and the sudo re-exec of an already-answered prompt).
func parseAddOwnerArgs(args []string) (id, label string, yes bool) {
	var positionals []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--label" && i+1 < len(args):
			label = args[i+1]
			i++
		case strings.HasPrefix(a, "--label="):
			label = strings.TrimPrefix(a, "--label=")
		case a == "-y" || a == "--yes":
			yes = true
		case strings.HasPrefix(a, "-"):
			// ignore stray flags
		default:
			positionals = append(positionals, a)
		}
	}
	idIdx := -1
	for i, p := range positionals {
		if client.LooksLikeOwnerID(p) {
			id, idIdx = p, i
			break
		}
	}
	if id == "" && len(positionals) > 0 {
		id, idIdx = positionals[0], 0
	}
	if label == "" && idIdx == 0 && len(positionals) > 1 {
		label = strings.Join(positionals[1:], " ")
	}
	return id, label, yes
}

// runOwners lists the machine's owner devices, or renames/revokes one.
// Usage: reminal owners [rename <id|label> <new-label> | revoke <id|label>]
func runOwners(args []string) error {
	if len(args) == 0 {
		return listOwners()
	}
	switch args[0] {
	case "list":
		return listOwners()
	case "rename":
		if len(args) < 3 {
			return fmt.Errorf("usage: reminal owners rename <id|label> <new-label>")
		}
		target := args[1]
		newLabel := strings.TrimSpace(strings.Join(args[2:], " "))
		if newLabel == "" {
			return fmt.Errorf("the new label can't be empty")
		}
		o, n, err := client.RenameOwner(target, newLabel)
		if needsSudoRetry(err) {
			return sudoReexec()
		}
		if err != nil {
			return err
		}
		switch {
		case n == 0:
			fmt.Printf("  %s No owner matching %q — run %s to see them.\n", cRed("!"), target, cBold("reminal owners"))
		case n > 1:
			printAmbiguous(target)
		default:
			fmt.Printf("  %s Renamed %s → %s\n", cGreen("✓"), cDim(o.ID), cBold(o.Label))
		}
		return nil

	case "revoke":
		if len(args) < 2 {
			return fmt.Errorf("usage: reminal owners revoke <id|label>")
		}
		target := args[1]
		o, n, err := client.RemoveOwner(target)
		if needsSudoRetry(err) {
			return sudoReexec()
		}
		if err != nil {
			return err
		}
		switch {
		case n == 0:
			fmt.Printf("  %s No owner matching %q — run %s to see them.\n", cRed("!"), target, cBold("reminal owners"))
		case n > 1:
			printAmbiguous(target)
		default:
			name := o.Label
			if name == "" {
				name = o.ID
			}
			fmt.Printf("  %s Revoked %s  %s\n", cGreen("✓"), cBold(name), cDim("("+o.ID+")"))
			// The background daemon stays installed — it is the machine's permanent
			// presence + stats layer, not an ownership-gated feature (see above).
			// But bounce it so it re-keys the machine channel NOW: a revoked device
			// that already completed the owner handshake holds a working channel key
			// until the agent is rebuilt, and the daemon's periodic owner-set recheck
			// would otherwise leave that access open for up to a minute. Best-effort;
			// that recheck is the backstop if the bounce doesn't take.
			_ = client.RestartDaemonService()
		}
		return nil

	case "restore":
		if len(args) < 2 {
			return fmt.Errorf("usage: reminal owners restore <id|label>")
		}
		target := args[1]
		o, n, wasRevoked, err := client.RestoreOwnerTarget(target)
		if err != nil {
			return err
		}
		switch {
		case n == 0:
			fmt.Printf("  %s No owner matching %q — run %s to see them.\n", cRed("!"), target, cBold("reminal owners"))
		case n > 1:
			printAmbiguous(target)
		default:
			name := o.Label
			if name == "" {
				name = o.ID
			}
			if wasRevoked {
				fmt.Printf("  %s Restored %s — PIN-free access re-enabled  %s\n", cGreen("✓"), cBold(name), cDim("("+o.ID+")"))
			} else {
				fmt.Printf("  %s %s wasn't revoked — already active  %s\n", cDim("·"), cBold(name), cDim("("+o.ID+")"))
			}
		}
		return nil

	default:
		return fmt.Errorf("unknown: reminal owners [rename|revoke|restore]")
	}
}

// printAmbiguous tells the user a label matched multiple devices and lists their
// ids so they can target one precisely.
func printAmbiguous(label string) {
	ids := client.OwnersWithLabel(label)
	fmt.Printf("  %d devices are labeled %q — target one by id:\n", len(ids), label)
	for _, id := range ids {
		fmt.Printf("    %s\n", id)
	}
}

func listOwners() error {
	owners, err := client.ListOwners()
	if err != nil {
		return err
	}
	if len(owners) == 0 {
		fmt.Println("  " + cBold("No owner devices yet."))
		fmt.Println("  On a device, run " + cBold("reminal own") + " to get its id, then here:")
		fmt.Println("    " + cBold("sudo reminal add owner <id>"))
		return nil
	}
	revoked, _ := client.RevokedIDs() // best-effort; nil map is fine

	type row struct {
		id, name, added, status string
		unlabeled, revoked      bool
	}
	rows := make([]row, 0, len(owners))
	wID, wName, wAdded := len("ID"), len("NAME"), len("ADDED")
	anyRevoked := false
	for _, o := range owners {
		name := o.Label
		unlabeled := name == ""
		if unlabeled {
			name = "(unlabeled)"
		}
		added := o.AddedAt
		if t, perr := time.Parse(time.RFC3339, o.AddedAt); perr == nil {
			added = t.Local().Format("Jan 2, 2006")
		}
		rv := revoked[o.Pubkey]
		anyRevoked = anyRevoked || rv
		st := "active"
		if rv {
			st = "self-revoked"
		}
		rows = append(rows, row{o.ID, name, added, st, unlabeled, rv})
		wID = maxInt(wID, len(o.ID))
		wName = maxInt(wName, visLen(name))
		wAdded = maxInt(wAdded, len(added))
	}

	fmt.Printf("  %s\n\n", cBold(fmt.Sprintf("Owner devices (%d)", len(owners))))
	fmt.Printf("  %s  %s  %s  %s\n",
		padCol("ID", wID, cDim), padCol("NAME", wName, cDim), padCol("ADDED", wAdded, cDim), cDim("STATUS"))
	for _, r := range rows {
		nameColor := func(x string) string { return x }
		if r.unlabeled {
			nameColor = cDim
		}
		statusCol := cGreen(r.status)
		if r.revoked {
			statusCol = cRed(r.status)
		}
		fmt.Printf("  %s  %s  %s  %s\n",
			padCol(r.id, wID, cBold), padCol(r.name, wName, nameColor), padCol(r.added, wAdded, cDim), statusCol)
	}
	if anyRevoked {
		fmt.Println("\n  " + cRed("A self-revoked device has no PIN-free access until you run:"))
		fmt.Println("    " + cBold("reminal owners restore <id|label>"))
	}
	return nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
