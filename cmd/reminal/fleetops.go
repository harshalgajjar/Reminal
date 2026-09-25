// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"crypto/ed25519"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"reminal/internal/client"
	"reminal/internal/updater"
)

// restartOpTimeout bounds a remote restart: dial + owner handshake + the host
// poking each of its sessions over their control sockets. Roomier than a plain
// query because the host does real work before it answers.
const restartOpTimeout = 45 * time.Second

// plural is the "s" on a count, so fleet output reads as English.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// machineScope is how a fleet verb is aimed: this machine (the default), one
// named machine, or every machine you own.
type machineScope struct {
	selector string // --machine <id|name>
	allOwned bool   // --all-owned-machines
}

func (s machineScope) remote() bool { return s.allOwned || strings.TrimSpace(s.selector) != "" }

// parseMachineScope reads the machine-targeting flags shared by upgrade and
// restart, matching the --machine spellings new/kill/stop already accept so the
// fleet verbs feel like the session verbs.
func parseMachineScope(args []string) (machineScope, error) {
	return parseMachineScopeWith(args, false)
}

// parseMachineScopeStrict is parseMachineScope for a verb with no flags of
// its own: anything unknown is refused rather than ignored. The verbs this
// scopes act the moment they run — "upgrade --help" once upgraded a machine
// whose owner only wanted the flags.
func parseMachineScopeStrict(args []string) (machineScope, error) {
	return parseMachineScopeWith(args, true)
}

func parseMachineScopeWith(args []string, strict bool) (machineScope, error) {
	var sc machineScope
	seen := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-h" || a == "--help" || a == "help":
			return sc, fmt.Errorf("usage: [--machine <id|name>] [--all-owned-machines]")
		case a == "--all-owned-machines" || a == "--all-owned":
			sc.allOwned = true
		case a == "--machine" || a == "-machine" || a == "-m":
			seen = true
			// Guard against swallowing a following flag as the value, the same
			// way the session verbs do.
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				sc.selector = args[i+1]
				i++
			}
		case strings.HasPrefix(a, "--machine="):
			seen = true
			sc.selector = strings.TrimPrefix(a, "--machine=")
		default:
			if strict {
				return sc, fmt.Errorf("usage: [--machine <id|name>] [--all-owned-machines] — not %q", a)
			}
		}
	}
	if seen && strings.TrimSpace(sc.selector) == "" {
		return sc, fmt.Errorf("--machine needs a machine name or id (see reminal machines)")
	}
	if sc.allOwned && strings.TrimSpace(sc.selector) != "" {
		return sc, fmt.Errorf("--machine and --all-owned-machines cannot be combined")
	}
	return sc, nil
}

// fleetResult is one machine's outcome. The fan-out collects these rather than
// letting N machines interleave their narration into one terminal.
type fleetResult struct {
	label   string
	detail  string
	err     error
	skipped bool
}

// runFleetOp applies one operation to every machine this device owns.
//
// Remotes run in parallel — they are independent, and a fleet upgrade that took
// each machine in turn would take as long as the slowest chain. THIS machine
// runs LAST and alone: upgrading or restarting ourselves re-execs the very
// process running the fan-out, so anything scheduled after it would never run.
// A machine that is offline is reported as skipped, never silently dropped: the
// point of a fleet command is knowing what it did and did not reach.
func runFleetOp(verb string, remote func(client.FleetMachine) (string, error), local func() (string, error)) error {
	fleet, err := client.CollectFleet("")
	if err != nil {
		return err
	}
	var localM *client.FleetMachine
	var remotes []client.FleetMachine
	for i := range fleet {
		if fleet[i].Local {
			localM = &fleet[i]
			continue
		}
		remotes = append(remotes, fleet[i])
	}

	results := make([]fleetResult, len(remotes))
	var wg sync.WaitGroup
	for i := range remotes {
		m := remotes[i]
		label := fleetLabel(m)
		if !m.Online {
			results[i] = fleetResult{label: label, detail: "offline — skipped", skipped: true}
			continue
		}
		wg.Add(1)
		go func(idx int, m client.FleetMachine, label string) {
			defer wg.Done()
			detail, err := remote(m)
			results[idx] = fleetResult{label: label, detail: detail, err: err}
		}(i, m, label)
	}
	if len(remotes) > 0 {
		fmt.Printf("\n  %s %d machine%s…\n\n", verb, len(remotes), plural(len(remotes)))
	}
	wg.Wait()

	sort.Slice(results, func(a, b int) bool { return results[a].label < results[b].label })
	for _, r := range results {
		printFleetResult(r)
	}

	// This machine last — see the doc comment.
	if localM != nil && local != nil {
		detail, lerr := local()
		printFleetResult(fleetResult{label: fleetLabel(*localM) + " (this machine)", detail: detail, err: lerr})
		if lerr != nil {
			return fmt.Errorf("%s failed on this machine: %w", strings.ToLower(verb), lerr)
		}
	}
	fmt.Println()

	var failed int
	for _, r := range results {
		if r.err != nil {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d machine%s failed", failed, plural(failed))
	}
	return nil
}

func printFleetResult(r fleetResult) {
	switch {
	case r.err != nil:
		fmt.Printf("  %s %-24s %s\n", cRed("✗"), r.label, r.err)
	case r.skipped:
		fmt.Printf("  %s %-24s %s\n", cDim("·"), r.label, r.detail)
	default:
		fmt.Printf("  %s %-24s %s\n", cGreen("✓"), r.label, r.detail)
	}
}

func fleetLabel(m client.FleetMachine) string {
	if strings.TrimSpace(m.Name) != "" {
		return m.Name
	}
	if strings.TrimSpace(m.Hostname) != "" {
		return m.Hostname
	}
	return m.ShortID
}

// upgradeOutcome turns the host's last narrated step into a RESULT, not an echo
// of the narration — "upgraded — restarting" reads as an ending, where the
// host's own last words ("Restarting this session — reconnecting shortly") read
// as a story cut off mid-sentence.
//
// A host that had nothing to do says so itself, and that wording is kept: it is
// how an up-to-date machine reports being skipped without us having to ask its
// version first.
func upgradeOutcome(st client.UpgradeStep) string {
	detail := strings.TrimSpace(st.Detail)
	switch {
	case st.Error != "":
		return st.Error
	case st.Stage == "done":
		if detail != "" {
			return detail
		}
		return "already on the latest version"
	case st.Stage == "restart":
		// The host publishes its last step BEFORE it re-execs, so reaching this
		// stage is the successful ending rather than a truncated one.
		return "upgraded — restarting"
	case detail != "":
		return detail
	}
	return st.Stage
}

// ---- upgrade ---------------------------------------------------------------

// runUpgradeLocal is the plain local upgrade, factored out so the fleet verbs
// can run it as the last step without duplicating the daemon-reinstall rule.
func runUpgradeLocal() (string, error) {
	updated, err := updater.Upgrade(version)
	if err != nil {
		return "", err
	}
	if !updated {
		return "already on the latest version", nil
	}
	// Re-INSTALL (not just restart) so the service DEFINITION is refreshed, and
	// on darwin always — a bare→bundle migration has no daemon yet.
	if runtime.GOOS == "darwin" || client.DaemonServiceInstalled() {
		_ = client.InstallDaemonService()
	}
	return "upgraded", nil
}

func runUpgradeOnMachineSel(selector string) error {
	om, err := client.ResolveOwnedMachine(selector)
	if err != nil {
		return err
	}
	if local, _ := client.MachinePub(); local != nil && om.Key.Equal(local) {
		detail, err := runUpgradeLocal()
		if err != nil {
			return err
		}
		fmt.Printf("  %s %s — %s\n", cGreen("✓"), cBold("this machine"), detail)
		return nil
	}
	label := machineLabel(om)
	fmt.Printf("\n  Upgrading %s…\n\n", cBold(label))
	// One machine: relay the host's own narration live, the way the Machines
	// panel does. (A fleet run collects outcomes instead — see runFleetOp.)
	last, err := client.UpgradeOnMachine(om.Key, func(st client.UpgradeStep) {
		if d := strings.TrimSpace(st.Detail); d != "" {
			fmt.Printf("    %s\n", d)
		}
	})
	if err != nil {
		return fmt.Errorf("upgrade %s: %w", label, err)
	}
	fmt.Printf("\n  %s %s — %s\n\n", cGreen("✓"), cBold(label), upgradeOutcome(last))
	return nil
}

func runUpgradeAllOwned() error {
	return runFleetOp("Upgrading",
		func(m client.FleetMachine) (string, error) {
			st, err := client.UpgradeOnMachine(m.Key, nil)
			if err != nil {
				return "", err
			}
			return upgradeOutcome(st), nil
		},
		runUpgradeLocal,
	)
}

// ---- restart ---------------------------------------------------------------

func runRestartLocalAll() (string, error) {
	if err := runRestartAll(); err != nil {
		return "", err
	}
	return "sessions restarted", nil
}

func restartRemote(key ed25519.PublicKey, label string) (string, error) {
	count, ok, err := client.RestartOnMachine(key, restartOpTimeout)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("that reminal is too old to restart remotely — upgrade it first")
	}
	if count == 0 {
		return "no sessions to restart", nil
	}
	return fmt.Sprintf("restarted %d session%s", count, plural(count)), nil
}

func runRestartOnMachineSel(selector string) error {
	om, err := client.ResolveOwnedMachine(selector)
	if err != nil {
		return err
	}
	if local, _ := client.MachinePub(); local != nil && om.Key.Equal(local) {
		return runRestartAll()
	}
	label := machineLabel(om)
	detail, err := restartRemote(om.Key, label)
	if err != nil {
		return fmt.Errorf("restart %s: %w", label, err)
	}
	fmt.Printf("  %s %s — %s\n", cGreen("✓"), cBold(label), detail)
	return nil
}

func runRestartAllOwned() error {
	return runFleetOp("Restarting",
		func(m client.FleetMachine) (string, error) { return restartRemote(m.Key, fleetLabel(m)) },
		runRestartLocalAll,
	)
}
