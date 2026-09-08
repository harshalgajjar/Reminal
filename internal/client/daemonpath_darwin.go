// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build darwin

package client

import (
	"os"
	"path/filepath"
	"regexp"
)

// daemonProgramPath returns the binary the installed daemon service actually
// runs, or "" when there is no service (or it cannot be read).
//
// Read from the plist rather than assumed, because "we are running from a
// bundle" is NOT the same question as "we are running from THE daemon's
// bundle". An upgrade rewrites our own executable into whatever bundle it
// installed — which for a scratch or side-by-side install is somewhere else
// entirely — and a build that just replaced a binary the daemon has never run
// has no business restarting it.
func daemonProgramPath() string {
	u, err := targetUser()
	if err != nil {
		return ""
	}
	plistPath := filepath.Join(u.HomeDir, "Library", "LaunchAgents", daemonLabel+".plist")
	b, err := os.ReadFile(plistPath)
	if err != nil {
		return ""
	}
	return parseDaemonProgram(b)
}

// parseDaemonProgram pulls the first ProgramArguments string out of the plist.
// The plist is a small XML file we wrote ourselves, so a regex beats pulling
// in a parser for one field — and a miss returns "", which means "do not
// bounce", the safe answer.
var daemonProgramRe = regexp.MustCompile(`(?s)<key>ProgramArguments</key>\s*<array>\s*<string>([^<]+)</string>`)

func parseDaemonProgram(b []byte) string {
	m := daemonProgramRe.FindSubmatch(b)
	if m == nil {
		return ""
	}
	return string(m[1])
}

// sameInstall reports whether two paths resolve to the same file, following
// symlinks — an upgrade leaves the executable as a symlink into the bundle it
// installed, so a string compare would miss.
func sameInstall(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return ra == rb
}

// daemonBounceApplies reports whether this process is the very install the
// daemon service runs, and so may restart it after replacing that binary.
func daemonBounceApplies() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	return sameInstall(exe, daemonProgramPath())
}
