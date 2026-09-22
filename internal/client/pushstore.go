// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// The phones that asked this machine for alerts, and what each one wants to
// hear about. Kept on the machine, not on the relay: the relay is stateless
// for push (it signs and forwards), so it never holds a list of anyone's
// devices, and removing an owner here is enough to silence that owner's phone.

// pushRules is one phone's thresholds for this machine. Each rule is off
// unless its switch is on, so an empty struct asks for nothing.
type pushRules struct {
	CPU     bool `json:"cpu"`
	CPUPct  int  `json:"cpu_pct"`  // alert at or above this utilisation…
	CPULow  int  `json:"cpu_low"`  // …or at or below this one (0 = no lower bound)…
	CPUMins int  `json:"cpu_mins"` // …held for this many minutes
	Battery bool `json:"battery"`
	BatPct  int  `json:"bat_pct"` // alert when discharging at or below this
	BatTime bool `json:"bat_time"`
	BatMins int  `json:"bat_mins"` // alert when the OS estimates this many minutes left, or fewer
	Charger bool `json:"charger"`  // alert on plugged in / unplugged
}

// defaultPushRules is what a phone gets the first time it subscribes: the two
// alerts nobody wants to miss, and the chatty charger one left for the owner
// to turn on.
func defaultPushRules() pushRules {
	return pushRules{CPU: true, CPUPct: 90, CPUMins: 5, Battery: true, BatPct: 20}
}

// clamp keeps rules inside ranges that make sense, whatever the page sent.
func (r pushRules) clamp() pushRules {
	lim := func(v, lo, hi, def int) int {
		if v == 0 {
			return def
		}
		return max(lo, min(hi, v))
	}
	r.CPUPct = lim(r.CPUPct, 10, 100, 90)
	r.CPUMins = lim(r.CPUMins, 1, 120, 5)
	// The lower bound must sit under the upper one, or the machine would be
	// "out of range" at every reading.
	if r.CPULow < 0 || r.CPULow >= r.CPUPct {
		r.CPULow = 0
	}
	r.BatPct = lim(r.BatPct, 1, 99, 20)
	r.BatMins = lim(r.BatMins, 5, 600, 60)
	return r
}

func (r pushRules) any() bool { return r.CPU || r.Battery || r.BatTime || r.Charger }

// pushEntry is one subscribed phone. DevicePub is the owner key that asked,
// so an owner removed from this machine stops getting alerts from it.
type pushEntry struct {
	DevicePub string    `json:"device_pub"` // base64 ed25519
	Sub       pushSub   `json:"sub"`
	Rules     pushRules `json:"rules"`
	Added     time.Time `json:"added"`
}

type pushFile struct {
	Version int         `json:"version"`
	Subs    []pushEntry `json:"subs"`
}

// pushStoreMu serialises read-modify-write of push.json within a process.
// Across processes the write is an atomic rename, and the only writers are
// the directory host (on an owner's request) and the watcher (pruning a dead
// subscription), so the last write winning is acceptable.
var pushStoreMu sync.Mutex

func pushStorePath() (string, error) {
	dir, err := reminalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "push.json"), nil
}

func loadPushSubs() ([]pushEntry, error) {
	path, err := pushStorePath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f pushFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	return f.Subs, nil
}

func savePushSubs(subs []pushEntry) error {
	path, err := pushStorePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(pushFile{Version: 1, Subs: subs}, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// updatePushSubs applies fn to the stored list under the lock and saves it.
func updatePushSubs(fn func([]pushEntry) []pushEntry) error {
	pushStoreMu.Lock()
	defer pushStoreMu.Unlock()
	subs, err := loadPushSubs()
	if err != nil {
		return err
	}
	return savePushSubs(fn(subs))
}

// findPushSub returns the entry for endpoint, if this machine has one.
func findPushSub(endpoint string) (pushEntry, bool) {
	pushStoreMu.Lock()
	defer pushStoreMu.Unlock()
	subs, _ := loadPushSubs()
	for _, s := range subs {
		if s.Sub.Endpoint == endpoint {
			return s, true
		}
	}
	return pushEntry{}, false
}

// upsertPushSub records (or re-records) one phone's subscription and rules.
// Keyed by endpoint: one phone, one entry, whichever owner key it used.
func upsertPushSub(devicePub string, sub pushSub, rules pushRules) error {
	return updatePushSubs(func(subs []pushEntry) []pushEntry {
		for i := range subs {
			if subs[i].Sub.Endpoint == sub.Endpoint {
				subs[i].DevicePub, subs[i].Sub, subs[i].Rules = devicePub, sub, rules
				return subs
			}
		}
		return append(subs, pushEntry{DevicePub: devicePub, Sub: sub, Rules: rules, Added: time.Now()})
	})
}

func removePushSub(endpoint string) error {
	return updatePushSubs(func(subs []pushEntry) []pushEntry {
		out := subs[:0]
		for _, s := range subs {
			if s.Sub.Endpoint != endpoint {
				out = append(out, s)
			}
		}
		return out
	})
}

// stillOwner reports whether the device that subscribed is still enrolled.
// Checked at send time rather than on owner removal, so every removal path
// (CLI, revoke-self, a hand-edited owners.json) silences the phone alike.
func (e pushEntry) stillOwner() bool {
	pub, err := base64.StdEncoding.DecodeString(e.DevicePub)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return false
	}
	ok, err := IsOwner(pub)
	return err == nil && ok
}

// validPushSub rejects anything that could not be a real push subscription
// before it is stored; the relay re-checks the endpoint before it forwards.
func validPushSub(s pushSub) error {
	if !strings.HasPrefix(s.Endpoint, "https://") || len(s.Endpoint) > 1024 {
		return errors.New("not a push endpoint")
	}
	if k, err := b64urlDecode(s.P256dh); err != nil || len(k) != 65 {
		return errors.New("bad subscription key")
	}
	if a, err := b64urlDecode(s.Auth); err != nil || len(a) != 16 {
		return errors.New("bad subscription secret")
	}
	return nil
}
