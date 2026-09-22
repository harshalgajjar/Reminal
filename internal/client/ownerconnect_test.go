// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"reminal/internal/crypto"
)

func TestIsOwner(t *testing.T) {
	isolateHome(t)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	if ok, _ := IsOwner(pub); ok {
		t.Fatal("unenrolled key reported as owner")
	}
	if _, _, err := AddOwner(ownerID(pub), "iPhone"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := IsOwner(pub); !ok {
		t.Fatal("enrolled key not reported as owner")
	}
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	if ok, _ := IsOwner(other); ok {
		t.Fatal("different key reported as owner")
	}
}

func TestKnownMachinesTOFU(t *testing.T) {
	isolateHome(t)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	if _, ok, _ := PinnedMachineKey("SESS"); ok {
		t.Fatal("nothing should be pinned yet")
	}
	if conflict, err := RecordMachineKey("SESS", pub); err != nil || conflict {
		t.Fatalf("first pin: conflict=%v err=%v", conflict, err)
	}
	got, ok, _ := PinnedMachineKey("SESS")
	if !ok || !bytes.Equal(got, pub) {
		t.Fatal("pinned key not returned")
	}
	// Same key again → no conflict.
	if conflict, _ := RecordMachineKey("SESS", pub); conflict {
		t.Fatal("re-pinning the same key flagged a conflict")
	}
	// Different key for the same session → conflict (possible MITM), unchanged.
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	if conflict, _ := RecordMachineKey("SESS", other); !conflict {
		t.Fatal("a changed machine key was not flagged")
	}
	if got, _, _ := PinnedMachineKey("SESS"); !bytes.Equal(got, pub) {
		t.Fatal("conflict overwrote the pinned key")
	}
}

// TestOwnerHandshakeFlow simulates the full 4-message PIN-free connect in memory,
// exercising the real IsOwner check, the TOFU store, and the crypto primitives.
func TestOwnerHandshakeFlow(t *testing.T) {
	isolateHome(t)
	devPub, devPriv, _ := ed25519.GenerateKey(rand.Reader)
	macPub, macPriv, _ := ed25519.GenerateKey(rand.Reader)
	if _, _, err := AddOwner(ownerID(devPub), "iPhone"); err != nil {
		t.Fatal(err)
	}
	const sessionID = "SESS1234"
	sessionKey := make([]byte, 32)
	_, _ = rand.Read(sessionKey)

	// msg1  client → agent : viewer ephemeral + device pubkey + device sig
	viewerEph, _ := crypto.NewEphemeralKey()
	vEph := viewerEph.PublicKey().Bytes()
	sigDevice := crypto.SignOwner(devPriv, crypto.OwnerClientTranscript(sessionID, vEph, devPub))

	// agent: authorize the device (sig + enrollment), then answer with its
	// machine identity + server sig.
	if !crypto.VerifyOwner(devPub, crypto.OwnerClientTranscript(sessionID, vEph, devPub), sigDevice) {
		t.Fatal("agent rejected a valid device signature")
	}
	if ok, _ := IsOwner(devPub); !ok {
		t.Fatal("enrolled device not recognised by the agent")
	}
	agentEph, _ := crypto.NewEphemeralKey()
	aEph := agentEph.PublicKey().Bytes()
	sigMachine := crypto.SignOwner(macPriv, crypto.OwnerServerTranscript(sessionID, vEph, aEph, devPub, macPub))

	// msg2  agent → client : agent ephemeral + machine pubkey + machine sig
	if _, ok, _ := PinnedMachineKey(sessionID); ok {
		t.Fatal("machine should be unpinned on first connect")
	}
	if !crypto.VerifyOwner(macPub, crypto.OwnerServerTranscript(sessionID, vEph, aEph, devPub, macPub), sigMachine) {
		t.Fatal("client rejected a valid machine signature")
	}
	if conflict, err := RecordMachineKey(sessionID, macPub); err != nil || conflict {
		t.Fatalf("TOFU pin failed: conflict=%v err=%v", conflict, err)
	}
	aPeer, _ := crypto.PeerPublicKey(vEph)
	sharedA, _ := agentEph.ECDH(aPeer)
	_, exID, _ := crypto.NewExID()
	wrapped, err := crypto.WrapSessionKey(sharedA, exID, sessionKey)
	if err != nil {
		t.Fatal(err)
	}

	// msg4  agent → client : wrapped session key
	vPeer, _ := crypto.PeerPublicKey(agentEph.PublicKey().Bytes())
	sharedC, _ := viewerEph.ECDH(vPeer)
	gotK, err := crypto.UnwrapSessionKey(sharedC, exID, wrapped)
	if err != nil || !bytes.Equal(gotK, sessionKey) {
		t.Fatalf("session key delivery failed: %v", err)
	}

	// A relay swapping in a different machine key on a later connect is caught.
	evilMachine, _, _ := ed25519.GenerateKey(rand.Reader)
	if conflict, _ := RecordMachineKey(sessionID, evilMachine); !conflict {
		t.Fatal("machine-key swap (MITM) not detected on reconnect")
	}
	// A device that isn't enrolled can't pass the agent's check.
	evilDevice, _, _ := ed25519.GenerateKey(rand.Reader)
	if ok, _ := IsOwner(evilDevice); ok {
		t.Fatal("unenrolled device accepted as owner")
	}
}
