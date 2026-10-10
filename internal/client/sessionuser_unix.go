// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !windows

package client

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
)

// sessionUserFile is where a machine set up to run its sessions as a user of
// their own (not the root reminal runs as there) names that user, on one
// line. A machine image writes it; a person's own computer has none.
const sessionUserFile = "/etc/reminal/session-user"

// Stood in for by the tests: the file, reminal's own euid, and who must own
// the file and its directory (root).
var (
	sessionUserPath  = sessionUserFile
	geteuid          = os.Geteuid
	sessionUserOwner = uint32(0)
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
// nothing changes; with no such file, nothing changes either). The file and
// its directory are trusted only as root's own, writable by no one else, and
// it must name a user the machine has, never root: anything else is an
// error — a session refused, never started as root by default.
func machineSessionUser() (acct sessionAccount, ok bool, err error) {
	if geteuid() != 0 {
		return acct, false, nil
	}
	if _, err := os.Lstat(sessionUserPath); os.IsNotExist(err) {
		return acct, false, nil
	}
	b, err := readRootsOwn(sessionUserPath)
	if err != nil {
		return acct, false, fmt.Errorf("the machine's session user (%s): %w", sessionUserPath, err)
	}
	name, _, _ := strings.Cut(string(b), "\n")
	name = strings.TrimSpace(name)
	if name == "" || name == "root" {
		return acct, false, fmt.Errorf("the machine's session user (%s) names no user but root", sessionUserPath)
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
		return acct, false, fmt.Errorf("the machine's session user %q is root (uid 0)", name)
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

// readRootsOwn reads a small file only if it and its directory are what
// sessionUserOwner (root) alone could have written: neither a link, both
// owned by it, neither writable by group or others.
func readRootsOwn(p string) ([]byte, error) {
	for _, q := range []string{filepath.Dir(p), p} {
		fi, err := os.Lstat(q)
		if err != nil {
			return nil, err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return nil, fmt.Errorf("%s is a link", q)
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != sessionUserOwner {
			return nil, fmt.Errorf("%s is not root's own", q)
		}
		if fi.Mode().Perm()&0o022 != 0 {
			return nil, fmt.Errorf("%s is writable by others than root (%v)", q, fi.Mode().Perm())
		}
	}
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a file", p)
	}
	return io.ReadAll(io.LimitReader(f, 4<<10))
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
	// Its own ~/.local/bin first on its PATH (what it installs, npm's and
	// pip's), for it alone: never on reminal's own, which is root's.
	path := "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok && v != "" {
			path = v
		}
	}
	if own := filepath.Join(acct.home, ".local", "bin"); !slices.Contains(filepath.SplitList(path), own) {
		path = own + string(os.PathListSeparator) + path
	}
	cmd.Env = withEnv(env, "HOME="+acct.home, "USER="+acct.name, "LOGNAME="+acct.name, "PATH="+path)
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
