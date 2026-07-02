// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package crypto

// PIN-authenticated key exchange (EKE-style) for reminal v2.
//
// The v1 wire encryption key was deterministically derived from
// (PIN, sessionID) via HKDF. The sessionID is the relay's routing
// key, so its only secret is the 6-digit PIN (~20 bits). A relay
// that recorded a single ciphertext frame could iterate all 10^6
// PIN candidates offline against the AES-GCM authentication tag and
// recover the session key. See GitHub issue #1.
//
// v2 establishes the session key with an authenticated ECDH instead:
//
//   1. Both endpoints (host & viewer) generate ephemeral X25519
//      keypairs per WebSocket connection.
//   2. Each side blinds its public key by XOR-ing it with a 32-byte
//      mask derived from the PIN: HKDF-SHA256(IKM=PIN, salt=blindSalt,
//      info=kexVersion). Every 32-byte value is a valid Montgomery
//      u-coordinate, so the masked bytes carry no PIN information a
//      passive observer can verify offline.
//   3. After exchanging blinded public keys, each side unblinds with
//      its own copy of the PIN, runs ECDH, and derives a wrap key:
//      HKDF-SHA256(IKM=shared, salt=ex_id, info=wrapInfo).
//   4. The agent wraps a random 256-bit session key under this wrap
//      key (AES-256-GCM) and sends the ciphertext to the viewer.
//   5. The viewer decrypts. A successful unwrap proves both sides
//      used the same PIN.
//
// Security properties:
//
//   - Passive recorder: cannot brute-force the PIN. To verify a
//     guess they would need either an ECDH private key (ephemeral,
//     destroyed after the handshake) or a value whose distribution
//     depends on the PIN — the blinded pubkey looks uniform random
//     for every PIN. Forward secrecy is preserved.
//   - Active MITM (the threat model the README's "relay-blind"
//     claim is supposed to cover): forced to complete a full ECDH
//     with each side per PIN guess. Each guess is one online
//     attempt — a wrong guess produces a different wrap key, the
//     unwrap fails, the viewer disconnects. The relay observes
//     many failed handshakes; this is loud and the 5-strike PIN
//     lockout at the auth layer above us still bounds it.
//
// The exchange ID (ex_id) the viewer picks per handshake is the
// HKDF salt for the wrap key and is also echoed back in
// TypeKexResp. With multiple viewers, the relay broadcasts the
// agent's response to all of them; the ex_id is how each viewer
// recognises which response is for them. (A non-originating viewer
// could not unwrap it anyway — different ECDH shared secret — but
// matching the ex_id avoids gratuitous decryption attempts.)

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

// Wire-protocol version. Bumping this string forces a hard cutover —
// any peer using a different version derives a different mask / wrap
// key and the unwrap fails. v1 used "reminal-v1" in HKDF.
const kexVersion = "reminal-kex-v2"

// Domain separation for the PIN→mask HKDF call. Distinct from the
// wrap key derivation so the two key streams cannot collide.
var blindSalt = []byte("reminal-blind-v2")

// Domain separation for the wrap-key derivation (salt is per-handshake
// ex_id; info is this constant).
var wrapInfo = []byte("reminal-wrap-v2")

// PubKeyBytes is the length of an X25519 public key.
const PubKeyBytes = 32

// ExIDBytes is the length the viewer should pick for the random
// per-handshake correlation ID. Long enough that two concurrent
// handshakes from different viewers won't collide.
const ExIDBytes = 16

// NewEphemeralKey returns a fresh X25519 keypair for one handshake.
func NewEphemeralKey() (*ecdh.PrivateKey, error) {
	return ecdh.X25519().GenerateKey(rand.Reader)
}

// pinMask returns the 32-byte XOR mask derived from the PIN.
func pinMask(pin string) ([]byte, error) {
	r := hkdf.New(sha256.New, []byte(pin), blindSalt, []byte(kexVersion))
	mask := make([]byte, PubKeyBytes)
	if _, err := io.ReadFull(r, mask); err != nil {
		return nil, err
	}
	return mask, nil
}

// BlindPub XOR-masks a 32-byte X25519 public key with HKDF(PIN). The
// output is indistinguishable from random without the PIN. The
// transform is its own inverse — UnblindPub just calls back here.
func BlindPub(pub []byte, pin string) ([]byte, error) {
	if len(pub) != PubKeyBytes {
		return nil, fmt.Errorf("blind: public key must be %d bytes, got %d", PubKeyBytes, len(pub))
	}
	mask, err := pinMask(pin)
	if err != nil {
		return nil, err
	}
	out := make([]byte, PubKeyBytes)
	for i := range pub {
		out[i] = pub[i] ^ mask[i]
	}
	return out, nil
}

// UnblindPub reverses BlindPub. XOR is its own inverse, so this is the
// same operation — the named alias exists for code that reads with the
// peer's role in mind.
func UnblindPub(blinded []byte, pin string) ([]byte, error) {
	return BlindPub(blinded, pin)
}

// NewExID returns a fresh random per-handshake correlation ID,
// hex-encoded for use in the wire `ex_id` field.
func NewExID() (string, []byte, error) {
	raw := make([]byte, ExIDBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	return hex.EncodeToString(raw), raw, nil
}

// ParseExID decodes the hex form back to bytes. Returns an error if
// the encoding is malformed or the length looks suspicious; we accept
// any length that came from a real client to stay tolerant of future
// minor bumps, but reject empty or absurdly long IDs that a malicious
// relay could try to feed us.
func ParseExID(hexStr string) ([]byte, error) {
	if hexStr == "" {
		return nil, errors.New("ex_id missing")
	}
	if len(hexStr) > 128 {
		return nil, errors.New("ex_id too long")
	}
	return hex.DecodeString(hexStr)
}

// wrapKey derives the AES-256-GCM key that wraps the session key.
// salt is the handshake's ex_id (so two concurrent handshakes derive
// independent wrap keys even if — somehow — the ECDH shared collides).
func wrapKey(shared, exID []byte) ([]byte, error) {
	r := hkdf.New(sha256.New, shared, exID, wrapInfo)
	key := make([]byte, 32)
	if _, err := io.ReadFull(r, key); err != nil {
		return nil, err
	}
	return key, nil
}

// WrapSessionKey encrypts a 32-byte session key under the AES-256-GCM
// key derived from (shared, exID). Output is nonce ‖ ciphertext.
func WrapSessionKey(shared, exID, sessionKey []byte) ([]byte, error) {
	if len(sessionKey) != 32 {
		return nil, fmt.Errorf("session key must be 32 bytes, got %d", len(sessionKey))
	}
	key, err := wrapKey(shared, exID)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	ct := aead.Seal(nil, nonce, sessionKey, nil)
	out := make([]byte, 0, len(nonce)+len(ct))
	out = append(out, nonce...)
	out = append(out, ct...)
	return out, nil
}

// UnwrapSessionKey decrypts a wrap produced by WrapSessionKey. A
// failure here means the peer used a different PIN, the relay tried
// an active MITM with the wrong guess, or the bytes were corrupted.
func UnwrapSessionKey(shared, exID, wrapped []byte) ([]byte, error) {
	key, err := wrapKey(shared, exID)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(wrapped) < aead.NonceSize()+aead.Overhead() {
		return nil, errors.New("wrap: ciphertext too short")
	}
	nonce, ct := wrapped[:aead.NonceSize()], wrapped[aead.NonceSize():]
	pt, err := aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("wrap: %w", err)
	}
	if len(pt) != 32 {
		return nil, fmt.Errorf("wrap: unexpected session key length %d", len(pt))
	}
	return pt, nil
}

// PeerPublicKey wraps a 32-byte X25519 public key for use with
// ecdh.PrivateKey.ECDH. Rejects low-order / invalid points (which
// crypto/ecdh's X25519 curve also flags, so this is mostly defensive
// labelling).
func PeerPublicKey(raw []byte) (*ecdh.PublicKey, error) {
	if len(raw) != PubKeyBytes {
		return nil, fmt.Errorf("peer key: wrong length %d", len(raw))
	}
	return ecdh.X25519().NewPublicKey(raw)
}
