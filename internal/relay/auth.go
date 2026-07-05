// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package relay

// authState tracks who has authenticated on a room.
//
// The relay intentionally does NO PIN verification of its own. A 6-digit PIN
// it could check would be offline-brute-forceable, and — worse — a relay that
// knew the PIN could unblind both ephemeral keys and MITM the EKE. So there is
// no failure counter / lockout here: viewers authenticate END-TO-END via the
// EKE (a wrong PIN fails the AES-GCM session-key unwrap), and the only online
// brute-force surface — forged kex handshakes against the agent — is bounded by
// the agent's own kex throttle. The relay merely records that an agent proved
// control of the session (via its pin_hash / reattach token) so it won't route
// a viewer into a session no agent is holding.
type authState struct {
	pinHash      string // legacy credential; superseded by token (see server.go)
	token        string // high-entropy reattach credential; empty on legacy sessions
	agentAuthed  bool
	viewerAuthed bool
}
