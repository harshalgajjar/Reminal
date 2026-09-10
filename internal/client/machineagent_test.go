// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"crypto/ed25519"
	"testing"

	"github.com/reminal/reminal/internal/crypto"
	"github.com/reminal/reminal/internal/protocol"
	"github.com/reminal/reminal/internal/session"
)

// The machine channel is found by every owner device by derivation, so its
// identity must come from the machine key — never a minted session id — and it
// must carry no PIN at all: owners connect by key, and nothing else connects.
func TestMachineAgentIdentityIsDerived(t *testing.T) {
	isolateHome(t)
	a, err := NewAgentWith("1.2.3", AgentOptions{Machine: true})
	if err != nil {
		t.Fatal(err)
	}
	key, err := loadOrCreateMachineKey()
	if err != nil {
		t.Fatal(err)
	}
	pub := key.Public().(ed25519.PublicKey)
	if a.sessionID != crypto.DeriveDirectoryID(pub) {
		t.Fatalf("machine agent registered at %q, want the derived directory id", a.sessionID)
	}
	if a.token != crypto.DirectoryToken(key) {
		t.Fatal("machine agent did not present the derived directory token")
	}
	if a.pin != "" || a.pinHash != "" {
		t.Fatal("a machine channel must not have a PIN — there is no shell to guard and no guest to admit")
	}
	if !a.machine || !a.headless || a.dirLimits == nil {
		t.Fatalf("machine agent not configured as one: machine=%v headless=%v limits=%v", a.machine, a.headless, a.dirLimits != nil)
	}
}

// Nothing terminal-shaped may reach the machine channel: there is no shell to
// type into, and the PIN handshake would let a non-owner in.
func TestMachineChannelRefusesTerminalMessages(t *testing.T) {
	refused := []protocol.MessageType{
		protocol.TypeKexInit, protocol.TypeData, protocol.TypeResize, protocol.TypeResume, protocol.TypeUpload,
	}
	for _, mt := range refused {
		if machineAccepts[mt] {
			t.Errorf("machine channel accepts %q — it must not", mt)
		}
	}
	needed := []protocol.MessageType{
		protocol.TypeOwnerInit, protocol.TypeDirQuery, protocol.TypeNewSession, protocol.TypeHostInfo,
		protocol.TypeWindowList, protocol.TypeAppOpen, protocol.TypeUpgrade, protocol.TypeWebRTCHello,
	}
	for _, mt := range needed {
		if !machineAccepts[mt] {
			t.Errorf("machine channel refuses %q — the homepage needs it", mt)
		}
	}
}

// The directory list/transcript/keys/rename/kill actions must be reachable ONLY
// on the owner-authenticated machine channel. A session agent shares its key
// with every PIN guest, so serving them there would let a guest of one session
// read, inject keys into, rename or kill EVERY other session on the machine.
func TestSessionAgentRefusesDirectoryActions(t *testing.T) {
	sess := &Agent{machine: false}
	mach := &Agent{machine: true}
	dirOps := []protocol.MessageType{
		protocol.TypeDirQuery, protocol.TypeDirRename, protocol.TypeDirRevokeSelf, protocol.TypeDirKill,
	}
	for _, mt := range dirOps {
		if sess.servesOnThisChannel(mt) {
			t.Errorf("a session agent serves %q — cross-session escalation to a PIN guest", mt)
		}
		if !mach.servesOnThisChannel(mt) {
			t.Errorf("the machine channel refuses %q — the homepage needs it", mt)
		}
	}
	// A session agent still serves the shell surface, and spawning stays allowed
	// (a shell guest can already run `reminal new`).
	for _, mt := range []protocol.MessageType{protocol.TypeData, protocol.TypeResize, protocol.TypeNewSession, protocol.TypeHostInfo} {
		if !sess.servesOnThisChannel(mt) {
			t.Errorf("a session agent refuses %q — it must serve the shell surface", mt)
		}
	}
	// The machine channel must process viewer disconnects (TypeClosed): the
	// always-on daemon otherwise keeps capturing the screen after the owner
	// leaves. And it must refuse terminal-shaped input.
	if !mach.servesOnThisChannel(protocol.TypeClosed) {
		t.Error("the machine channel drops TypeClosed — capture never stops when the owner leaves")
	}
	if mach.servesOnThisChannel(protocol.TypeData) {
		t.Error("the machine channel serves TypeData — there is no shell behind it")
	}
}

// A machine is reached through the directory, not listed in it: the channel
// must never write itself into the session registry, or every machine would
// show a phantom session.
func TestMachineAgentIsNotASession(t *testing.T) {
	isolateHome(t)
	a, err := NewAgentWith("1.2.3", AgentOptions{Machine: true})
	if err != nil {
		t.Fatal(err)
	}
	a.recordActive(0)
	all, err := session.ReadAllActive()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 0 {
		t.Fatalf("the machine channel registered itself as a session: %+v", all)
	}
}
