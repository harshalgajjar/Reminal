// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package crypto

import (
	"bytes"
	"testing"
)

// TestEKERoundTrip exercises the full handshake the way the wire
// protocol does it: viewer generates ephemeral key + ex_id, both
// sides blind/unblind with the PIN, ECDH, the agent wraps a session
// key, the viewer unwraps. A successful round-trip is the spec.
func TestEKERoundTrip(t *testing.T) {
	const pin = "483920"

	viewerEph, err := NewEphemeralKey()
	if err != nil {
		t.Fatalf("viewer keygen: %v", err)
	}
	agentEph, err := NewEphemeralKey()
	if err != nil {
		t.Fatalf("agent keygen: %v", err)
	}

	_, exID, err := NewExID()
	if err != nil {
		t.Fatalf("ex_id: %v", err)
	}

	// Viewer → agent: blinded viewer pubkey
	viewerBlinded, err := BlindPub(viewerEph.PublicKey().Bytes(), pin)
	if err != nil {
		t.Fatalf("blind viewer: %v", err)
	}
	if bytes.Equal(viewerBlinded, viewerEph.PublicKey().Bytes()) {
		t.Fatalf("blinded pubkey should not equal original")
	}

	// Agent receives, unblinds, computes shared.
	viewerPubFromAgent, err := UnblindPub(viewerBlinded, pin)
	if err != nil {
		t.Fatalf("unblind viewer: %v", err)
	}
	viewerPubObj, err := PeerPublicKey(viewerPubFromAgent)
	if err != nil {
		t.Fatalf("decode viewer key: %v", err)
	}
	if !bytes.Equal(viewerPubObj.Bytes(), viewerEph.PublicKey().Bytes()) {
		t.Fatalf("agent's decoded viewer pubkey diverges from viewer's actual")
	}
	sharedAgent, err := agentEph.ECDH(viewerPubObj)
	if err != nil {
		t.Fatalf("agent ECDH: %v", err)
	}

	// Agent wraps the session key.
	sessionKey, err := NewSessionKey()
	if err != nil {
		t.Fatalf("session key: %v", err)
	}
	wrapped, err := WrapSessionKey(sharedAgent, exID, sessionKey)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	// Agent → viewer: blinded agent pubkey
	agentBlinded, err := BlindPub(agentEph.PublicKey().Bytes(), pin)
	if err != nil {
		t.Fatalf("blind agent: %v", err)
	}

	// Viewer receives, unblinds, computes shared, unwraps.
	agentPubFromViewer, err := UnblindPub(agentBlinded, pin)
	if err != nil {
		t.Fatalf("unblind agent: %v", err)
	}
	agentPubObj, err := PeerPublicKey(agentPubFromViewer)
	if err != nil {
		t.Fatalf("decode agent key: %v", err)
	}
	sharedViewer, err := viewerEph.ECDH(agentPubObj)
	if err != nil {
		t.Fatalf("viewer ECDH: %v", err)
	}
	if !bytes.Equal(sharedAgent, sharedViewer) {
		t.Fatalf("ECDH shared secrets diverge")
	}
	recovered, err := UnwrapSessionKey(sharedViewer, exID, wrapped)
	if err != nil {
		t.Fatalf("unwrap: %v", err)
	}
	if !bytes.Equal(recovered, sessionKey) {
		t.Fatalf("recovered session key differs from sent")
	}
}

// TestEKEWrongPIN proves that a viewer with the wrong PIN can't
// unwrap the session key. This is exactly the property the v1 design
// lacked — a relay that recorded the v1 ciphertext could iterate the
// 10^6 PIN candidates against the AES-GCM tag and recover the key.
// In v2 the wrap key depends on the ECDH shared, which the relay
// can't compute without one of the ephemeral private keys.
func TestEKEWrongPIN(t *testing.T) {
	const realPIN = "483920"
	const wrongPIN = "111111"

	viewerEph, _ := NewEphemeralKey()
	agentEph, _ := NewEphemeralKey()
	_, exID, _ := NewExID()

	// Agent unblinds with the real PIN (it knows the PIN).
	viewerBlinded, _ := BlindPub(viewerEph.PublicKey().Bytes(), realPIN)
	viewerPubFromAgent, _ := UnblindPub(viewerBlinded, realPIN)
	viewerPubObj, _ := PeerPublicKey(viewerPubFromAgent)
	sharedAgent, _ := agentEph.ECDH(viewerPubObj)
	sessionKey, _ := NewSessionKey()
	wrapped, err := WrapSessionKey(sharedAgent, exID, sessionKey)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	agentBlinded, _ := BlindPub(agentEph.PublicKey().Bytes(), realPIN)

	// Viewer (or relay-MITM) tries to unwrap using the WRONG PIN.
	// The blinded agent pubkey unmasks to a different point, the
	// resulting ECDH diverges, the AES-GCM tag fails.
	agentPubFromViewer, _ := UnblindPub(agentBlinded, wrongPIN)
	agentPubObj, err := PeerPublicKey(agentPubFromViewer)
	if err != nil {
		// The mismatched mask might land on a low-order point and
		// PeerPublicKey rejects it — also a failure path, which is
		// fine for this test's intent.
		return
	}
	sharedViewer, err := viewerEph.ECDH(agentPubObj)
	if err != nil {
		// ECDH itself can fail on certain inputs — also a failure
		// path the attacker doesn't get past.
		return
	}
	if _, err := UnwrapSessionKey(sharedViewer, exID, wrapped); err == nil {
		t.Fatalf("unwrap with wrong PIN must fail, but succeeded")
	}
}

// TestBlindPubMaskShape: a passive attacker who only sees the
// blinded pubkey shouldn't learn anything. We check the trivial
// shape property — same input, different PIN → different blinded
// output. (A real distinguishing attack would need cryptanalysis;
// this just guards against an HKDF wiring bug that made the mask
// constant.)
func TestBlindPubVariesByPIN(t *testing.T) {
	priv, _ := NewEphemeralKey()
	pub := priv.PublicKey().Bytes()

	a, _ := BlindPub(pub, "000000")
	b, _ := BlindPub(pub, "999999")
	if bytes.Equal(a, b) {
		t.Fatalf("blinded pubkey under two distinct PINs collided")
	}
}

// TestBoxRoundTrip: the AEAD wrapper around a session key encrypts
// and decrypts correctly.
func TestBoxRoundTrip(t *testing.T) {
	key, err := NewSessionKey()
	if err != nil {
		t.Fatalf("session key: %v", err)
	}
	box, err := NewBox(key)
	if err != nil {
		t.Fatalf("new box: %v", err)
	}
	plaintext := []byte("hello reminal")
	enc, err := box.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	dec, err := box.Decrypt(enc)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(dec, plaintext) {
		t.Fatalf("plaintext mismatch: got %q, want %q", dec, plaintext)
	}
}
