// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/reminal/reminal/internal/client"
	"github.com/reminal/reminal/internal/protocol"
)

// runMachines lists every machine this device owns and the live sessions running
// on each, or renames one.
// Usage: reminal machines [list | rename <id|name> <new-name>]
func runMachines(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "list":
			// fall through to the listing below
		case "rename":
			if len(args) < 3 {
				return fmt.Errorf("usage: reminal machines rename <id|name> <new-name>")
			}
			target := args[1]
			newName := strings.TrimSpace(strings.Join(args[2:], " "))
			if newName == "" {
				return fmt.Errorf("the new name can't be empty")
			}
			om, err := client.RenameOwnedMachine(target, newName)
			if err != nil {
				return err
			}
			fmt.Printf("  %s Renamed → %s  %s\n", cGreen("✓"), cBold(om.Name), cDim("("+client.ShortMachineID(om.Key)+")"))
			return nil
		default:
			return fmt.Errorf("unknown: reminal machines [list | rename <id|name> <new-name>]")
		}
	}
	return listMachines()
}

type machineResult struct {
	m    client.OwnedMachine
	resp protocol.DirResponse
	err  error
}

func listMachines() error {
	machines, err := client.ListOwnedMachines()
	if err != nil {
		return err
	}
	// The machine we're running on always appears — you own the machine you're
	// sitting at, so it's served straight from the local registry (no relay, no
	// ownership handshake). Split it out from the enrolled machines, which are
	// reached over their directory channels.
	localKey, _ := client.MachinePub()
	var remote []client.OwnedMachine
	localOM := client.OwnedMachine{Key: localKey}
	for _, m := range machines {
		if localKey != nil && m.Key.Equal(localKey) {
			localOM = m // reuse its stored name/label
			continue
		}
		remote = append(remote, m)
	}

	// Reach every enrolled machine's directory channel in parallel — one
	// slow/offline machine shouldn't hold up the rest.
	remoteResults := make([]machineResult, len(remote))
	var wg sync.WaitGroup
	for i, m := range remote {
		wg.Add(1)
		go func(i int, m client.OwnedMachine) {
			defer wg.Done()
			resp, qerr := client.QueryDirectory(m.Key, client.DirectoryTimeout)
			remoteResults[i] = machineResult{m: m, resp: resp, err: qerr}
		}(i, m)
	}
	wg.Wait()

	// Local machine first (from the registry), then the remote ones.
	var results []machineResult
	if localKey != nil {
		results = append(results, machineResult{m: localOM, resp: client.LocalDirectory()})
	}
	results = append(results, remoteResults...)

	online := 0
	for _, r := range results {
		if r.err == nil {
			online++
		}
	}

	fmt.Printf("  %s\n\n", cBold(fmt.Sprintf("Machines this device owns (%d) — %d online", len(results), online)))
	for _, r := range results {
		printMachine(r, localKey != nil && r.m.Key.Equal(localKey))
		fmt.Println()
	}
	if len(remote) == 0 {
		fmt.Println("  " + cDim("Just this machine so far. To reach another here, run ") + cBold("reminal own"))
		fmt.Println("  " + cDim("on it, then ") + cBold("sudo reminal add owner <id>") + cDim(" — it'll show up once you owner-connect."))
	}
	return nil
}

func printMachine(r machineResult, isLocal bool) {
	// Prefer the user's name, then the machine's reported hostname, then the id.
	name := r.m.Name
	if name == "" {
		if r.err == nil && r.resp.Hostname != "" {
			name = r.resp.Hostname
		} else {
			name = client.ShortMachineID(r.m.Key)
		}
	}
	name = cleanTerm(name) // may be the remote's reported hostname
	short := client.ShortMachineID(r.m.Key)
	// Don't repeat the id when it's already standing in for an (unnamed) name.
	idPart := ""
	if name != short {
		idPart = "   " + cDim(short)
	}
	localTag := ""
	if isLocal {
		localTag = "  " + cGreen("[this machine]")
	}

	// A machine that did not answer still has a battery history, and that is
	// exactly when it is worth reading: "was 20% at 3:04pm" is the only thing
	// a dark laptop can tell you.
	batt := batteryLabel(client.ObserveBattery(client.MachineID(r.m.Key), r.resp))
	battPart := ""
	if batt != "" {
		battPart = "  " + batt
	}

	if r.err != nil { // offline — nothing to enumerate
		fmt.Printf("  %s %s%s  %s%s%s\n", cDim("○"), cBold(name), idPart, cRed("(offline)"), battPart, localTag)
		return
	}
	count := ""
	if n := len(r.resp.Sessions); n > 0 {
		count = " " + cDim(fmt.Sprintf("· %d", n))
	}
	fmt.Printf("  %s %s%s%s%s%s\n", cGreen("●"), cBold(name), count, idPart, battPart, localTag)
	if len(r.resp.Sessions) == 0 {
		fmt.Println("      " + cDim("no sessions running"))
		return
	}

	// Shells before port forwards, then least-idle first, so the session you're
	// most likely to want is at the top.
	sess := append([]protocol.DirSession(nil), r.resp.Sessions...)
	sort.SliceStable(sess, func(i, j int) bool {
		pi, pj := sess[i].Kind == "port", sess[j].Kind == "port"
		if pi != pj {
			return !pi // shells first
		}
		return sess[i].IdleSecs < sess[j].IdleSecs
	})

	// Colour breaks tabwriter (it counts escape bytes as width), so pad manually:
	// id (session ids are a fixed 8 chars) + a label column sized to the widest,
	// capped so one very long title can't stretch the whole table.
	const maxLabelCap = 40
	labels := make([]string, len(sess))
	maxLabel := 0
	for i, s := range sess {
		// Labels come from a remote machine — sanitize before printing so a
		// hostile title can't inject terminal escapes.
		labels[i] = truncate(cleanTerm(sessionLabel(s)), maxLabelCap)
		if w := visLen(labels[i]); w > maxLabel {
			maxLabel = w
		}
	}
	plain := func(x string) string { return x }
	for i, s := range sess {
		fmt.Printf("      %s  %s  %s\n",
			padCol(s.ID, 8, cBold),
			padCol(labels[i], maxLabel, plain),
			cDim(sessionMeta(s)))
	}
}

// truncate shortens s to at most n runes, appending an ellipsis when it cuts.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// sessionLabel picks the most recognisable one-liner for a session.
func sessionLabel(s protocol.DirSession) string {
	if s.Kind == "port" {
		return fmt.Sprintf("port :%d", s.Port)
	}
	switch {
	case s.Name != "":
		return s.Name
	case s.Title != "":
		return s.Title
	case s.Cwd != "":
		return abbrevHome(s.Cwd)
	case s.Headless:
		return "background shell"
	default:
		return "shell"
	}
}

// sessionMeta is the trailing "2 viewers · idle 3m" column.
func sessionMeta(s protocol.DirSession) string {
	var parts []string
	if s.Viewers > 0 {
		unit := "viewer"
		if s.Viewers != 1 {
			unit += "s"
		}
		parts = append(parts, fmt.Sprintf("%d %s", s.Viewers, unit))
	}
	if s.IdleSecs > 0 {
		parts = append(parts, "idle "+humanShort(time.Duration(s.IdleSecs)*time.Second))
	}
	return strings.Join(parts, "  ")
}

// batteryLabel renders a machine's power state for the machine header, or ""
// when there is nothing to show — a desktop, or a machine we have never seen a
// reading from. Absence is deliberate: no battery, no text.
//
//	62% · 2h 14m left
//	41% charging · 1h 12m to full
//	100% charged
//	was 20% at 3:04pm · 1h 2m left then
//
// Plain words, no glyphs: this sits in a terminal next to hostnames and
// session ids, and colour already carries the urgency. The stale form is the
// reason the feature exists — a machine that has gone dark cannot tell you
// anything, so the last thing it said, and when, is the whole answer.
func batteryLabel(b *client.BatterySnapshot) string {
	if b == nil {
		return ""
	}
	dur := durLabel(b.Mins)
	if b.Stale {
		out := fmt.Sprintf("was %d%% %s", b.Pct, whenLabel(b.At))
		// Quoted as of that moment, not extrapolated to now: we have no idea
		// what the machine did after it stopped answering.
		if dur != "" && b.State == "discharging" {
			out += " · " + dur + " left then"
		}
		return cDim(out)
	}
	switch b.State {
	case "charging":
		out := fmt.Sprintf("%d%% charging", b.Pct)
		if dur != "" {
			out += " · " + dur + " to full"
		}
		return cGreen(out)
	case "charged":
		return cDim(fmt.Sprintf("%d%% charged", b.Pct))
	default:
		out := fmt.Sprintf("%d%%", b.Pct)
		if dur != "" {
			out += " · " + dur + " left"
		}
		// Running out is the one state worth interrupting the eye for.
		if b.Pct <= 10 {
			return cRed(out)
		}
		return cDim(out)
	}
}

// durLabel turns minutes into "2h 5m" / "45m". 0 means the OS declined to
// estimate, which is common right after plugging in or unplugging.
func durLabel(mins int) string {
	if mins <= 0 {
		return ""
	}
	if mins < 60 {
		return fmt.Sprintf("%dm", mins)
	}
	h, m := mins/60, mins%60
	if m == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh %dm", h, m)
}

// whenLabel says when a stale reading was taken: a clock time for today, and a
// date once it is older than that, because "was 20% at 3:04pm" is useless if
// you cannot tell which day.
func whenLabel(t time.Time) string { return whenLabelAt(t, time.Now()) }

// whenLabelAt takes "now" so the wording can be tested without depending on
// the wall clock or the runner's timezone — the first version of this was
// exercised with a hardcoded 3:04pm that read as "just now" on a CI box whose
// clock had not reached 3pm yet.
func whenLabelAt(t, now time.Time) string {
	if t.IsZero() {
		return "at an unknown time"
	}
	switch {
	case !t.Before(now.Add(-time.Minute)):
		// Also catches a timestamp slightly in the future, which a backwards
		// NTP correction between writing and reading can produce. "Just now"
		// is the honest reading; a future clock time would not be.
		return "just now"
	case sameDay(t, now):
		return "at " + t.Format("3:04pm")
	case sameDay(t, now.AddDate(0, 0, -1)):
		return "yesterday " + t.Format("3:04pm")
	default:
		return "on " + t.Format("Jan 2, 3:04pm")
	}
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}
