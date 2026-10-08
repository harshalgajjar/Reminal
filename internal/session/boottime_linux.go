// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build linux

package session

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"time"
)

// bootTime is when this machine last started, per the kernel (btime in
// /proc/stat).
func bootTime() (time.Time, bool) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return time.Time{}, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(sc.Text(), "btime "); ok {
			if s, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64); err == nil && s > 0 {
				return time.Unix(s, 0), true
			}
		}
	}
	return time.Time{}, false
}
