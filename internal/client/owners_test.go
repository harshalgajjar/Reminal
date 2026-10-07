// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func newID(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return ownerID(pub)
}

func isolateHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Keep the machine-level owners store inside the test's temp dir instead of
	// the real /etc/reminal.
	t.Setenv("REMINAL_OWNERS_DIR", filepath.Join(home, "etc-reminal"))
	// What this process remembers about the last HOME's sessions is not this
	// one's.
	forgetSessionCount(t)
	return home
}

// forgetSessionCount drops the cached session count (countRestartableSessions)
// now and when the test ends: a count cached by an earlier test is another
// HOME's, and within its TTL it was served to the next — a restart of one
// seeded session reported zero.
func forgetSessionCount(t *testing.T) {
	t.Helper()
	forget := func() {
		sessCountMu.Lock()
		sessCountVal, sessCountRead = 0, time.Time{}
		sessCountMu.Unlock()
	}
	forget()
	t.Cleanup(forget)
}

// ownersJSONPath is the owners.json location under the current test override.
func ownersJSONPath() string {
	return filepath.Join(os.Getenv("REMINAL_OWNERS_DIR"), "owners.json")
}

func TestOwnerIDRoundTrip(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := ownerID(pub)
	if !strings.HasPrefix(id, OwnerIDPrefix) {
		t.Fatalf("id missing prefix: %q", id)
	}
	got, err := parseOwnerID(id)
	if err != nil || !bytes.Equal(got, pub) {
		t.Fatalf("round trip failed: err=%v", err)
	}
	// Tolerant of surrounding whitespace and a missing prefix (paste slips).
	body := strings.TrimPrefix(id, OwnerIDPrefix)
	got, err = parseOwnerID("  " + body + "\n")
	if err != nil || !bytes.Equal(got, pub) {
		t.Fatalf("tolerant parse failed: err=%v", err)
	}
}

func TestParseOwnerIDToleratesWrappedPaste(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := ownerID(pub)
	body := strings.TrimPrefix(id, OwnerIDPrefix)

	// An id that wrapped across lines mid-string when copied.
	half := len(id) / 2
	wrapped := id[:half] + "\n  " + id[half:] + "\t"
	if got, err := parseOwnerID(wrapped); err != nil || !bytes.Equal(got, pub) {
		t.Fatalf("wrapped-mid paste failed: err=%v", err)
	}
	// A newline landing right after the prefix.
	if got, err := parseOwnerID("rmnl_\n" + body); err != nil || !bytes.Equal(got, pub) {
		t.Fatalf("prefix-split paste failed: err=%v", err)
	}
}

func TestParseOwnerIDRejectsGarbage(t *testing.T) {
	bad := []string{
		"", "   ", OwnerIDPrefix, "rmnl_!!!!", "notanid",
		"rmnl_AAAA",                        // too short for a 32-byte key
		"rmnl_" + strings.Repeat("A", 100), // too long
	}
	for _, b := range bad {
		if _, err := parseOwnerID(b); err == nil {
			t.Errorf("accepted invalid id %q", b)
		}
	}
}

func TestAddOwnerLifecycle(t *testing.T) {
	isolateHome(t)
	id := newID(t)

	o, res, err := AddOwner(id, "iPhone")
	if err != nil || res != OwnerAdded || o.Label != "iPhone" {
		t.Fatalf("add: res=%v label=%q err=%v", res, o.Label, err)
	}
	// Idempotent with no new label.
	if _, res, _ := AddOwner(id, ""); res != OwnerUnchanged {
		t.Fatalf("re-add no-label: want Unchanged, got %v", res)
	}
	// A new label relabels rather than duplicating.
	o, res, _ = AddOwner(id, "Work Phone")
	if res != OwnerRelabeled || o.Label != "Work Phone" {
		t.Fatalf("relabel: res=%v label=%q", res, o.Label)
	}
	if owners, _ := ListOwners(); len(owners) != 1 {
		t.Fatalf("relabel duplicated entry: %d owners", len(owners))
	}
	// Second, distinct device.
	if _, res, _ := AddOwner(newID(t), "Laptop"); res != OwnerAdded {
		t.Fatalf("second device: %v", res)
	}
	if owners, _ := ListOwners(); len(owners) != 2 {
		t.Fatalf("want 2 owners, got %d", len(owners))
	}
}

func TestSameKeyNeverDuplicates(t *testing.T) {
	isolateHome(t)
	id := newID(t)
	for i := 0; i < 5; i++ {
		AddOwner(id, "x")
	}
	if owners, _ := ListOwners(); len(owners) != 1 {
		t.Fatalf("same key added %d times", len(owners))
	}
}

func TestRenameByLabelAndID(t *testing.T) {
	isolateHome(t)
	id := newID(t)
	AddOwner(id, "iPhone")

	o, n, err := RenameOwner("iPhone", "iPad")
	if err != nil || n != 1 || o.Label != "iPad" {
		t.Fatalf("rename by label: n=%d label=%q err=%v", n, o.Label, err)
	}
	o, n, _ = RenameOwner(o.ID, "Tablet")
	if n != 1 || o.Label != "Tablet" {
		t.Fatalf("rename by id: n=%d label=%q", n, o.Label)
	}
	if _, n, _ := RenameOwner("nope", "x"); n != 0 {
		t.Fatalf("rename miss: n=%d", n)
	}
}

func TestAmbiguousLabelIsRefused(t *testing.T) {
	isolateHome(t)
	a := newID(t)
	b := newID(t)
	AddOwner(a, "dup")
	AddOwner(b, "dup")

	if _, n, _ := RenameOwner("dup", "x"); n != 2 {
		t.Fatalf("ambiguous rename: want n=2, got %d", n)
	}
	if _, n, _ := RemoveOwner("dup"); n != 2 {
		t.Fatalf("ambiguous revoke: want n=2, got %d", n)
	}
	// Nothing was changed or removed.
	owners, _ := ListOwners()
	if len(owners) != 2 || owners[0].Label != "dup" || owners[1].Label != "dup" {
		t.Fatalf("ambiguous op mutated state: %+v", owners)
	}
	if got := OwnersWithLabel("dup"); len(got) != 2 {
		t.Fatalf("OwnersWithLabel: %v", got)
	}
	// Targeting by unique id still works.
	if _, n, _ := RemoveOwner(owners[0].ID); n != 1 {
		t.Fatalf("revoke by id: n=%d", n)
	}
	if owners, _ := ListOwners(); len(owners) != 1 {
		t.Fatalf("want 1 after id revoke, got %d", len(owners))
	}
}

func TestRemoveOwner(t *testing.T) {
	isolateHome(t)
	id := newID(t)
	AddOwner(id, "iPhone")
	o, n, err := RemoveOwner("iPhone")
	if err != nil || n != 1 || o.Label != "iPhone" {
		t.Fatalf("revoke: n=%d err=%v", n, err)
	}
	if owners, _ := ListOwners(); len(owners) != 0 {
		t.Fatalf("want empty after revoke, got %d", len(owners))
	}
}

func TestLabelSanitized(t *testing.T) {
	isolateHome(t)
	// Control chars (tab, newline) stripped; surrounding whitespace trimmed.
	o, _, _ := AddOwner(newID(t), "  spaced\tname\n")
	if o.Label != "spacedname" {
		t.Fatalf("label not sanitized: %q, want %q", o.Label, "spacedname")
	}
	// Over-long labels are capped.
	o, _, _ = AddOwner(newID(t), strings.Repeat("z", 200))
	if len(o.Label) > maxLabelLen {
		t.Fatalf("label not capped: len=%d", len(o.Label))
	}
}

func TestLabelSanitizeUnicode(t *testing.T) {
	isolateHome(t)
	// A multibyte label over the cap must stay valid UTF-8 (no mid-rune cut)
	// and be capped by rune count.
	o, _, _ := AddOwner(newID(t), strings.Repeat("é", 100)) // 'é' is 2 bytes
	if !utf8.ValidString(o.Label) {
		t.Fatalf("label not valid UTF-8 after cap: %q", o.Label)
	}
	if n := utf8.RuneCountInString(o.Label); n > maxLabelLen {
		t.Fatalf("label rune count %d exceeds cap %d", n, maxLabelLen)
	}
	// C1 controls (NEL U+0085, U+009F) are stripped just like C0.
	o, _, _ = AddOwner(newID(t), "abc")
	if o.Label != "abc" {
		t.Fatalf("C1 controls not stripped: %q", o.Label)
	}
}

func TestPersistenceAndPerms(t *testing.T) {
	isolateHome(t)
	AddOwner(newID(t), "iPhone")

	// Reload from disk (fresh call re-reads the file).
	owners, err := ListOwners()
	if err != nil || len(owners) != 1 || owners[0].Label != "iPhone" {
		t.Fatalf("persistence: %+v err=%v", owners, err)
	}
	fi, err := os.Stat(ownersJSONPath())
	if err != nil {
		t.Fatal(err)
	}
	// World-readable (public keys), root-writable — the sudo gate.
	if perm := fi.Mode().Perm(); perm != 0o644 {
		t.Fatalf("owners.json perms = %o, want 644", perm)
	}
}

func TestCorruptOwnersFile(t *testing.T) {
	isolateHome(t)
	dir := os.Getenv("REMINAL_OWNERS_DIR")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "owners.json"), []byte("{ not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ListOwners(); err == nil {
		t.Fatal("expected error on corrupt owners.json")
	}
}

func TestEmptyOwnersFileTreatedAsFresh(t *testing.T) {
	isolateHome(t)
	dir := os.Getenv("REMINAL_OWNERS_DIR")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "owners.json"), []byte("  \n\t"), 0o644); err != nil {
		t.Fatal(err)
	}
	owners, err := ListOwners()
	if err != nil {
		t.Fatalf("empty file should be treated as fresh, got err: %v", err)
	}
	if len(owners) != 0 {
		t.Fatalf("want 0 owners, got %d", len(owners))
	}
	// And a subsequent add succeeds (overwrites the empty file cleanly).
	if _, res, err := AddOwner(newID(t), "iPhone"); err != nil || res != OwnerAdded {
		t.Fatalf("add onto empty file: res=%v err=%v", res, err)
	}
}

func TestSaveLeavesNoStrayTemp(t *testing.T) {
	isolateHome(t)
	AddOwner(newID(t), "a")
	AddOwner(newID(t), "b")
	entries, err := os.ReadDir(os.Getenv("REMINAL_OWNERS_DIR"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("stray temp file left behind: %s", e.Name())
		}
	}
	// Data still intact after multiple atomic saves.
	if owners, _ := ListOwners(); len(owners) != 2 {
		t.Fatalf("want 2 owners after saves, got %d", len(owners))
	}
}

func TestAddOwnerRequiresWritableStore(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses the permission check")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	// A read-only parent so creating the owners dir inside it is denied —
	// simulating /etc/reminal without sudo.
	ro := filepath.Join(home, "readonly")
	if err := os.MkdirAll(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REMINAL_OWNERS_DIR", filepath.Join(ro, "reminal"))

	_, _, err := AddOwner(newID(t), "iPhone")
	if err == nil {
		t.Fatal("expected a permission error writing the owners store")
	}
	if !strings.Contains(err.Error(), "sudo") {
		t.Fatalf("error should hint at sudo, got: %v", err)
	}
}

func TestDeviceKeyStableAndPrivate(t *testing.T) {
	home := isolateHome(t)
	id1, err := MyOwnerID()
	if err != nil {
		t.Fatal(err)
	}
	id2, _ := MyOwnerID()
	if id1 != id2 {
		t.Fatalf("device id not stable: %q vs %q", id1, id2)
	}
	fi, err := os.Stat(filepath.Join(home, ".reminal", "device_ed25519"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("device key perms = %o, want 600", perm)
	}
	// The key write is atomic: no stray temp file left behind.
	entries, _ := os.ReadDir(filepath.Join(home, ".reminal"))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("stray temp after key mint: %s", e.Name())
		}
	}
}

func TestDeviceKeyCorruptNotClobbered(t *testing.T) {
	home := isolateHome(t)
	dir := filepath.Join(home, ".reminal")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "device_ed25519")
	const corrupt = "garbage-not-a-key"
	if err := os.WriteFile(path, []byte(corrupt), 0o600); err != nil {
		t.Fatal(err)
	}
	// A corrupt key must error, not silently mint a new identity.
	if _, err := MyOwnerID(); err == nil {
		t.Fatal("expected error on corrupt device key, got silent regeneration")
	}
	// And the corrupt file must be left intact (not overwritten).
	b, _ := os.ReadFile(path)
	if string(b) != corrupt {
		t.Fatalf("corrupt key was clobbered: %q", b)
	}
}

func TestMachineKeyStableAndPrivate(t *testing.T) {
	home := isolateHome(t)
	p1, err := MachinePub()
	if err != nil {
		t.Fatal(err)
	}
	p2, _ := MachinePub()
	if !bytes.Equal(p1, p2) {
		t.Fatal("machine key not stable across calls")
	}
	if len(p1) != ed25519.PublicKeySize {
		t.Fatalf("machine pubkey wrong size: %d", len(p1))
	}
	// Distinct from the device key (different files, different identities).
	dev, _ := MyOwnerID()
	if ownerID(p1) == dev {
		t.Fatal("machine key must differ from device key")
	}
	fi, err := os.Stat(filepath.Join(home, ".reminal", "machine_ed25519"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("machine key perms = %o, want 600", perm)
	}
}

func TestMachineKeyCorruptNotClobbered(t *testing.T) {
	home := isolateHome(t)
	dir := filepath.Join(home, ".reminal")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "machine_ed25519")
	const corrupt = "not-a-key"
	if err := os.WriteFile(path, []byte(corrupt), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := MachinePub(); err == nil {
		t.Fatal("expected error on corrupt machine key, got silent regeneration")
	}
	b, _ := os.ReadFile(path)
	if string(b) != corrupt {
		t.Fatalf("corrupt machine key was clobbered: %q", b)
	}
}

func TestLooksLikeOwnerID(t *testing.T) {
	if !LooksLikeOwnerID("  rmnl_abc ") {
		t.Fatal("should recognize an rmnl_ token with surrounding space")
	}
	if LooksLikeOwnerID("sudo") || LooksLikeOwnerID("") {
		t.Fatal("false positive")
	}
}

// TestRevokeAndRenameByFullOwnerID guards the symmetry bug: you enroll a device
// by its full rmnl_ id, so you must be able to revoke and rename it by that same
// id (not only by the short fingerprint or a label).
func TestRevokeAndRenameByFullOwnerID(t *testing.T) {
	isolateHome(t)
	id, err := MyOwnerID()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := AddOwner(id, "laptop"); err != nil {
		t.Fatal(err)
	}
	// Rename by the full id.
	if _, n, err := RenameOwner(id, "work-laptop"); err != nil || n != 1 {
		t.Fatalf("rename by full id: n=%d err=%v", n, err)
	}
	// A wrapped/whitespaced paste of the same id must still match.
	if _, n, err := RenameOwner("  "+id+"\n", "desk"); err != nil || n != 1 {
		t.Fatalf("rename by whitespaced full id: n=%d err=%v", n, err)
	}
	// Revoke by the full id.
	_, n, err := RemoveOwner(id)
	if err != nil || n != 1 {
		t.Fatalf("revoke by full id: n=%d err=%v", n, err)
	}
	dev, _ := loadOrCreateDeviceKey()
	if ok, _ := IsOwner(dev.Public().(ed25519.PublicKey)); ok {
		t.Fatal("still an owner after revoke by full id")
	}
}

// Another process that mints the machine key first wins: a later mint finds
// its key in place, does not replace it, and hands back that one — so every
// process on the machine signs as the one identity its devices pinned.
func TestMintedKeyNeverReplacesAnother(t *testing.T) {
	path := filepath.Join(t.TempDir(), "machine_ed25519")
	won, err := createKeyFile(path, []byte("first\n"))
	if err != nil || !won {
		t.Fatalf("first mint: won %v, %v", won, err)
	}
	won, err = createKeyFile(path, []byte("second\n"))
	if err != nil || won {
		t.Fatalf("second mint: won %v, %v", won, err)
	}
	if b, _ := os.ReadFile(path); string(b) != "first\n" {
		t.Fatalf("the key file holds %q", b)
	}
	if left, _ := filepath.Glob(path + ".new-*"); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".machine_ed25519.new-*")); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}

	// Many minting at once, as separate processes would (no lock shared):
	// one wins, and the file holds exactly what it wrote.
	path = filepath.Join(t.TempDir(), "machine_ed25519")
	var wg sync.WaitGroup
	var mu sync.Mutex
	winners := []string{}
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			data := strings.Repeat(string(rune('a'+i)), 64) + "\n"
			won, err := createKeyFile(path, []byte(data))
			if err != nil {
				t.Error(err)
			}
			if won {
				mu.Lock()
				winners = append(winners, data)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	b, _ := os.ReadFile(path)
	if len(winners) != 1 || string(b) != winners[0] {
		t.Fatalf("%d winners; the file holds %q", len(winners), b)
	}
}
