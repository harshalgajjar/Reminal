// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build linux

package session

import (
	"os"
	"strconv"
	"strings"
)

// parentPID returns the parent of pid, or 0 when it can't be read.
// /proc/<pid>/stat field 4, read the same careful way procStartTime reads field
// 22: comm (field 2) is parenthesized and may itself contain spaces or ')', so
// the fields after it are counted from the LAST ')'.
func parentPID(pid int) int {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0
	}
	s := string(b)
	close := strings.LastIndexByte(s, ')')
	if close < 0 || close+2 >= len(s) {
		return 0
	}
	fields := strings.Fields(s[close+2:]) // first field here is state (3)
	if len(fields) < 2 {
		return 0
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0
	}
	return ppid
}
