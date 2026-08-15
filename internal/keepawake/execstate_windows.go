// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build windows

package keepawake

import (
	"runtime"
	"sync"

	"golang.org/x/sys/windows"
)

// Windows in-process sleep inhibitor: kernel32's SetThreadExecutionState, the
// same mechanism video players use. x/sys/windows doesn't wrap it, so we bind
// the proc lazily; ES_* values are from winbase.h.
var procSetThreadExecutionState = windows.NewLazySystemDLL("kernel32.dll").NewProc("SetThreadExecutionState")

const (
	esContinuous      = 0x80000000 // requirements below stay in force until changed
	esSystemRequired  = 0x00000001 // keep the system from idle-sleeping
	esDisplayRequired = 0x00000002 // keep the display lit (and the idle-lock away)
)

// execStateStart turns the inhibitor on and returns an idempotent stop func.
// No child process to spawn (there's no caffeinate equivalent) — the stop func
// just clears the flags.
//
// The trap this function exists to sidestep: the execution state is a property
// of the calling OS THREAD, and Go goroutines migrate between threads freely —
// a naive call would pin the state to whatever thread we happened to be on,
// then lose it (or leak it onto unrelated code) at the next reschedule. So a
// dedicated goroutine locks itself to one thread, sets the state there, and
// parks until stopped; that thread stays alive (and its state in force) for
// exactly the inhibitor's lifetime.
func execStateStart(display bool) (stop func(), ok bool) {
	flags := uintptr(esContinuous | esSystemRequired)
	if display {
		flags |= esDisplayRequired
	}

	set := make(chan bool)      // did the initial call take?
	done := make(chan struct{}) // closed by stop
	go func() {
		runtime.LockOSThread()
		// A zero return means the call failed (the non-zero value is the previous
		// state, which we don't need — ES_CONTINUOUS alone restores defaults).
		if prev, _, _ := procSetThreadExecutionState.Call(flags); prev == 0 {
			runtime.UnlockOSThread()
			set <- false
			return
		}
		set <- true
		<-done
		// Clear our requirements: ES_CONTINUOUS by itself resets this thread's
		// contribution to the system's sleep policy.
		_, _, _ = procSetThreadExecutionState.Call(uintptr(esContinuous))
		runtime.UnlockOSThread()
	}()

	if !<-set {
		return nil, false
	}
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }, true
}
