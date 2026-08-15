// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode"
)

// OwnerIDPrefix tags a device's public owner id so it's recognisable when
// pasted into `reminal add owner`. The body is the raw Ed25519 public key.
const OwnerIDPrefix = "rmnl_"

// maxLabelLen caps a stored label so a pasted essay can't wreck listings.
const maxLabelLen = 64

// LooksLikeOwnerID reports whether s is shaped like an owner id — used to pick
// the id out of a pasted line even if extra words came along with it.
func LooksLikeOwnerID(s string) bool {
	return strings.HasPrefix(strings.TrimSpace(s), OwnerIDPrefix)
}

// sanitizeLabel trims, strips control characters (a stray newline or C1 control
// would garble the owners listing / terminal), and caps the length. The cap is
// by RUNE, not byte, so a multibyte label (emoji, accents) is never cut in the
// middle of a rune into invalid UTF-8.
func sanitizeLabel(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > maxLabelLen {
		s = strings.TrimSpace(string(r[:maxLabelLen]))
	}
	return s
}

func reminalDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".reminal"), nil
}

func deviceKeyPath() (string, error) {
	dir, err := reminalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "device_ed25519"), nil
}

// ownersDir is where the machine's owner list lives. It's a MACHINE-level trust
// store (who may own this computer), so it belongs in an admin-writable system
// location — that's what makes `add owner` require sudo (an Administrator
// terminal on Windows). Overridable via REMINAL_OWNERS_DIR (used by tests and
// unusual layouts). The per-device key stays in ~/.reminal; only this list is
// system-wide.
func ownersDir() string {
	if d := os.Getenv("REMINAL_OWNERS_DIR"); d != "" {
		return d
	}
	if runtime.GOOS == "windows" {
		if pd := os.Getenv("ProgramData"); pd != "" {
			return filepath.Join(pd, "reminal")
		}
		return `C:\ProgramData\reminal`
	}
	return "/etc/reminal"
}

func ownersPath() (string, error) {
	return filepath.Join(ownersDir(), "owners.json"), nil
}

// loadOrCreateKey returns the Ed25519 private key stored at path, minting one on
// first use. Shared by the device key and the machine key. A present-but-corrupt
// key errors rather than silently regenerating (which would swap the identity and
// orphan its trust relationships); a permission/I-O error is surfaced, never
// clobbered. The mint is an atomic 0600 write so a crash can't leave a partial
// key the corrupt-guard would then wedge on.
func loadOrCreateKey(path string) (ed25519.PrivateKey, error) {
	b, rerr := os.ReadFile(path)
	switch {
	case rerr == nil:
		raw, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
		if derr == nil && len(raw) == ed25519.PrivateKeySize {
			return ed25519.PrivateKey(raw), nil
		}
		return nil, fmt.Errorf("key at %s is corrupt; move it aside to mint a new identity", path)
	case !os.IsNotExist(rerr):
		return nil, rerr
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	enc := base64.StdEncoding.EncodeToString(priv)
	if err := atomicWrite(path, []byte(enc+"\n"), 0o600); err != nil {
		return nil, err
	}
	return priv, nil
}

// loadOrCreateDeviceKey returns this DEVICE's Ed25519 private key. The private
// key never leaves this device — only the public id (see MyOwnerID) is shared.
func loadOrCreateDeviceKey() (ed25519.PrivateKey, error) {
	path, err := deviceKeyPath()
	if err != nil {
		return nil, err
	}
	return loadOrCreateKey(path)
}

func machineKeyPath() (string, error) {
	dir, err := reminalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "machine_ed25519"), nil
}

// loadOrCreateMachineKey returns this MACHINE/agent's Ed25519 signing key — its
// identity for mutual auth during an owner connect (the agent signs the exchange
// so a device can verify it's the real host, not a relay impersonating it).
func loadOrCreateMachineKey() (ed25519.PrivateKey, error) {
	path, err := machineKeyPath()
	if err != nil {
		return nil, err
	}
	return loadOrCreateKey(path)
}

// MachinePub returns this machine's public identity key, minting the keypair on
// first use. Devices pin it (trust-on-first-connect) to detect a relay MITM.
func MachinePub() (ed25519.PublicKey, error) {
	priv, err := loadOrCreateMachineKey()
	if err != nil {
		return nil, err
	}
	return priv.Public().(ed25519.PublicKey), nil
}

// ownerID encodes a public key as a copy-paste-able owner id.
func ownerID(pub ed25519.PublicKey) string {
	return OwnerIDPrefix + base64.RawURLEncoding.EncodeToString(pub)
}

// parseOwnerID decodes an owner id back into a public key, tolerating a missing
// prefix and any embedded whitespace — an id can pick up spaces or newlines when
// it wraps across lines and gets copied (it's a 48-char paste). base64 contains
// no whitespace, so stripping it only ever repairs a mangled paste.
func parseOwnerID(id string) (ed25519.PublicKey, error) {
	s := strings.Map(func(r rune) rune {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, id)
	s = strings.TrimPrefix(s, OwnerIDPrefix)
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("not a valid owner id (expected %s…)", OwnerIDPrefix)
	}
	return ed25519.PublicKey(raw), nil
}

// deviceFingerprint is a short, stable handle for a public key — shown in
// `reminal owners` and used as the revoke target.
func deviceFingerprint(pub ed25519.PublicKey) string {
	h := sha256.Sum256(pub)
	return "dv_" + hex.EncodeToString(h[:4])
}

// MyDeviceFingerprint returns this device's short fingerprint (dv_…) — the same
// id shown in `reminal owners`, so a user can tell which enrolled device is this
// one without a label.
func MyDeviceFingerprint() (string, error) {
	priv, err := loadOrCreateDeviceKey()
	if err != nil {
		return "", err
	}
	return deviceFingerprint(priv.Public().(ed25519.PublicKey)), nil
}

// HasDeviceKey reports whether this device already has an identity key (the user
// has set it up as a potential owner), WITHOUT minting one. Used to decide
// whether to try a PIN-free connect before falling back to asking for a PIN.
func HasDeviceKey() bool {
	path, err := deviceKeyPath()
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

// MyOwnerID returns this device's public owner id, minting the device key on
// first call. This is the string you paste into `reminal add owner`.
func MyOwnerID() (string, error) {
	priv, err := loadOrCreateDeviceKey()
	if err != nil {
		return "", err
	}
	return ownerID(priv.Public().(ed25519.PublicKey)), nil
}

// Owner is one enrolled device in a machine's owners.json.
type Owner struct {
	ID      string `json:"id"`     // deviceFingerprint
	Pubkey  string `json:"pubkey"` // full owner id (rmnl_…)
	Label   string `json:"label,omitempty"`
	AddedAt string `json:"added_at"`
}

type ownersFile struct {
	Version int     `json:"version"`
	Owners  []Owner `json:"owners"`
}

func loadOwners() (*ownersFile, error) {
	path, err := ownersPath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &ownersFile{Version: 1}, nil
		}
		return nil, err
	}
	// An empty or whitespace-only file (e.g. an external `touch`, or an
	// interrupted non-atomic write from an older build) is treated as "no
	// owners yet" rather than corrupt — our own writes are atomic and never
	// empty, so this only ever forgives a benign external state.
	if len(bytes.TrimSpace(b)) == 0 {
		return &ownersFile{Version: 1}, nil
	}
	var of ownersFile
	if err := json.Unmarshal(b, &of); err != nil {
		return nil, fmt.Errorf("owners.json is corrupt: %w", err)
	}
	if of.Version == 0 {
		of.Version = 1
	}
	return &of, nil
}

// atomicWrite writes data to path via a UNIQUE temp file in the same directory
// then renames it into place with perm, so a crash or a concurrent reader never
// sees a partial file and two concurrent writers can't interleave.
func atomicWrite(path string, data []byte, perm os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".reminal-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, werr := f.Write(data)
	cerr := f.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(tmp)
		if werr != nil {
			return werr
		}
		return cerr
	}
	if err := os.Chmod(tmp, perm); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// sudoHint annotates a permission error on the owners store with the fix, since
// the store is root-owned and mutations need sudo.
func sudoHint(err error, dir string) error {
	if errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("modifying owners writes %s — re-run with sudo: %w", dir, err)
	}
	return err
}

func saveOwners(of *ownersFile) error {
	dir := ownersDir()
	// 0755: the list is world-READABLE (public keys — any user's agent reads it
	// to verify owners) but root-WRITABLE (the sudo gate).
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return sudoHint(err, dir)
	}
	path, err := ownersPath()
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(of, "", "  ")
	if err != nil {
		return err
	}
	if err := atomicWrite(path, append(b, '\n'), 0o644); err != nil {
		return sudoHint(err, dir)
	}
	return nil
}

// matchOwners resolves a target to owner indices. An exact fingerprint match is
// unique and wins; otherwise every owner with that exact label matches (so a
// duplicate label surfaces as ambiguous instead of silently hitting the first).
func matchOwners(of *ownersFile, target string) []int {
	target = strings.TrimSpace(target)
	// If target is a full owner id (rmnl_…), canonicalise it so it matches the
	// stored Pubkey even if the paste was wrapped or whitespaced — you enroll a
	// device by its full id, so you must be able to revoke/rename it by that same
	// id, not only by the short fingerprint or label.
	canonical := ""
	if pub, err := parseOwnerID(target); err == nil {
		canonical = ownerID(pub)
	}
	var byLabel []int
	for i := range of.Owners {
		if of.Owners[i].ID == target {
			return []int{i}
		}
		if canonical != "" && of.Owners[i].Pubkey == canonical {
			return []int{i}
		}
		if of.Owners[i].Label != "" && of.Owners[i].Label == target {
			byLabel = append(byLabel, i)
		}
	}
	return byLabel
}

// AddResult reports what AddOwner did.
type AddResult int

const (
	OwnerAdded     AddResult = iota // new device enrolled
	OwnerRelabeled                  // already an owner; label updated
	OwnerUnchanged                  // already an owner; nothing changed
)

// AddOwner records a device's public id as an owner of this machine. Devices are
// keyed by their public key: adding the same device again is idempotent, and
// passing a new label updates it (OwnerRelabeled) rather than duplicating.
func AddOwner(id, label string) (Owner, AddResult, error) {
	pub, err := parseOwnerID(id)
	if err != nil {
		return Owner{}, OwnerUnchanged, err
	}
	label = sanitizeLabel(label)
	of, err := loadOwners()
	if err != nil {
		return Owner{}, OwnerUnchanged, err
	}
	pk := ownerID(pub)
	for i := range of.Owners {
		if of.Owners[i].Pubkey == pk {
			if label != "" && label != of.Owners[i].Label {
				of.Owners[i].Label = label
				if err := saveOwners(of); err != nil {
					return Owner{}, OwnerUnchanged, err
				}
				return of.Owners[i], OwnerRelabeled, nil
			}
			return of.Owners[i], OwnerUnchanged, nil
		}
	}
	o := Owner{
		ID:      deviceFingerprint(pub),
		Pubkey:  pk,
		Label:   label,
		AddedAt: time.Now().UTC().Format(time.RFC3339),
	}
	of.Owners = append(of.Owners, o)
	if err := saveOwners(of); err != nil {
		return Owner{}, OwnerUnchanged, err
	}
	return o, OwnerAdded, nil
}

// RenameOwner changes an owner's label, matched by fingerprint, full owner id,
// or current label.
// The int is the number of devices that matched: 0 = none, 1 = renamed, >1 =
// ambiguous (nothing changed — caller should ask the user to target by id).
func RenameOwner(target, newLabel string) (Owner, int, error) {
	newLabel = sanitizeLabel(newLabel)
	of, err := loadOwners()
	if err != nil {
		return Owner{}, 0, err
	}
	m := matchOwners(of, target)
	if len(m) != 1 {
		return Owner{}, len(m), nil
	}
	of.Owners[m[0]].Label = newLabel
	if err := saveOwners(of); err != nil {
		return Owner{}, 0, err
	}
	return of.Owners[m[0]], 1, nil
}

// RemoveOwner drops an owner by fingerprint, full owner id, or label. The int is
// the match count with the same meaning as RenameOwner (>1 = ambiguous, nothing
// removed).
func RemoveOwner(target string) (Owner, int, error) {
	of, err := loadOwners()
	if err != nil {
		return Owner{}, 0, err
	}
	m := matchOwners(of, target)
	if len(m) != 1 {
		return Owner{}, len(m), nil
	}
	idx := m[0]
	o := of.Owners[idx]
	of.Owners = append(of.Owners[:idx], of.Owners[idx+1:]...)
	if err := saveOwners(of); err != nil {
		return Owner{}, 0, err
	}
	return o, 1, nil
}

// OwnersWithLabel returns the fingerprints of owners carrying an exact label —
// used to show the choices when a label is ambiguous.
func OwnersWithLabel(label string) []string {
	of, err := loadOwners()
	if err != nil {
		return nil
	}
	var ids []string
	for _, o := range of.Owners {
		if o.Label == label {
			ids = append(ids, o.ID)
		}
	}
	return ids
}

// IsOwner reports whether a device public key is enrolled as an owner of this
// machine — the check the agent runs during a PIN-free (owner) connect.
func IsOwner(pub ed25519.PublicKey) (bool, error) {
	of, err := loadOwners()
	if err != nil {
		return false, err
	}
	id := ownerID(pub)
	found := false
	for _, o := range of.Owners {
		if o.Pubkey == id {
			found = true
			break
		}
	}
	if !found {
		return false, nil
	}
	// An enrolled device that has self-revoked is no longer an owner until it's
	// restored — the tombstone (agent-writable) overrides owners.json.
	revoked, err := IsRevoked(pub)
	if err != nil {
		return false, err
	}
	return !revoked, nil
}

// ListOwners returns the machine's enrolled owner devices.
func ListOwners() ([]Owner, error) {
	of, err := loadOwners()
	if err != nil {
		return nil, err
	}
	return of.Owners, nil
}
