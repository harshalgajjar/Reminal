// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package session

import (
	"crypto/rand"
	"math/big"
)

const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

func NewID(length int) (string, error) {
	id := make([]byte, length)
	max := big.NewInt(int64(len(alphabet)))
	for i := range id {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		id[i] = alphabet[n.Int64()]
	}
	return string(id), nil
}
