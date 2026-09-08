// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"
	"time"

	"github.com/reminal/reminal/internal/crypto"
)

// signAction builds the proof a viewer would send.
func signAction(t *testing.T, priv ed25519.PrivateKey, sid, action string, unix int64) ownerProof {
	t.Helper()
	nonce := make([]byte, 24)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	sig := crypto.SignOwner(priv, crypto.OwnerActionTranscript(sid, action, nonce, unix))
	return ownerProof{
		DevicePub: base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey)),
		DeviceSig: base64.StdEncoding.EncodeToString(sig),
		Nonce:     base64.StdEncoding.EncodeToString(nonce),
		Unix:      unix,
	}
}

// The threat this exists for: every viewer on a session shares one encryption
// key, so a PIN guest can craft the upgrade message byte for byte. Hiding the
// button changes nothing. The host must refuse.
func TestUpgradeRequiresAnEnrolledOwner(t *testing.T) {
	isolateHome(t)
	a := &Agent{sessionID: "SESSION1"}
	now := time.Now().Unix()

	// A guest with a perfectly well-formed key that is simply not enrolled.
	guestPub, guestPriv, _ := ed25519.GenerateKey(rand.Reader)
	_ = guestPub
	if why := a.verifyOwnerAction(signAction(t, guestPriv, "SESSION1", "upgrade", now), "upgrade"); why == "" {
		t.Fatal("a non-owner device authorised an upgrade — a PIN guest could replace the binary and restart every session")
	}

	// No proof at all, which is what a hand-rolled message looks like.
	if why := a.verifyOwnerAction(ownerProof{}, "upgrade"); why == "" {
		t.Error("an empty proof authorised an upgrade")
	}

	// Now enrol the device and it should pass.
	ownerPub, ownerPriv, _ := ed25519.GenerateKey(rand.Reader)
	if _, _, err := AddOwner(ownerID(ownerPub), "test device"); err != nil {
		t.Skipf("cannot enrol an owner in this environment: %v", err)
	}
	if why := a.verifyOwnerAction(signAction(t, ownerPriv, "SESSION1", "upgrade", now), "upgrade"); why != "" {
		t.Fatalf("an enrolled owner was refused: %s", why)
	}
}

func TestOwnerProofCannotBeReplayedOrRetargeted(t *testing.T) {
	isolateHome(t)
	ownerPub, ownerPriv, _ := ed25519.GenerateKey(rand.Reader)
	if _, _, err := AddOwner(ownerID(ownerPub), "test device"); err != nil {
		t.Skipf("cannot enrol an owner in this environment: %v", err)
	}
	a := &Agent{sessionID: "SESSION1"}
	now := time.Now().Unix()

	// Replay: a guest holds the same session key, so it can DECRYPT an owner's
	// request and send it again. The nonce must be single-use.
	p := signAction(t, ownerPriv, "SESSION1", "upgrade", now)
	if why := a.verifyOwnerAction(p, "upgrade"); why != "" {
		t.Fatalf("first use refused: %s", why)
	}
	if why := a.verifyOwnerAction(p, "upgrade"); why == "" {
		t.Error("the same proof worked twice — a guest could replay an owner's upgrade")
	}

	// Wrong session: a proof captured on one session must not authorise another.
	other := &Agent{sessionID: "SESSION2"}
	if why := other.verifyOwnerAction(signAction(t, ownerPriv, "SESSION1", "upgrade", now), "upgrade"); why == "" {
		t.Error("a proof for SESSION1 authorised an action on SESSION2")
	}

	// Wrong action: an "upgrade" proof must not authorise some other verb.
	if why := a.verifyOwnerAction(signAction(t, ownerPriv, "SESSION1", "upgrade", now), "wipe"); why == "" {
		t.Error("an upgrade proof authorised a different action")
	}

	// Stale: bounds how long a captured proof stays interesting.
	old := time.Now().Add(-ownerActionSkew - time.Minute).Unix()
	if why := a.verifyOwnerAction(signAction(t, ownerPriv, "SESSION1", "upgrade", old), "upgrade"); why == "" {
		t.Error("a stale proof was accepted")
	}

	// Tampered signature.
	bad := signAction(t, ownerPriv, "SESSION1", "upgrade", time.Now().Unix())
	bad.DeviceSig = base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
	if why := a.verifyOwnerAction(bad, "upgrade"); why == "" {
		t.Error("a zeroed signature verified")
	}
}

// A forged proof must not be able to consume nonce capacity and lock out the
// real owner.
func TestForgedProofDoesNotSpendANonce(t *testing.T) {
	isolateHome(t)
	ownerPub, ownerPriv, _ := ed25519.GenerateKey(rand.Reader)
	if _, _, err := AddOwner(ownerID(ownerPub), "test device"); err != nil {
		t.Skipf("cannot enrol an owner in this environment: %v", err)
	}
	a := &Agent{sessionID: "S"}
	now := time.Now().Unix()
	good := signAction(t, ownerPriv, "S", "upgrade", now)

	// Same nonce, but signed by a stranger: rejected, and must NOT be recorded.
	_, guestPriv, _ := ed25519.GenerateKey(rand.Reader)
	forged := signAction(t, guestPriv, "S", "upgrade", now)
	forged.Nonce = good.Nonce
	if why := a.verifyOwnerAction(forged, "upgrade"); why == "" {
		t.Fatal("a forged proof verified")
	}
	if why := a.verifyOwnerAction(good, "upgrade"); why != "" {
		t.Errorf("the real owner was locked out by a forged proof reusing its nonce: %s", why)
	}
}
