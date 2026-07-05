// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package session

import (
	"crypto/rand"
	"encoding/hex"
)

// tokenBytes is the entropy of a reattach token. 32 bytes (256 bits) is far
// beyond any brute-force reach, which is the whole point: unlike the 6-digit
// PIN's bcrypt hash, a token the relay holds carries NO offline-crackable
// secret — knowing it lets you reattach as the agent but tells you nothing
// about the PIN, so it can never be used to MITM the EKE.
const tokenBytes = 32

// NewToken returns a fresh high-entropy hex reattach token. The agent presents
// it to the relay to prove control of a session across WS drops / hot-restarts,
// replacing the legacy bcrypt(PIN) credential (see internal/relay/auth.go).
func NewToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
