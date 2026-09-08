// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/reminal/reminal/internal/protocol"
)

// Battery history lives on the machine doing the LOOKING, not the one being
// looked at. That is the whole point: the interesting question is "what was
// the laptop at when it went dark?", and by then the laptop is not answering
// anything. So every reading an owner sees is written down here, and when a
// machine stops replying the last one is what gets shown, with the time it was
// taken.
//
// The relay cannot help with this even in principle — it routes ciphertext and
// has no idea what a battery is — so the observer is the only place the memory
// can live.

// batteryHistoryFile is the on-disk cache, next to settings.json.
const batteryHistoryFile = "battery-history.json"

// batteryHistoryMax bounds the file for someone who has enrolled and retired a
// lot of machines. Oldest entries fall off first.
const batteryHistoryMax = 256

// batteryRewriteAfter is how stale a stored reading may get before an
// unchanged one is written again. Without it, a viewer polling the fleet would
// rewrite the file every few seconds for no new information.
const batteryRewriteAfter = 2 * time.Minute

// BatterySnapshot is a reading plus when it was taken. Stale marks one that
// came from history rather than from the machine just now — the difference
// between "is at 20%" and "was at 20% at 3pm".
type BatterySnapshot struct {
	Pct   int       `json:"pct"`
	State string    `json:"state,omitempty"`
	Mins  int       `json:"mins,omitempty"`
	At    time.Time `json:"at"`
	Stale bool      `json:"-"`
}

// Empty reports whether the estimate is worth rendering as a time.
func (s *BatterySnapshot) Empty() bool { return s == nil }

type batteryHistory struct {
	Machines map[string]BatterySnapshot `json:"machines"`
}

func batteryHistoryPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".reminal", batteryHistoryFile), nil
}

func loadBatteryHistory() batteryHistory {
	h := batteryHistory{Machines: map[string]BatterySnapshot{}}
	p, err := batteryHistoryPath()
	if err != nil {
		return h
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return h
	}
	_ = json.Unmarshal(data, &h)
	if h.Machines == nil {
		h.Machines = map[string]BatterySnapshot{}
	}
	return h
}

func saveBatteryHistory(h batteryHistory) {
	if len(h.Machines) > batteryHistoryMax {
		type kv struct {
			k string
			t time.Time
		}
		all := make([]kv, 0, len(h.Machines))
		for k, v := range h.Machines {
			all = append(all, kv{k, v.At})
		}
		sort.Slice(all, func(i, j int) bool { return all[i].t.After(all[j].t) })
		trimmed := make(map[string]BatterySnapshot, batteryHistoryMax)
		for _, e := range all[:batteryHistoryMax] {
			trimmed[e.k] = h.Machines[e.k]
		}
		h.Machines = trimmed
	}
	p, err := batteryHistoryPath()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return
	}
	data, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return
	}
	// Temp + rename, matching settings.go: a concurrent reader (another CLI
	// invocation, the viewer's poll) must never see a half-written file, which
	// would parse as an empty history and silently forget every machine.
	dir := filepath.Dir(p)
	f, err := os.CreateTemp(dir, ".battery-*.tmp")
	if err != nil {
		return
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return
	}
	_ = os.Chmod(tmp, 0o600)
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
	}
}

// ObserveBattery folds one directory reply into the history and returns what
// should be displayed for that machine:
//
//   - the machine reported a battery  → that reading, fresh, and recorded
//   - it did not (offline, or no reply) → the last reading we ever saw, marked
//     stale, with the time it was taken
//   - we have never seen one           → nil, and the caller shows nothing
//
// A desktop therefore renders nothing forever, which is the requirement: no
// battery icon on machines that have no battery.
func ObserveBattery(machineID string, resp protocol.DirResponse) *BatterySnapshot {
	if machineID == "" {
		return nil
	}
	if resp.BatteryPct != nil {
		snap := BatterySnapshot{
			Pct:   *resp.BatteryPct,
			State: resp.BatteryState,
			Mins:  resp.BatteryMins,
			At:    time.Now(),
		}
		recordBattery(machineID, snap)
		return &snap
	}
	h := loadBatteryHistory()
	prev, ok := h.Machines[machineID]
	if !ok {
		return nil
	}
	prev.Stale = true
	return &prev
}

// recordBattery persists a fresh reading, skipping the write when nothing a
// human would notice has changed and the stored entry is still recent.
func recordBattery(machineID string, snap BatterySnapshot) {
	h := loadBatteryHistory()
	if prev, ok := h.Machines[machineID]; ok {
		unchanged := prev.Pct == snap.Pct && prev.State == snap.State
		if unchanged && time.Since(prev.At) < batteryRewriteAfter {
			return
		}
	}
	h.Machines[machineID] = snap
	saveBatteryHistory(h)
}

// ForgetBattery drops a machine's history — called when it is disowned, so a
// retired machine doesn't linger in the file forever.
func ForgetBattery(machineID string) {
	h := loadBatteryHistory()
	if _, ok := h.Machines[machineID]; !ok {
		return
	}
	delete(h.Machines, machineID)
	saveBatteryHistory(h)
}
