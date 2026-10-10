// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !windows

package client

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

// sessionUserFile is where a machine set up to run its sessions as a user of
// their own (not the root reminal runs as there) names that user, on one
// line. A machine image writes it; a person's own computer has none.
const sessionUserFile = "/etc/reminal/session-user"

// Stood in for by the tests.
var (
	sessionUserPath = sessionUserFile
	geteuid         = os.Geteuid
)

var sessionUserNameRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// sessionAccount is the machine's session user, as asSessionUser runs a
// session as it.
type sessionAccount struct {
	name     string
	uid, gid uint32
	groups   []uint32
	home     string
}

// machineSessionUser is the user this machine's sessions run as: named by
// sessionUserFile, and only when reminal runs as root (ok false otherwise —
// nothing changes). A file naming a user the machine does not have is an
// error, never root by default.
func machineSessionUser() (acct sessionAccount, ok bool, err error) {
	if geteuid() != 0 {
		return acct, false, nil
	}
	b, err := os.ReadFile(sessionUserPath)
	if os.IsNotExist(err) {
		return acct, false, nil
	}
	if err != nil {
		return acct, false, fmt.Errorf("reading the machine's session user (%s): %w", sessionUserPath, err)
	}
	name, _, _ := strings.Cut(string(b), "\n")
	name = strings.TrimSpace(name)
	if name == "" || name == "root" {
		return acct, false, nil
	}
	if !sessionUserNameRe.MatchString(name) {
		return acct, false, fmt.Errorf("the machine's session user %q is not a user name", name)
	}
	u, err := user.Lookup(name)
	if err != nil {
		return acct, false, fmt.Errorf("the machine's session user %q: %w", name, err)
	}
	uid, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return acct, false, fmt.Errorf("the machine's session user %q: uid %q", name, u.Uid)
	}
	gid, err := strconv.ParseUint(u.Gid, 10, 32)
	if err != nil {
		return acct, false, fmt.Errorf("the machine's session user %q: gid %q", name, u.Gid)
	}
	if uid == 0 {
		return acct, false, nil
	}
	acct = sessionAccount{name: name, uid: uint32(uid), gid: uint32(gid), home: u.HomeDir}
	if ids, err := u.GroupIds(); err == nil {
		for _, g := range ids {
			if n, err := strconv.ParseUint(g, 10, 32); err == nil {
				acct.groups = append(acct.groups, uint32(n))
			}
		}
	}
	return acct, true, nil
}

// asSessionUser makes cmd, a session about to start, run as the machine's
// session user (machineSessionUser): its uid, gid and groups, and its HOME,
// USER and LOGNAME. Anywhere else nothing changes; a file naming a user the
// machine does not have refuses the session, so it never starts as root by
// mistake. Call it after prepareHandshake, which sets cmd.SysProcAttr.
func asSessionUser(cmd *exec.Cmd) error {
	acct, ok, err := machineSessionUser()
	if !ok {
		return err
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Credential = &syscall.Credential{Uid: acct.uid, Gid: acct.gid, Groups: acct.groups}
	env := cmd.Env
	if env == nil {
		env = os.Environ()
	}
	cmd.Env = withEnv(env, "HOME="+acct.home, "USER="+acct.name, "LOGNAME="+acct.name)
	return nil
}

// withEnv is env with each of set's keys set to its value (the old one
// dropped).
func withEnv(env []string, set ...string) []string {
	drop := map[string]bool{}
	for _, kv := range set {
		k, _, _ := strings.Cut(kv, "=")
		drop[k] = true
	}
	out := make([]string, 0, len(env)+len(set))
	for _, kv := range env {
		if k, _, _ := strings.Cut(kv, "="); !drop[k] {
			out = append(out, kv)
		}
	}
	return append(out, set...)
}
