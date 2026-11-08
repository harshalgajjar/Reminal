// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package vtwasm

import (
	"github.com/tetratelabs/wazero/experimental"
)

// An instance's linear memory. wazero's own is a Go slice that is copied
// whole each time the module grows it, which is where a session's memory
// peaked, and the GC paces on it. Instead the module's whole maximum is
// reserved as address space once (reserve: mmap on Linux and macOS,
// VirtualAlloc on Windows), committed as the module grows, and freed with
// the instance: growth never copies, the pages live outside the Go heap,
// and only what is committed is memory. Where there is no reserve, a slice.

// memoryMax bounds one instance's memory: the module declares no maximum,
// for which wazero asks 4 GiB, and that much address space per session is
// refused by a strict overcommit or RLIMIT_AS. A grow past it is refused
// (reservedMemory.Reallocate gives nil), and the module's call traps: a
// terminal cannot run away, and its session goes on without it (Terminal.err).
// A variable for the test of that.
var memoryMax uint64 = 512 << 20

// allocate is the experimental.MemoryAllocatorFunc of every instance.
func allocate(capacity, maximum uint64) experimental.LinearMemory {
	maximum = min(maximum, memoryMax)
	if maximum < capacity {
		maximum = capacity
	}
	if buf, free, err := reserve(maximum); err == nil {
		return &reservedMemory{buf: buf, free: free}
	}
	return &sliceMemory{buf: make([]byte, 0, capacity), max: maximum}
}

// reservedMemory is a reservation of the maximum, committed as it grows.
type reservedMemory struct {
	buf       []byte // the whole reservation, PROT_NONE past committed
	committed uint64
	free      func()
}

// Reallocate implements experimental.LinearMemory: the memory at size, or
// nil when size is past the maximum, which the module sees as a refused grow.
func (m *reservedMemory) Reallocate(size uint64) []byte {
	if size > uint64(len(m.buf)) {
		return nil
	}
	if size > m.committed {
		if err := commit(m.buf[m.committed:size]); err != nil {
			return nil
		}
		m.committed = size
	}
	return m.buf[:size:size]
}

// Free implements experimental.LinearMemory: the whole reservation goes
// back when the instance is closed.
func (m *reservedMemory) Free() { m.free() }

// sliceMemory is the memory where nothing can be reserved: a slice grown by
// copying, bounded the same way.
type sliceMemory struct {
	buf []byte
	max uint64
}

func (m *sliceMemory) Reallocate(size uint64) []byte {
	if size > m.max {
		return nil
	}
	if size > uint64(cap(m.buf)) {
		grown := make([]byte, size, min(max(size*2, size), m.max))
		copy(grown, m.buf)
		m.buf = grown
	}
	m.buf = m.buf[:size]
	return m.buf
}

func (m *sliceMemory) Free() { m.buf = nil }
