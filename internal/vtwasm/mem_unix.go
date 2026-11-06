// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build linux || darwin

package vtwasm

import "golang.org/x/sys/unix"

// reserve is n bytes of address space, inaccessible until committed, and
// how to give it back.
func reserve(n uint64) ([]byte, func(), error) {
	b, err := unix.Mmap(-1, 0, int(n), unix.PROT_NONE, unix.MAP_PRIVATE|unix.MAP_ANON|unix.MAP_NORESERVE)
	if err != nil {
		return nil, nil, err
	}
	return b, func() { _ = unix.Munmap(b) }, nil
}

// commit makes a reserved range readable and writable: memory from here on.
func commit(b []byte) error { return unix.Mprotect(b, unix.PROT_READ|unix.PROT_WRITE) }
