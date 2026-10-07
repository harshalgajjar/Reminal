// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"reminal/internal/crypto"
)

// Authorising a privileged action cannot rely on the session key or on which
// connection a message arrived over.
//
// Every viewer on a session shares ONE key: a PIN guest can decrypt, forge and
// replay anything an owner sends. And the agent holds a single relay
// connection for all of them, so there is no per-viewer socket to attribute a
// message to. Hiding the button in the UI is therefore exactly as strong as
// not hiding it — the request is one line of JSON either way.
//
// So the request carries its own proof: a signature by a device key that is
// enrolled in this machine's owners store. That is the same key and the same
// verification path a PIN-free owner connect already uses; the only difference
// is what the transcript commits to.

// ownerActionSkew is how far a proof's timestamp may be from our clock. Wide
// enough for a phone with a lazy clock, narrow enough that a captured proof
// stops being interesting quickly. The nonce set is what actually prevents
// replay; this bounds how long we must remember one.
const ownerActionSkew = 2 * time.Minute

// ownerNonceMax caps the replay set. Well above any plausible burst of real
// requests, and the window is short, so eviction cannot make a replay succeed
// in practice — but the bound must exist or a spammer grows it forever.
const ownerNonceMax = 512

type ownerNonces struct {
	mu   sync.Mutex
	seen map[string]time.Time
	// file, when set, keeps the set on disk (the machine channel's, see
	// newMachineAgent): a process restarted — every upgrade restarts the
	// daemon — inside a proof's freshness window must not take a proof the
	// one before it already took. Read and written under ownerNoncesLock.
	file string
}

// ownerNoncesLock serialises the read-modify-write of an ownerNonces file
// between processes: an old daemon and its replacement can overlap.
const ownerNoncesLock = "owner-nonces.lock"

// ownerNoncesFile is where the machine channel keeps the proofs it took.
func ownerNoncesFile() string {
	dir, err := reminalDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "owner-nonces.json")
}

// use records a nonce, reporting "" when it was fresh, or why not. A repeat
// is a replay.
func (n *ownerNonces) use(nonce string, now time.Time) string {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.seen == nil {
		n.seen = make(map[string]time.Time)
	}
	if n.file != "" {
		lock, held, err := tryLockFile(ownerNoncesLock)
		if err != nil || !held {
			// Not taken without the record: a proof taken here and not
			// written down could be taken again after a restart.
			return "this machine is busy; try again"
		}
		defer unlockFile(lock)
		n.loadFile() // what another process (or the one before) took
	}
	// Drop anything older than the window on the way past; a proof that stale
	// is rejected by the skew check anyway, so remembering it buys nothing.
	for k, t := range n.seen {
		if now.Sub(t) > ownerActionSkew*2 {
			delete(n.seen, k)
		}
	}
	if _, dup := n.seen[nonce]; dup {
		return "this owner proof has already been used"
	}
	if len(n.seen) >= ownerNonceMax {
		// Full of live entries: refuse rather than evict. Evicting under
		// pressure is precisely how an attacker would make room for a replay.
		return "this owner proof has already been used"
	}
	n.seen[nonce] = now
	if n.file != "" && !n.saveFile() {
		delete(n.seen, nonce)
		return "this machine could not record the proof; try again"
	}
	return ""
}

// loadFile merges the set on disk into memory, pruning what is past the
// window (it was pruned before it was written, but time has passed since).
func (n *ownerNonces) loadFile() {
	b, err := os.ReadFile(n.file)
	if err != nil {
		return
	}
	var on map[string]int64
	if json.Unmarshal(b, &on) != nil {
		return
	}
	for k, t := range on {
		if _, have := n.seen[k]; !have {
			n.seen[k] = time.Unix(t, 0)
		}
	}
}

// saveFile writes the set (already pruned to the window) whole, atomically.
func (n *ownerNonces) saveFile() bool {
	on := make(map[string]int64, len(n.seen))
	for k, t := range n.seen {
		on[k] = t.Unix()
	}
	b, err := json.Marshal(on)
	if err != nil {
		return false
	}
	if err := os.MkdirAll(filepath.Dir(n.file), 0o700); err != nil {
		return false
	}
	tmp := n.file + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return false
	}
	return os.Rename(tmp, n.file) == nil
}

// ownerProof is what a viewer attaches to a privileged request.
type ownerProof struct {
	DevicePub string `json:"device_pub"` // base64 raw ed25519
	DeviceSig string `json:"device_sig"` // base64 signature over the transcript
	Nonce     string `json:"nonce"`      // base64, viewer-generated, one-time
	Unix      int64  `json:"unix"`       // seconds; bounded by ownerActionSkew
}

// verifyOwnerAction checks that a request was authorised by a device enrolled
// as an owner of THIS machine, for THIS session and THIS action, once.
// Returns a reason on failure — the caller reports it, because a viewer that
// legitimately cannot do this deserves to know why rather than watching a
// button do nothing.
func (a *Agent) verifyOwnerAction(p ownerProof, action string) string {
	pub, err := base64.StdEncoding.DecodeString(p.DevicePub)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return "this action needs an owner device; none was presented"
	}
	sig, err := base64.StdEncoding.DecodeString(p.DeviceSig)
	if err != nil {
		return "the owner signature could not be read"
	}
	nonce, err := base64.StdEncoding.DecodeString(p.Nonce)
	if err != nil || len(nonce) < 16 {
		return "the owner proof is missing a usable nonce"
	}
	now := time.Now()
	if d := now.Sub(time.Unix(p.Unix, 0)); d > ownerActionSkew || d < -ownerActionSkew {
		return "the owner proof is stale; try again"
	}
	// Signature BEFORE the ownership lookup: the lookup reads a file, and an
	// unauthenticated caller should not be able to make us do that repeatedly.
	if !crypto.VerifyOwner(pub, crypto.OwnerActionTranscript(a.sessionID, action, nonce, p.Unix), sig) {
		return "the owner signature did not verify"
	}
	if ok, err := IsOwner(pub); err != nil || !ok {
		return "this device is not an owner of this machine"
	}
	// Last, so a forged proof can never consume a nonce and lock out the real
	// one by filling the set.
	if why := a.ownerNonces.use(p.Nonce, now); why != "" {
		return why
	}
	return ""
}
