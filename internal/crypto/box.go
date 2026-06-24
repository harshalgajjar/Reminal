package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

// Box encrypts terminal data end-to-end. The relay only sees opaque blobs.
//
// Prior to v2 the AES key was HKDF(PIN, sessionID) — only ~20 bits of
// secrecy against the relay, since the salt (sessionID) is the relay's
// routing key. A passive relay could capture one ciphertext frame and
// offline-iterate the 10^6 possible PINs against the GCM tag. See
// kex.go for the v2 construction: a random 256-bit session key
// delivered to each viewer via an EKE-style PIN-authenticated ECDH.
type Box struct {
	aead cipher.AEAD
}

// NewBox wraps a 32-byte session key in an AES-256-GCM AEAD. The
// session key MUST come from NewSessionKey or from UnwrapSessionKey on
// the viewer side — there is no longer a deterministic derivation
// from (sessionID, PIN).
func NewBox(key []byte) (*Box, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("session key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// NewSessionKey returns 32 fresh random bytes for use as an
// AES-256-GCM session key.
func NewSessionKey() ([]byte, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return key, nil
}

func (b *Box) Encrypt(plaintext []byte) (string, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ciphertext := b.aead.Seal(nil, nonce, plaintext, nil)
	out := append(nonce, ciphertext...)
	return base64.StdEncoding.EncodeToString(out), nil
}

func (b *Box) Decrypt(encoded string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	nonceSize := b.aead.NonceSize()
	if len(raw) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}
	nonce, ciphertext := raw[:nonceSize], raw[nonceSize:]
	return b.aead.Open(nil, nonce, ciphertext, nil)
}
