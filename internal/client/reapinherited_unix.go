//go:build !windows

package client

import "syscall"

// reapInherited clears children that died before this image took over and were
// never waited on.
//
// A hot restart is an in-place Exec: the PID is preserved, and so is every
// child, including the dead ones. So the leak fixed in 3.13.16/3.13.17 could
// not be cleared by restarting — the fresh binary inherited the whole backlog
// (815 entries on the Mac that reported this) and had no reason to look at it.
// Reaping here is what finally drains a machine that has been up for weeks,
// without ending anyone's session.
//
// WNOHANG is what makes this safe: it collects only children that have ALREADY
// exited and returns immediately otherwise, so the live PTY shell we inherited
// is never waited on. Call it once at startup, before anything is spawned, so
// it cannot race an os/exec Wait and steal its exit status.
func reapInherited() int {
	n := 0
	for {
		var ws syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil)
		// 0 = children exist but none have exited; <0 = ECHILD, none at all.
		if pid <= 0 || err != nil {
			return n
		}
		n++
	}
}
