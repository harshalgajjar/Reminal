// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build darwin

package session

import "golang.org/x/sys/unix"

// parentPID returns the parent of pid, or 0 when the kernel won't say.
func parentPID(pid int) int {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || kp == nil {
		return 0
	}
	return int(kp.Eproc.Ppid)
}
