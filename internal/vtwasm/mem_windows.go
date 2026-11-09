// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package vtwasm

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// reserve is n bytes of address space, inaccessible until committed, and
// how to give it back.
func reserve(n uint64) ([]byte, func(), error) {
	addr, err := windows.VirtualAlloc(0, uintptr(n), windows.MEM_RESERVE, windows.PAGE_NOACCESS)
	if err != nil {
		return nil, nil, err
	}
	// The address is the system's, not Go's: read as a pointer through a
	// pointer to it, which vet accepts.
	p := *(*unsafe.Pointer)(unsafe.Pointer(&addr))
	b := unsafe.Slice((*byte)(p), n)
	return b, func() { _ = windows.VirtualFree(addr, 0, windows.MEM_RELEASE) }, nil
}

// commit makes a reserved range readable and writable: memory from here on.
func commit(b []byte) error {
	if len(b) == 0 {
		return nil
	}
	_, err := windows.VirtualAlloc(uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)), windows.MEM_COMMIT, windows.PAGE_READWRITE)
	return err
}
