// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package relay

import (
	"testing"

	"github.com/reminal/reminal/internal/protocol"
)

// authMsg is a tiny helper to build an auth Message.
func authMsg(pinHash, token, pin string) protocol.Message {
	return protocol.Message{Type: protocol.TypeAuth, PinHash: pinHash, Token: token, Pin: pin}
}

// TestAgentAuthAcceptEither exercises the Level B credential matrix: legacy
// pin_hash sessions keep working, new sessions are token-native, and a legacy
// session migrates to token-only the first time its upgraded agent presents
// pin_hash + token together.
func TestAgentAuthAcceptEither(t *testing.T) {
	s := NewServer()

	t.Run("legacy agent registers and reattaches with pin_hash", func(t *testing.T) {
		r := &room{}
		if e := s.handleAuthLocked(r, protocol.RoleAgent, authMsg("HASH", "", "")); e != "" {
			t.Fatalf("first pin_hash register rejected: %q", e)
		}
		if r.auth.pinHash != "HASH" || r.auth.token != "" {
			t.Fatalf("expected pin_hash stored, token empty; got %+v", r.auth)
		}
		if e := s.handleAuthLocked(r, protocol.RoleAgent, authMsg("HASH", "", "")); e != "" {
			t.Fatalf("reattach with same pin_hash rejected: %q", e)
		}
		if e := s.handleAuthLocked(r, protocol.RoleAgent, authMsg("OTHER", "", "")); e == "" {
			t.Fatalf("reattach with wrong pin_hash should be rejected")
		}
	})

	t.Run("new agent registers token-only, no pin_hash stored", func(t *testing.T) {
		r := &room{}
		if e := s.handleAuthLocked(r, protocol.RoleAgent, authMsg("", "TOK", "")); e != "" {
			t.Fatalf("token register rejected: %q", e)
		}
		if r.auth.token != "TOK" || r.auth.pinHash != "" {
			t.Fatalf("expected token stored, pin_hash empty; got %+v", r.auth)
		}
		if e := s.handleAuthLocked(r, protocol.RoleAgent, authMsg("", "TOK", "")); e != "" {
			t.Fatalf("token reattach rejected: %q", e)
		}
		if e := s.handleAuthLocked(r, protocol.RoleAgent, authMsg("", "NOPE", "")); e == "" {
			t.Fatalf("wrong token should be rejected")
		}
	})

	t.Run("legacy session migrates to token on upgraded reattach", func(t *testing.T) {
		r := &room{}
		// Old binary registered the session with a pin_hash.
		if e := s.handleAuthLocked(r, protocol.RoleAgent, authMsg("HASH", "", "")); e != "" {
			t.Fatalf("legacy register rejected: %q", e)
		}
		// Upgraded binary reattaches: proves control with pin_hash + brings token.
		if e := s.handleAuthLocked(r, protocol.RoleAgent, authMsg("HASH", "TOK", "")); e != "" {
			t.Fatalf("migration reattach rejected: %q", e)
		}
		if r.auth.token != "TOK" || r.auth.pinHash != "" {
			t.Fatalf("expected migration to token-only; got %+v", r.auth)
		}
		// After migration, the old pin_hash must no longer authenticate.
		if e := s.handleAuthLocked(r, protocol.RoleAgent, authMsg("HASH", "", "")); e == "" {
			t.Fatalf("post-migration pin_hash-only should be rejected")
		}
		// And the token keeps working.
		if e := s.handleAuthLocked(r, protocol.RoleAgent, authMsg("", "TOK", "")); e != "" {
			t.Fatalf("post-migration token rejected: %q", e)
		}
	})

	t.Run("migration requires the correct pin_hash", func(t *testing.T) {
		r := &room{}
		if e := s.handleAuthLocked(r, protocol.RoleAgent, authMsg("HASH", "", "")); e != "" {
			t.Fatalf("legacy register rejected: %q", e)
		}
		// A stranger who knows the session id but not the pin_hash cannot hijack
		// by presenting a token — the legacy credential must still match.
		if e := s.handleAuthLocked(r, protocol.RoleAgent, authMsg("WRONG", "EVIL", "")); e == "" {
			t.Fatalf("migration with wrong pin_hash should be rejected")
		}
		if r.auth.token != "" {
			t.Fatalf("failed migration must not store the attacker token; got %+v", r.auth)
		}
	})

	t.Run("agent with no credential is rejected", func(t *testing.T) {
		r := &room{}
		if e := s.handleAuthLocked(r, protocol.RoleAgent, authMsg("", "", "")); e == "" {
			t.Fatalf("empty credential should be rejected")
		}
	})
}

// TestViewerAuthNoPIN verifies the Level A change: a viewer is admitted on the
// session being live WITHOUT presenting a PIN, and any pin field is ignored.
func TestViewerAuthNoPIN(t *testing.T) {
	s := NewServer()

	// No agent yet → not ready.
	r := &room{}
	if e := s.handleAuthLocked(r, protocol.RoleViewer, authMsg("", "", "")); e == "" {
		t.Fatalf("viewer before agent should be 'session not ready'")
	}

	// Agent authenticates, then a viewer with NO pin is admitted.
	if e := s.handleAuthLocked(r, protocol.RoleAgent, authMsg("", "TOK", "")); e != "" {
		t.Fatalf("agent auth rejected: %q", e)
	}
	if e := s.handleAuthLocked(r, protocol.RoleViewer, authMsg("", "", "")); e != "" {
		t.Fatalf("viewer with no pin should be admitted once agent is live: %q", e)
	}
	// A stray pin field from an old viewer is ignored, not rejected.
	if e := s.handleAuthLocked(r, protocol.RoleViewer, authMsg("", "", "999999")); e != "" {
		t.Fatalf("viewer sending a legacy pin should still be admitted: %q", e)
	}
}
