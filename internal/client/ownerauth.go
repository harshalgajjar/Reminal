// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"crypto/ed25519"
	"encoding/base64"
	"sync"
	"time"

	"github.com/reminal/reminal/internal/crypto"
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
}

// use records a nonce and reports whether it was fresh. A repeat is a replay.
func (n *ownerNonces) use(nonce string, now time.Time) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.seen == nil {
		n.seen = make(map[string]time.Time)
	}
	// Drop anything older than the window on the way past; a proof that stale
	// is rejected by the skew check anyway, so remembering it buys nothing.
	for k, t := range n.seen {
		if now.Sub(t) > ownerActionSkew*2 {
			delete(n.seen, k)
		}
	}
	if _, dup := n.seen[nonce]; dup {
		return false
	}
	if len(n.seen) >= ownerNonceMax {
		// Full of live entries: refuse rather than evict. Evicting under
		// pressure is precisely how an attacker would make room for a replay.
		return false
	}
	n.seen[nonce] = now
	return true
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
	if !a.ownerNonces.use(p.Nonce, now) {
		return "this owner proof has already been used"
	}
	return ""
}
