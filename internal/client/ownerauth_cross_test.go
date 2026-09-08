// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"

	"github.com/reminal/reminal/internal/crypto"
)

// The transcript is built independently in two languages: Go's
// OwnerActionTranscript and the viewer's signOwnerAction. If they ever drift —
// a field reordered, a length prefix widened, the tag edited on one side —
// every same-language test still passes and every real owner is refused with
// "the owner signature did not verify".
//
// So this checks a proof captured from the SHIPPING browser code against the
// SHIPPING Go verifier. Regenerate the fixture only when the transcript is
// deliberately changed, and then on purpose.
//
// Session id and action are the ones the fixture was signed for.
func TestBrowserProofMatchesGoTranscript(t *testing.T) {
	raw, err := os.ReadFile("testdata/browser_owner_proof.json")
	if err != nil {
		t.Fatalf("fixture missing: %v", err)
	}
	var p ownerProof
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	pub, err := base64.StdEncoding.DecodeString(p.DevicePub)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		t.Fatalf("fixture device key unreadable: %v", err)
	}
	sig, err := base64.StdEncoding.DecodeString(p.DeviceSig)
	if err != nil {
		t.Fatal(err)
	}
	nonce, err := base64.StdEncoding.DecodeString(p.Nonce)
	if err != nil {
		t.Fatal(err)
	}
	// Verified against the fixture's own timestamp, so this stays a statement
	// about the transcript rather than about the clock.
	tr := crypto.OwnerActionTranscript("SESSION1", "upgrade", nonce, p.Unix)
	if !crypto.VerifyOwner(pub, tr, sig) {
		t.Fatal("a proof signed by the browser does not verify in Go — the two " +
			"transcript builders have drifted, and every genuine owner would be refused")
	}
}
