// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// owned_machines.json is this device's record of the machines IT owns — the
// machines where someone ran `reminal add owner <this-device-id>`. It's written
// automatically the first time this device successfully owner-connects to a
// machine (we already verify + pin the machine identity key there), so the set
// grows as you enroll. `reminal machines` reads it to list every machine you own
// and, by reaching each one's owner-derived directory channel, the live sessions
// running on it.
//
// Keyed by the machine identity pubkey (base64url) — the same stable key pinned
// in known_machines.json — so it survives session churn (session ids are
// ephemeral; the machine key is not).

// OwnedMachine is one entry: a machine this device owns.
type OwnedMachine struct {
	Key        ed25519.PublicKey // machine identity pubkey (32 bytes)
	Name       string            // user-set label; empty until named
	FirstOwned time.Time
	LastSeen   time.Time // last successful owner-connect
}

// MachineIDPrefix is the human-facing id scheme for a machine identity key,
// mirroring OwnerIDPrefix for devices.
const MachineIDPrefix = "mach_"

// MachineID returns the stable, copy-pasteable id for a machine key.
func MachineID(pub ed25519.PublicKey) string {
	return MachineIDPrefix + base64.RawURLEncoding.EncodeToString(pub)
}

// ShortMachineID is a compact form for tables — the prefix plus the first 8
// id characters, enough to disambiguate by eye without wrapping a terminal.
func ShortMachineID(pub ed25519.PublicKey) string {
	enc := base64.RawURLEncoding.EncodeToString(pub)
	if len(enc) > 8 {
		enc = enc[:8]
	}
	return MachineIDPrefix + enc + "…"
}

type ownedMachineRec struct {
	Name       string    `json:"name,omitempty"`
	FirstOwned time.Time `json:"first_owned"`
	LastSeen   time.Time `json:"last_seen"`
}

func ownedMachinesPath() (string, error) {
	dir, err := reminalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "owned_machines.json"), nil
}

func loadOwnedMachines() (map[string]ownedMachineRec, error) {
	path, err := ownedMachinesPath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]ownedMachineRec{}, nil
		}
		return nil, err
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return map[string]ownedMachineRec{}, nil
	}
	m := map[string]ownedMachineRec{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("owned_machines.json is corrupt: %w", err)
	}
	return m, nil
}

func saveOwnedMachines(m map[string]ownedMachineRec) error {
	dir, err := reminalDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path, err := ownedMachinesPath()
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(path, append(b, '\n'), 0o600)
}

// RecordOwnedMachine notes that this device owns the machine identified by pub,
// stamping FirstOwned on first sight and refreshing LastSeen every time. Called
// best-effort from the owner-connect success path — a write failure must never
// abort an already-authenticated connection, so callers ignore the error.
func RecordOwnedMachine(pub ed25519.PublicKey) error {
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("machine key must be %d bytes, got %d", ed25519.PublicKeySize, len(pub))
	}
	m, err := loadOwnedMachines()
	if err != nil {
		return err
	}
	key := base64.RawURLEncoding.EncodeToString(pub)
	now := time.Now()
	rec, ok := m[key]
	if !ok {
		rec.FirstOwned = now
	}
	rec.LastSeen = now
	m[key] = rec
	return saveOwnedMachines(m)
}

// ListOwnedMachines returns every machine this device owns, most-recently-seen
// first so the machine you touched last sorts to the top.
func ListOwnedMachines() ([]OwnedMachine, error) {
	m, err := loadOwnedMachines()
	if err != nil {
		return nil, err
	}
	out := make([]OwnedMachine, 0, len(m))
	for k, rec := range m {
		raw, derr := base64.RawURLEncoding.DecodeString(k)
		if derr != nil || len(raw) != ed25519.PublicKeySize {
			continue // skip a corrupt key rather than fail the whole listing
		}
		out = append(out, OwnedMachine{
			Key:        ed25519.PublicKey(raw),
			Name:       rec.Name,
			FirstOwned: rec.FirstOwned,
			LastSeen:   rec.LastSeen,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].LastSeen.After(out[j].LastSeen)
	})
	return out, nil
}

// findOwnedMachineKey resolves a user-supplied selector to a stored machine key.
// It matches (in order) an exact name, a full/short mach_ id, or a base64url key
// prefix. Returns the matched key, or an error naming the ambiguity/miss.
func findOwnedMachineKey(selector string) (ed25519.PublicKey, error) {
	sel := strings.TrimSpace(selector)
	if sel == "" {
		return nil, fmt.Errorf("no machine given")
	}
	list, err := ListOwnedMachines()
	if err != nil {
		return nil, err
	}
	// Exact name match wins outright.
	var byName []OwnedMachine
	for _, om := range list {
		if om.Name != "" && strings.EqualFold(om.Name, sel) {
			byName = append(byName, om)
		}
	}
	if len(byName) == 1 {
		return byName[0].Key, nil
	}
	if len(byName) > 1 {
		return nil, fmt.Errorf("more than one machine is named %q — use its mach_ id instead", sel)
	}
	// Otherwise treat it as an id/key prefix (tolerate the mach_ prefix and a
	// trailing … from ShortMachineID).
	needle := strings.TrimPrefix(sel, MachineIDPrefix)
	needle = strings.TrimSuffix(needle, "…")
	// An empty needle (e.g. a bare "mach_" or "…") must match NOTHING — otherwise
	// HasPrefix(key, "") matches every machine and a partial selector would
	// silently rename/resolve an arbitrary one.
	if needle == "" {
		return nil, fmt.Errorf("no owned machine matches %q (see `reminal machines`)", selector)
	}
	var byID []OwnedMachine
	for _, om := range list {
		if strings.HasPrefix(base64.RawURLEncoding.EncodeToString(om.Key), needle) {
			byID = append(byID, om)
		}
	}
	switch len(byID) {
	case 1:
		return byID[0].Key, nil
	case 0:
		return nil, fmt.Errorf("no owned machine matches %q (see `reminal machines`)", selector)
	default:
		return nil, fmt.Errorf("%q matches more than one machine — use a longer id", selector)
	}
}

// ResolveOwnedMachine resolves a selector — a machine name or a mach_ id (or id
// prefix), exactly as shown by `reminal machines` — to the owned machine it
// identifies. The same matching `reminal machines rename` uses.
func ResolveOwnedMachine(selector string) (OwnedMachine, error) {
	key, err := findOwnedMachineKey(selector)
	if err != nil {
		return OwnedMachine{}, err
	}
	list, err := ListOwnedMachines()
	if err != nil {
		return OwnedMachine{}, err
	}
	for _, om := range list {
		if om.Key.Equal(key) {
			return om, nil
		}
	}
	return OwnedMachine{Key: key}, nil
}

// RenameOwnedMachine sets the friendly name of an owned machine, resolving
// selector by name or id/key prefix.
func RenameOwnedMachine(selector, name string) (OwnedMachine, error) {
	key, err := findOwnedMachineKey(selector)
	if err != nil {
		return OwnedMachine{}, err
	}
	label := sanitizeLabel(name)
	if label == "" {
		return OwnedMachine{}, fmt.Errorf("name is empty")
	}
	m, err := loadOwnedMachines()
	if err != nil {
		return OwnedMachine{}, err
	}
	k := base64.RawURLEncoding.EncodeToString(key)
	rec, ok := m[k]
	if !ok {
		return OwnedMachine{}, fmt.Errorf("machine not found")
	}
	rec.Name = label
	m[k] = rec
	if err := saveOwnedMachines(m); err != nil {
		return OwnedMachine{}, err
	}
	return OwnedMachine{Key: key, Name: label, FirstOwned: rec.FirstOwned, LastSeen: rec.LastSeen}, nil
}
