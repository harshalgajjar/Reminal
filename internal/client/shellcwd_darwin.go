// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build darwin

package client

import (
	"bytes"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Reading a process's working directory on macOS without cgo and without a
// subprocess.
//
// The obvious route, proc_pidinfo(), lives in libproc and so needs cgo, which a
// CGO_ENABLED=0 build cannot use — which is why this used to shell out to lsof.
// But libproc is a thin wrapper over the proc_info syscall, and that is callable
// directly. Same answer, no fork, no pipe, no PATH lookup: ~50µs against lsof's
// ~350ms, and nothing that can hang, spin, or outlive us.
//
//	int __proc_info(int callnum, int pid, int flavor,
//	                uint64_t arg, void *buffer, int buffersize)
const (
	sysProcInfo          = 336 // SYS_proc_info
	procInfoCallPID      = 2   // PROC_INFO_CALL_PIDINFO
	procPIDVnodePathInfo = 9   // PROC_PIDVNODEPATHINFO

	// struct proc_vnodepathinfo { vnode_info_path pvi_cdir; vnode_info_path pvi_rdir; }
	// struct vnode_info_path   { vnode_info vip_vi; char vip_path[MAXPATHLEN]; }
	// vnode_info is 152 bytes, MAXPATHLEN 1024, so each half is 1176 and the
	// cwd string starts 152 in. Verified against the real cwd in the tests, and
	// a wrong answer here is caught by the sanity check below rather than
	// trusted, since a bad offset would otherwise read neighbouring memory.
	vnodePathInfoSize = 2352
	cdirPathOffset    = 152
	maxPathLen        = 1024
)

// procCwdDarwin returns the working directory of pid, or "" if it cannot be
// read — the caller then falls back to lsof.
func procCwdDarwin(pid int) string {
	buf := make([]byte, vnodePathInfoSize)
	n, _, errno := unix.Syscall6(sysProcInfo, procInfoCallPID, uintptr(pid),
		procPIDVnodePathInfo, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if errno != 0 || int(n) < cdirPathOffset+1 {
		return ""
	}
	p := buf[cdirPathOffset : cdirPathOffset+maxPathLen]
	if i := bytes.IndexByte(p, 0); i >= 0 {
		p = p[:i]
	}
	// A path is absolute. Anything else means the offset is wrong for this
	// kernel, so say nothing and let lsof answer rather than report garbage.
	if len(p) == 0 || p[0] != '/' {
		return ""
	}
	return string(p)
}
