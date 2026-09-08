// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package crypto

// Owner (PIN-free) connect: mutually-authenticated ECDH.
//
// An enrolled device connects without a PIN by proving its Ed25519 owner key
// instead. The exchange is a signed Diffie-Hellman (Noise-IK in shape):
//
//   1. Both sides generate an ephemeral X25519 keypair (NewEphemeralKey) and
//      exchange the RAW public keys — no PIN blinding.
//   2. Both run ECDH to a shared secret.
//   3. Each side signs OwnerTranscript(...) with its long-lived identity key:
//      the device with its owner key, the agent with its machine key. The
//      transcript binds the session id, BOTH ephemerals, and BOTH identity
//      public keys, so a signature is meaningless on any other exchange and
//      neither identity can be swapped by a relay in the middle.
//   4. The agent verifies the device signature against owners.json (authorised),
//      then delivers the session key with WrapSessionKey over the shared secret.
//      The device verifies the agent signature against the machine key it pinned
//      on first connect (trust-on-first-use) — detecting a relay impersonating
//      the host.
//
// The session key itself is the agent's existing per-session key (wrapped, same
// as the PIN path) so every viewer — PIN or owner — shares one key and the agent
// encrypts the PTY stream once.

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
)

const (
	ownerClientTag = "reminal-owner-cli-v1"
	ownerServerTag = "reminal-owner-srv-v1"
)

// ownerHash is a length-prefixed digest over a domain tag, the session id, and
// the given fields — length-prefixing means no two distinct field sequences can
// collide, and distinct tags keep the client and server transcripts from ever
// matching.
func ownerHash(tag, sessionID string, fields ...[]byte) []byte {
	h := sha256.New()
	all := append([][]byte{[]byte(tag), []byte(sessionID)}, fields...)
	for _, f := range all {
		var l [4]byte
		binary.BigEndian.PutUint32(l[:], uint32(len(f)))
		h.Write(l[:])
		h.Write(f)
	}
	return h.Sum(nil)
}

// OwnerClientTranscript is what the DEVICE signs in own_init — it commits the
// device to this ephemeral for this session before it has seen the agent's
// ephemeral or machine key. The agent verifies it to authorise the device.
func OwnerClientTranscript(sessionID string, viewerEph, devicePub []byte) []byte {
	return ownerHash(ownerClientTag, sessionID, viewerEph, devicePub)
}

// OwnerServerTranscript is what the MACHINE signs in own_resp — it binds BOTH
// ephemerals and BOTH identities, so the device can confirm it's the real host
// (verified against the pinned machine key) and not a relay in the middle.
func OwnerServerTranscript(sessionID string, viewerEph, agentEph, devicePub, machinePub []byte) []byte {
	return ownerHash(ownerServerTag, sessionID, viewerEph, agentEph, devicePub, machinePub)
}

// ownerActionTag domain-separates a privileged ACTION request from the
// handshake transcripts above. Reusing either of those would let a signature
// captured during a connect be replayed as an authorisation to act.
const ownerActionTag = "reminal-owner-action-v1:"

// OwnerActionTranscript is what a device signs to authorise a privileged
// action on a machine it owns — today, upgrading the binary and restarting
// every session on it.
//
// This exists because the session key CANNOT carry authorisation: every viewer
// on a session shares the same key, so a PIN guest can encrypt, decrypt and
// forge any message an owner can. Ownership therefore has to be proved by the
// request itself, with a key only an enrolled device holds.
//
// The transcript binds four things, and each one closes a specific hole:
//   - sessionID — a signature for one session cannot authorise another
//   - action    — an "upgrade" proof cannot be replayed as some future verb
//   - nonce     — one-time use, so a guest who decrypts an owner's request
//     (they hold the same session key) cannot replay it
//   - unix      — bounds how long a captured proof stays interesting at all
func OwnerActionTranscript(sessionID, action string, nonce []byte, unix int64) []byte {
	var ts [8]byte
	binary.BigEndian.PutUint64(ts[:], uint64(unix))
	return ownerHash(ownerActionTag, sessionID, []byte(action), nonce, ts[:])
}

const directoryTag = "reminal-directory-v1:"

// directoryAlphabet matches session.NewID's Crockford-ish set so a directory id
// looks like an ordinary session id on the wire. Exactly 32 chars, so mapping a
// hash byte with %len is unbiased.
const directoryAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// DirectoryIDLen is deliberately longer than a real session id (8 chars) so a
// derived directory id can never collide with one an agent randomly generates.
const DirectoryIDLen = 26

// DeriveDirectoryID maps a machine identity key to the relay channel its owners
// rendezvous on to enumerate the machine's live sessions. Only holders of the
// machine pubkey — the machine itself and its enrolled owner devices — can
// compute it; to the relay it's an opaque, session-id-shaped string it can't
// reverse to the machine key. Deterministic, so every owner and the machine
// agree on the same channel without coordinating.
func DeriveDirectoryID(machinePub []byte) string {
	h := sha256.Sum256(append([]byte(directoryTag), machinePub...))
	id := make([]byte, DirectoryIDLen)
	for i := 0; i < DirectoryIDLen; i++ {
		id[i] = directoryAlphabet[int(h[i%len(h)])%len(directoryAlphabet)]
	}
	return string(id)
}

// DirectoryToken derives the relay auth token the directory host registers with,
// as an HMAC-like digest of the machine PRIVATE key. It's stable (so a host that
// reconnects — or a sibling agent that takes over — presents the same token the
// relay already stored for the channel) and opaque (the relay sees only the
// digest, never the key). Distinct domain from the channel id so the two derived
// values can't be related.
func DirectoryToken(machinePriv ed25519.PrivateKey) string {
	h := sha256.Sum256(append([]byte("reminal-directory-token:"), machinePriv.Seed()...))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

const revokeSelfTag = "reminal-revoke-self-v1"

// RevokeSelfTranscript is what a device signs to prove it is revoking ITSELF
// from a specific machine. Binding both the machine key and the device key means
// the signature can't be replayed to another machine, and — because the host
// verifies it with the named device's key — one owner can never revoke another
// (they don't hold that device's private key). So remote revocation is safe
// without the sudo gate: the worst you can do is lock yourself out.
func RevokeSelfTranscript(machinePub, devicePub []byte) []byte {
	return ownerHash(revokeSelfTag, "", machinePub, devicePub)
}

// SignOwner signs an owner-connect transcript with an Ed25519 identity key.
func SignOwner(priv ed25519.PrivateKey, transcript []byte) []byte {
	return ed25519.Sign(priv, transcript)
}

// VerifyOwner checks an owner-connect signature, tolerating malformed inputs.
func VerifyOwner(pub ed25519.PublicKey, transcript, sig []byte) bool {
	return len(pub) == ed25519.PublicKeySize && ed25519.Verify(pub, transcript, sig)
}
