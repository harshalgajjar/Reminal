// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !windows

package pty

// ProcMark is procMark for the session's own bookkeeping outside this
// package: the process now at pid, named by when it started, and its
// parent's pid. ok false when it cannot be read.
func ProcMark(pid int) (mark string, ppid int, ok bool) { return procMark(pid) }
