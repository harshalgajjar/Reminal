// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !linux && !darwin && !windows

package vtwasm

import "errors"

// reserve: nothing is reserved here, and the memory is a slice (allocate).
func reserve(uint64) ([]byte, func(), error) { return nil, nil, errors.New("vtwasm: no reserve") }

func commit([]byte) error { return nil }
