// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package session

import (
	"crypto/rand"
	"fmt"
	"math/big"
)

const pinDigits = "0123456789"

func NewPIN(length int) (string, error) {
	pin := make([]byte, length)
	max := big.NewInt(10)
	for i := range pin {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		pin[i] = pinDigits[n.Int64()]
	}
	return string(pin), nil
}

func ValidatePIN(pin string) error {
	if len(pin) < 6 || len(pin) > 8 {
		return fmt.Errorf("PIN must be 6–8 digits")
	}
	for _, c := range pin {
		if c < '0' || c > '9' {
			return fmt.Errorf("PIN must be numeric")
		}
	}
	return nil
}
